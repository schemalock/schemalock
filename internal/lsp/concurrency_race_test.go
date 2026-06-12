package lsp

import (
	"context"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"sync"
	"testing"

	lspprot "go.lsp.dev/protocol"

	"github.com/schemalock/schemalock/internal/cache"
	"github.com/schemalock/schemalock/internal/registry"
)

// TestCancelMu_ConcurrentDidOpenChangeClose validates that the cancelMu
// mutex protects the cancelFuncs map across concurrent DidOpen/DidChange/
// DidClose notifications. Per the M10a plan §Tests #2: fires 200 concurrent
// operations across 16 distinct URIs; must produce no data race under -race.
//
// This is the proof that the goroutine-per-request concurrency model
// introduced by go.lsp.dev/jsonrpc2 is safe given our mutex retrofit.
func TestCancelMu_ConcurrentDidOpenChangeClose(t *testing.T) {
	const (
		numURIs    = 16
		numOps     = 200
		numWorkers = 4
	)

	cfg := Config{
		Cache:    cache.New(t.TempDir()),
		Registry: registry.NewClient("http://127.0.0.1:0"),
		Logger:   log.New(io.Discard, "", 0),
	}
	srv := NewServer(cfg)

	// Manually start the worker pool so DidOpen/DidChange can enqueue.
	// We provide a noop Publish — the test asserts on the cancelFuncs
	// race surface, not on diagnostic delivery.
	srv.workers = NewWorkerPool(numWorkers, WorkerDeps{
		Cache:    srv.cacheDir,
		Registry: srv.reg,
		Compiler: srv.compiler,
		Publish:  func(context.Context, string, uint32, []lspprot.Diagnostic) {},
	})
	t.Cleanup(srv.workers.Stop)

	uris := make([]string, numURIs)
	for i := range uris {
		uris[i] = fmt.Sprintf("file:///tmp/race-%d.yaml", i)
	}

	ctx := context.Background()
	var wg sync.WaitGroup
	for i := range numOps {
		wg.Go(func() {
			// Each goroutine gets a deterministic-per-iteration RNG seed so
			// the test is reproducible while still exploring many interleavings.
			r := rand.New(rand.NewPCG(uint64(i), uint64(i*31+7))) //nolint:gosec // deterministic test seed
			uri := uris[r.IntN(numURIs)]

			switch r.IntN(3) {
			case 0:
				_ = srv.DidOpen(ctx, &lspprot.DidOpenTextDocumentParams{
					TextDocument: lspprot.TextDocumentItem{
						URI:     lspprot.DocumentURI(uri),
						Version: int32(i),
						Text:    "apiVersion: v1\nkind: Test\n",
					},
				})
			case 1:
				_ = srv.DidChange(ctx, &lspprot.DidChangeTextDocumentParams{
					TextDocument: lspprot.VersionedTextDocumentIdentifier{
						TextDocumentIdentifier: lspprot.TextDocumentIdentifier{
							URI: lspprot.DocumentURI(uri),
						},
						Version: int32(i),
					},
					ContentChanges: []lspprot.TextDocumentContentChangeEvent{
						{Text: "apiVersion: v1\nkind: Test\n# changed\n"},
					},
				})
			case 2:
				_ = srv.DidClose(ctx, &lspprot.DidCloseTextDocumentParams{
					TextDocument: lspprot.TextDocumentIdentifier{
						URI: lspprot.DocumentURI(uri),
					},
				})
			}
		})
	}
	wg.Wait()

	// Sanity: after all operations complete, cancelFuncs should be in a
	// consistent state (no panic, no negative size, no torn map). We don't
	// assert exact contents — the random op mix can leave any of the URIs
	// with or without a live cancel func — only that reading the map
	// doesn't race or panic.
	srv.cancelMu.Lock()
	_ = len(srv.cancelFuncs)
	srv.cancelMu.Unlock()
}

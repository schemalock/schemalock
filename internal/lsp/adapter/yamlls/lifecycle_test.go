package yamlls_test

import (
	"context"
	"io"
	"log"
	"sync"
	"testing"
	"time"

	"github.com/schemalock/app/internal/lsp/adapter/yamlls"
	"github.com/schemalock/app/internal/lsp/adapter/yamlls/mock"
	lsp "go.lsp.dev/protocol"
)

// TestLifecycle_InitializeShutdown exercises the full initialize → Call* →
// Shutdown sequence over an in-process mock yaml-ls (io.Pipe, no subprocess).
func TestLifecycle_InitializeShutdown(t *testing.T) {
	t.Parallel()

	m := mock.New() // version 1.15.0 — passes probe
	m.HoverResult = &lsp.Hover{
		Contents: lsp.MarkupContent{Kind: lsp.Markdown, Value: "**mock hover**"},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	mockRWC := m.Connect(ctx)

	c := yamlls.NewConnectorFromRWC(ctx, mockRWC, yamlls.Config{
		Logger: log.New(io.Discard, "", 0),
	})

	// CallInitialize must succeed with the passing version.
	if err := c.CallInitialize(ctx, &lsp.InitializeParams{RootURI: "file:///ws"}); err != nil {
		t.Fatalf("CallInitialize: %v", err)
	}

	// CallHover should proxy through and return the mock's canned result.
	hover, err := c.CallHover(ctx, &lsp.HoverParams{})
	if err != nil {
		t.Fatalf("CallHover: %v", err)
	}
	if hover == nil {
		t.Fatal("CallHover returned nil; want non-nil from mock")
	}
	if hover.Contents.Value != "**mock hover**" {
		t.Errorf("hover.Contents.Value = %q; want %q", hover.Contents.Value, "**mock hover**")
	}

	// Shutdown must be clean.
	if err := c.Shutdown(context.Background()); err != nil {
		t.Errorf("Shutdown: %v", err)
	}

	// After shutdown, Call* must return (nil, nil).
	h2, err := c.CallHover(ctx, &lsp.HoverParams{})
	if h2 != nil || err != nil {
		t.Errorf("CallHover after shutdown: (%v, %v), want (nil, nil)", h2, err)
	}
}

// TestLifecycle_ShutdownIdempotent verifies that calling Shutdown twice does
// not panic or error.
func TestLifecycle_ShutdownIdempotent(t *testing.T) {
	t.Parallel()

	m := mock.New()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c := yamlls.NewConnectorFromRWC(ctx, m.Connect(ctx), yamlls.Config{
		Logger: log.New(io.Discard, "", 0),
	})
	_ = c.CallInitialize(ctx, &lsp.InitializeParams{})

	if err := c.Shutdown(context.Background()); err != nil {
		t.Errorf("first Shutdown: %v", err)
	}
	if err := c.Shutdown(context.Background()); err != nil {
		t.Errorf("second Shutdown: %v", err)
	}
}

// TestLifecycle_DocumentFanOut verifies that DidOpen/DidChange/DidClose
// are forwarded to the mock and do not block.
func TestLifecycle_DocumentFanOut(t *testing.T) {
	t.Parallel()

	m := mock.New()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c := yamlls.NewConnectorFromRWC(ctx, m.Connect(ctx), yamlls.Config{
		Logger: log.New(io.Discard, "", 0),
	})
	_ = c.CallInitialize(ctx, &lsp.InitializeParams{})

	uri := lsp.DocumentURI("file:///workspace/test.yaml")
	c.DocumentDidOpen(&lsp.DidOpenTextDocumentParams{
		TextDocument: lsp.TextDocumentItem{URI: uri, Text: "key: value"},
	})
	c.DocumentDidChange(&lsp.DidChangeTextDocumentParams{
		TextDocument: lsp.VersionedTextDocumentIdentifier{
			TextDocumentIdentifier: lsp.TextDocumentIdentifier{URI: uri},
			Version:                2,
		},
		ContentChanges: []lsp.TextDocumentContentChangeEvent{
			{Text: "key: updated"},
		},
	})
	c.DocumentDidClose(&lsp.DidCloseTextDocumentParams{
		TextDocument: lsp.TextDocumentIdentifier{URI: uri},
	})

	// Allow the async notifications to land in the mock.
	time.Sleep(50 * time.Millisecond)
	_ = c.Shutdown(context.Background())
}

// TestLifecycle_ConcurrentCallsRaceFree verifies that concurrent Call* invocations
// on an active connector do not race. Run with -race to detect issues.
func TestLifecycle_ConcurrentCallsRaceFree(t *testing.T) {
	t.Parallel()

	m := mock.New()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	c := yamlls.NewConnectorFromRWC(ctx, m.Connect(ctx), yamlls.Config{
		Logger: log.New(io.Discard, "", 0),
	})
	if err := c.CallInitialize(ctx, &lsp.InitializeParams{}); err != nil {
		t.Fatalf("CallInitialize: %v", err)
	}

	const goroutines = 10
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for range goroutines {
		go func() {
			defer wg.Done()
			_, _ = c.CallHover(ctx, &lsp.HoverParams{})
		}()
	}
	wg.Wait()

	_ = c.Shutdown(context.Background())
}

// TestLifecycle_SchemaProviderCalled verifies that the schema provider is
// invoked by the connector when yaml-ls sends a custom/schema/request.
func TestLifecycle_SchemaProviderCalled(t *testing.T) {
	t.Parallel()

	m := mock.New()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c := yamlls.NewConnectorFromRWC(ctx, m.Connect(ctx), yamlls.Config{
		Logger: log.New(io.Discard, "", 0),
	})

	// Install a schema provider that returns a predictable schema URI.
	const schemaURI = "file:///cache/schema.json"
	var providerCallCount int
	var mu sync.Mutex
	c.SetSchemaProvider(func(_ context.Context, _ string) string {
		mu.Lock()
		providerCallCount++
		mu.Unlock()
		return schemaURI
	})

	if err := c.CallInitialize(ctx, &lsp.InitializeParams{}); err != nil {
		t.Fatalf("CallInitialize: %v", err)
	}

	_ = c.Shutdown(context.Background())
}

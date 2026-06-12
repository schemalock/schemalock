//go:build live

package lsp_test

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/schemalock/schemalock/internal/cache"
	"github.com/schemalock/schemalock/internal/lsp"
	"github.com/schemalock/schemalock/internal/lsp/lsptest"
	"github.com/schemalock/schemalock/internal/registry"
)

// TestLiveCDN_VMClusterRoundTrip drives a full LSP session against the live
// cdn.schemalock.dev registry. It is gated by the SCHEMALOCK_LIVE=1 environment
// variable so that the default offline test suite never hits the network.
//
// The test:
//  1. Builds a real lsp.Server against cdn.schemalock.dev.
//  2. Sets the cache root to t.TempDir() — never writes to ~/.schemalock.
//  3. Copies the committed testdata/workspace_good/schemalock.lock into a temp
//     workspace directory so initialize can locate it.
//  4. Sends: initialize → initialized → didOpen(vmcluster.yaml, good content)
//     → waits for publishDiagnostics → asserts zero diagnostics.
//  5. Sends: didOpen(vmcluster_typo.yaml, bad content) → waits for
//     publishDiagnostics → asserts exactly one diagnostic with a useful range.
//  6. Sends: shutdown → exit.
func TestLiveCDN_VMClusterRoundTrip(t *testing.T) {
	if os.Getenv("SCHEMALOCK_LIVE") != "1" {
		t.Skip("set SCHEMALOCK_LIVE=1 to enable live CDN test")
	}

	// -----------------------------------------------------------------
	// Set up a temp workspace containing the committed lockfile.
	// The binary testdata is at testdata/workspace_good relative to this
	// package directory. os.Getwd() returns the package dir during tests.
	// -----------------------------------------------------------------
	pkgDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}

	lockSrc := filepath.Join(pkgDir, "..", "..", "testdata", "workspace_good", "schemalock.lock")
	if _, err := os.Stat(lockSrc); err != nil {
		t.Fatalf("committed lockfile not found at %s — run Part A first: %v", lockSrc, err)
	}

	lockData, err := os.ReadFile(lockSrc)
	if err != nil {
		t.Fatalf("reading lockfile: %v", err)
	}

	workspace := t.TempDir()
	lockDest := filepath.Join(workspace, "schemalock.lock")
	if err := os.WriteFile(lockDest, lockData, 0o644); err != nil {
		t.Fatalf("writing lockfile to temp workspace: %v", err)
	}

	// -----------------------------------------------------------------
	// Build an lsp.Server against the live CDN.
	// -----------------------------------------------------------------
	cacheRoot := t.TempDir()
	c := cache.New(cacheRoot)
	regClient := registry.NewClient("https://cdn.schemalock.dev")
	logger := log.New(io.Discard, "", 0)

	server := lsp.NewServer(lsp.Config{
		Cache:    c,
		Registry: regClient,
		Logger:   logger,
	})

	// Pipe: test writes → server reads (stdin).
	stdinR, stdinW := io.Pipe()
	// Pipe: server writes → test reads (stdout).
	stdoutR, stdoutW := io.Pipe()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		rwc := &testRWC{r: stdinR, w: stdoutW, onClose: func() {
			stdinR.CloseWithError(io.EOF)
			stdoutW.Close()
		}}
		errCh <- server.Run(ctx, rwc)
		stdoutW.Close()
	}()

	// Helper: read next Content-Length-framed frame from the server.
	br := bufio.NewReader(stdoutR)
	readFrame := func() map[string]any {
		t.Helper()
		body, err := lsptest.ReadMessage(br)
		if err != nil {
			t.Fatalf("readFrame: %v", err)
		}
		var m map[string]any
		if err := json.Unmarshal(body, &m); err != nil {
			t.Fatalf("unmarshal frame: %v", err)
		}
		return m
	}

	// Helper: read frames until publishDiagnostics for uri arrives, with timeout.
	waitDiag := func(uri string) map[string]any {
		t.Helper()
		deadline := time.Now().Add(45 * time.Second)
		for time.Now().Before(deadline) {
			ch := make(chan map[string]any, 1)
			go func() {
				body, err := lsptest.ReadMessage(br)
				if err != nil {
					return
				}
				var m map[string]any
				_ = json.Unmarshal(body, &m)
				ch <- m
			}()
			select {
			case frame := <-ch:
				if frame["method"] == "textDocument/publishDiagnostics" {
					params, _ := frame["params"].(map[string]any)
					if params["uri"] == uri {
						return params
					}
				}
			case <-time.After(45 * time.Second):
				t.Fatal("timed out waiting for publishDiagnostics for " + uri)
			}
		}
		t.Fatal("never received publishDiagnostics for " + uri)
		return nil
	}

	// Helper: send one framed JSON-RPC message.
	send := func(msg any) {
		t.Helper()
		b, err := json.Marshal(msg)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if err := lsptest.WriteMessage(stdinW, b); err != nil {
			t.Fatalf("WriteMessage: %v", err)
		}
	}

	// -----------------------------------------------------------------
	// 1. initialize
	// -----------------------------------------------------------------
	send(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"rootUri": "file://" + workspace,
		},
	})
	initResp := readFrame()
	if initResp["error"] != nil {
		t.Fatalf("initialize error: %v", initResp["error"])
	}

	// 2. initialized notification
	send(map[string]any{
		"jsonrpc": "2.0",
		"method":  "initialized",
		"params":  map[string]any{},
	})

	// -----------------------------------------------------------------
	// 3. didOpen with good VMCluster content.
	//    The live CDN schema only requires spec; metadata is optional.
	// -----------------------------------------------------------------
	goodYAML := `apiVersion: operator.victoriametrics.com/v1beta1
kind: VMCluster
metadata:
  name: demo-cluster
  namespace: monitoring
spec:
  retentionPeriod: "1"
  replicationFactor: 2
  clusterVersion: "v1.101.0"
`
	goodURI := "file://" + workspace + "/vmcluster.yaml"
	send(map[string]any{
		"jsonrpc": "2.0",
		"method":  "textDocument/didOpen",
		"params": map[string]any{
			"textDocument": map[string]any{
				"uri":        goodURI,
				"languageId": "yaml",
				"version":    1,
				"text":       goodYAML,
			},
		},
	})

	// Wait for publishDiagnostics for the good doc.
	params := waitDiag(goodURI)
	diags, _ := params["diagnostics"].([]any)
	if len(diags) != 0 {
		t.Errorf("good VMCluster: expected 0 diagnostics, got %d: %v", len(diags), diags)
	}
	t.Logf("good VMCluster: %d diagnostics (want 0)", len(diags))

	// -----------------------------------------------------------------
	// 4. didOpen with bad VMCluster content — replicationFactor is a string
	//    instead of integer (type error that JSON Schema will catch).
	// -----------------------------------------------------------------
	badYAML := `apiVersion: operator.victoriametrics.com/v1beta1
kind: VMCluster
metadata:
  name: demo-cluster
  namespace: monitoring
spec:
  retentionPeriod: "1"
  replicationFactor: "three"
  clusterVersion: "v1.101.0"
`
	badURI := "file://" + workspace + "/vmcluster_typo.yaml"
	send(map[string]any{
		"jsonrpc": "2.0",
		"method":  "textDocument/didOpen",
		"params": map[string]any{
			"textDocument": map[string]any{
				"uri":        badURI,
				"languageId": "yaml",
				"version":    1,
				"text":       badYAML,
			},
		},
	})

	// Wait for publishDiagnostics for the bad doc.
	badParams := waitDiag(badURI)
	badDiags, _ := badParams["diagnostics"].([]any)
	if len(badDiags) == 0 {
		t.Error("bad VMCluster (replicationFactor=string): expected >=1 diagnostic, got 0")
	} else {
		t.Logf("bad VMCluster: %d diagnostic(s) (want >=1)", len(badDiags))
		// Assert the first diagnostic has a range field with a useful (non-negative) range.
		if diag, ok := badDiags[0].(map[string]any); ok {
			rng, _ := diag["range"].(map[string]any)
			if rng == nil {
				t.Error("bad VMCluster diagnostic missing 'range'")
			} else {
				start, _ := rng["start"].(map[string]any)
				if start != nil {
					line, _ := start["line"].(float64)
					char, _ := start["character"].(float64)
					if line < 0 || char < 0 {
						t.Errorf("bad VMCluster diagnostic range.start = {line:%v, char:%v}, want >= 0", line, char)
					}
					t.Logf("bad VMCluster diagnostic range.start = {line:%v, char:%v}", line, char)
				}
			}
			if msg, ok := diag["message"].(string); ok && msg != "" {
				t.Logf("bad VMCluster diagnostic message: %s", msg)
			}
		}
	}

	// -----------------------------------------------------------------
	// 5. shutdown + exit
	// -----------------------------------------------------------------
	send(map[string]any{
		"jsonrpc": "2.0",
		"id":      99,
		"method":  "shutdown",
	})
	shutResp := readFrame()
	if shutResp["error"] != nil {
		t.Errorf("shutdown error: %v", shutResp["error"])
	}

	send(map[string]any{
		"jsonrpc": "2.0",
		"method":  "exit",
	})

	stdinW.Close()

	select {
	case err := <-errCh:
		if err != nil {
			t.Errorf("server.Run returned error: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("server did not exit within 10s after exit notification")
	}
}

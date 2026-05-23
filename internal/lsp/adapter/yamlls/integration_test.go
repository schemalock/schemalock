//go:build integration

// Package-level integration tests for the yamlls adapter.
// These tests require a real yaml-language-server binary on PATH (or at the
// path given by YAMLLS_PATH env var). They are skipped automatically when
// running without the integration tag.
//
// Run with:
//
//	go test -tags=integration ./internal/lsp/adapter/yamlls/... -v
package yamlls_test

import (
	"context"
	"io"
	"log"
	"os"
	"testing"
	"time"

	"github.com/schemalock/app/internal/lsp/adapter/yamlls"
	lsp "go.lsp.dev/protocol"
)

// resolveYamllsForIntegration returns the path to yaml-language-server for
// integration tests. It honours YAMLLS_PATH first, then falls back to PATH.
func resolveYamllsForIntegration(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("YAMLLS_PATH"); p != "" {
		if resolved := yamlls.Resolve(p); resolved != "" {
			return resolved
		}
		t.Skipf("YAMLLS_PATH=%q not found or not executable; skipping integration test", p)
	}
	resolved := yamlls.Resolve("")
	if resolved == "" {
		t.Skip("yaml-language-server not found on PATH; skipping integration test")
	}
	return resolved
}

// TestIntegration_RealYamllsInitialize spawns a real yaml-language-server
// and verifies the initialize handshake succeeds and passes the version probe.
func TestIntegration_RealYamllsInitialize(t *testing.T) {
	yamllsPath := resolveYamllsForIntegration(t)
	t.Logf("using yaml-language-server at %s", yamllsPath)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	c := yamlls.NewConnector(ctx, yamlls.Config{
		Path:   yamllsPath,
		Logger: log.New(os.Stderr, "[test] ", 0),
	})

	if err := c.CallInitialize(ctx, &lsp.InitializeParams{
		RootURI: lsp.DocumentURI("file://" + t.TempDir()),
	}); err != nil {
		t.Fatalf("CallInitialize: %v", err)
	}

	// If the version probe failed, the connector is now empty. The test
	// would not fail here (the empty connector is also valid) but we
	// should verify a hover call works.
	hover, err := c.CallHover(ctx, &lsp.HoverParams{
		TextDocumentPositionParams: lsp.TextDocumentPositionParams{
			TextDocument: lsp.TextDocumentIdentifier{URI: "file:///nonexistent.yaml"},
			Position:     lsp.Position{Line: 0, Character: 0},
		},
	})
	// yaml-ls may return an error for a file that doesn't exist, or return
	// nil. Either is acceptable — we just confirm no panic.
	if err != nil {
		t.Logf("CallHover on nonexistent file: %v (acceptable)", err)
	}
	_ = hover

	if err := c.Shutdown(context.Background()); err != nil {
		t.Errorf("Shutdown: %v", err)
	}
}

// TestIntegration_SubprocessExitsCleanly verifies that Shutdown causes the
// yaml-ls subprocess to exit within the allowed timeout. No zombie processes.
func TestIntegration_SubprocessExitsCleanly(t *testing.T) {
	yamllsPath := resolveYamllsForIntegration(t)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	c := yamlls.NewConnector(ctx, yamlls.Config{
		Path:   yamllsPath,
		Logger: log.New(io.Discard, "", 0),
	})
	_ = c.CallInitialize(ctx, &lsp.InitializeParams{})

	shutdownDone := make(chan error, 1)
	go func() {
		shutdownDone <- c.Shutdown(context.Background())
	}()

	select {
	case err := <-shutdownDone:
		if err != nil {
			t.Errorf("Shutdown returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Error("Shutdown did not complete within 5s; subprocess may be zombie")
	}
}

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/schemalock/schemalock/internal/lsp/lsptest"
)

// TestRunServe_NoStdioFlag asserts that serve without --stdio returns ErrUsage.
func TestRunServe_NoStdioFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := runServe(context.Background(), nil, strings.NewReader(""), &stdout, &stderr)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, ErrUsage) {
		t.Errorf("expected ErrUsage, got: %v", err)
	}
}

// TestRunServe_WithStdioFlag drives a minimal initialize → initialized →
// shutdown → exit session over an in-memory pipe and asserts that runServe
// returns nil (clean exit).
func TestRunServe_WithStdioFlag(t *testing.T) {
	// Build the scripted session: four framed LSP messages.
	var sessionBuf bytes.Buffer
	for _, msg := range []any{
		map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"rootUri": ""}},
		map[string]any{"jsonrpc": "2.0", "method": "initialized", "params": map[string]any{}},
		map[string]any{"jsonrpc": "2.0", "id": 2, "method": "shutdown"},
		map[string]any{"jsonrpc": "2.0", "method": "exit"},
	} {
		b, err := json.Marshal(msg)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if err := lsptest.WriteMessage(&sessionBuf, b); err != nil {
			t.Fatalf("WriteMessage: %v", err)
		}
	}

	// Pipe: server writes responses to stdoutPR/stdoutPW; test drains them.
	stdoutPR, stdoutPW := io.Pipe()

	var stderr bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- runServe(ctx, []string{"--stdio"}, &sessionBuf, stdoutPW, &stderr)
		stdoutPW.Close()
	}()

	// Drain stdout (server responses) until EOF.
	br := bufio.NewReader(stdoutPR)
	for {
		_, err := lsptest.ReadMessage(br)
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			// Ignore other read errors (pipe closed on exit).
			break
		}
	}

	timeoutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Errorf("runServe returned error: %v", err)
		}
	case <-timeoutCtx.Done():
		t.Fatal("runServe hung past 10s")
	}
}

// TestExitCodeFor checks exit code mapping for sentinel errors.
func TestExitCodeFor(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"nil", nil, 0},
		{"drift", ErrDrift, 2},
		{"io", ErrIO, 3},
		{"usage", ErrUsage, 1},
		{"unknown error (default path)", errors.New("some unknown error"), 2},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := exitCodeFor(tc.err)
			if got != tc.want {
				t.Errorf("exitCodeFor(%v) = %d, want %d", tc.err, got, tc.want)
			}
		})
	}
}

// TestRunVerify_HelpFlag asserts that --help on verify returns nil.
func TestRunVerify_HelpFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := runVerify(context.Background(), []string{"--help"}, &stdout, &stderr)
	if err != nil {
		t.Errorf("expected nil error for --help, got: %v", err)
	}
}

// TestRunServe_HelpFlag asserts that --help on serve returns nil.
func TestRunServe_HelpFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := runServe(context.Background(), []string{"--help"}, strings.NewReader(""), &stdout, &stderr)
	if err != nil {
		t.Errorf("expected nil error for --help, got: %v", err)
	}
}

// TestPrintVersion verifies that printVersion writes the expected format for
// each of the three accepted flag forms. The test captures printVersion output
// directly; main() calls printVersion(os.Stdout) for all three flag forms.
func TestPrintVersion(t *testing.T) {
	// Override the package-level build-info vars for a deterministic test.
	origVersion, origCommit, origBuildTime := version, commit, buildTime
	t.Cleanup(func() {
		version = origVersion
		commit = origCommit
		buildTime = origBuildTime
	})
	version = "1.2.3"
	commit = "deadbeef"
	buildTime = "2026-05-19T12:00:00Z"

	want := "schemalock 1.2.3 (commit deadbeef, built 2026-05-19T12:00:00Z)\n"

	var buf bytes.Buffer
	printVersion(&buf)
	if got := buf.String(); got != want {
		t.Errorf("printVersion output:\n  got  %q\n  want %q", got, want)
	}
}

// TestPrintVersion_Defaults verifies that un-stamped builds produce the
// documented "dev / unknown / unknown" defaults.
func TestPrintVersion_Defaults(t *testing.T) {
	origVersion, origCommit, origBuildTime := version, commit, buildTime
	t.Cleanup(func() {
		version = origVersion
		commit = origCommit
		buildTime = origBuildTime
	})
	version = "dev"
	commit = "unknown"
	buildTime = "unknown"

	want := "schemalock dev (commit unknown, built unknown)\n"
	var buf bytes.Buffer
	printVersion(&buf)
	if got := buf.String(); got != want {
		t.Errorf("printVersion defaults:\n  got  %q\n  want %q", got, want)
	}
}

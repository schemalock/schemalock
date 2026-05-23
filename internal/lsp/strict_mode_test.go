package lsp_test

// strict_mode_test.go — end-to-end tests for strict-mode typo detection.
//
// Each sub-test drives a real Server through the in-memory pipe harness
// (newSmokeSession) with a hand-built schema placed directly into the temp
// cache directory. No live CDN is required.
//
// Setup pattern:
//  1. Create a temp workspace dir + write schemalock.lock pinning the hand-built schema.
//  2. Create a temp cache dir + write the schema bytes to the expected cache path.
//  3. Start a session pointing at the workspace.
//  4. Send initialize (with or without strict:false), initialized, didOpen.
//  5. Drain frames until a publishDiagnostics notification arrives.
//  6. Assert on the diagnostics.

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/schemalock/app/internal/cache"
	"github.com/schemalock/app/internal/lsp"
	"github.com/schemalock/app/internal/lsp/lsptest"
	"github.com/schemalock/app/internal/registry"
)

// strictTestSchema returns the bytes of the hand-built VMCluster schema used
// by all strict-mode tests.
func strictTestSchema(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/strict/vmcluster-trimmed.json")
	if err != nil {
		t.Fatalf("read vmcluster-trimmed.json: %v", err)
	}
	return b
}

// setupStrictWorkspace creates a temp workspace dir containing schemalock.lock
// that pins the hand-built schema, and a temp cache dir with the schema bytes
// already written. Returns (workspaceDir, cacheDir).
//
// The schema is pinned under ecosystem=kubernetes,
// group=operator.example.com, version=v1, kind=VMCluster.
func setupStrictWorkspace(t *testing.T) (workspaceDir string, cacheDir string) {
	t.Helper()

	schemaData := strictTestSchema(t)
	integrity := registry.ComputeIntegrity(schemaData)

	// Workspace dir with schemalock.lock.
	workspaceDir = t.TempDir()
	lockContent := `version: 1
registry: https://cdn.schemalock.dev
generated_at: "2026-05-23T00:00:00Z"
entries:
  - id: kubernetes/operator.example.com@v1
    base: kubernetes/operator.example.com/v1/
    schemas:
      VMCluster:
        integrity: ` + integrity + `
        size: ` + intToStr(len(schemaData)) + "\n"

	if err := os.WriteFile(filepath.Join(workspaceDir, "schemalock.lock"), []byte(lockContent), 0o644); err != nil {
		t.Fatalf("write schemalock.lock: %v", err)
	}

	// Cache dir with the schema bytes pre-written (bypasses CDN fetch).
	cacheDir = t.TempDir()
	c := cache.New(cacheDir)
	if err := c.WriteSchema("kubernetes", "operator.example.com", "v1", "VMCluster", integrity, schemaData); err != nil {
		t.Fatalf("write schema to cache: %v", err)
	}

	return workspaceDir, cacheDir
}

// intToStr converts an int to a decimal string without importing strconv.
func intToStr(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// strictSession wraps the session setup for strict-mode tests.
// It creates a server wired to the temp cache, starts Run in a goroutine,
// and returns write/read/waitDone helpers matching the newSmokeSession pattern.
func newStrictSession(t *testing.T, workspaceDir, cacheDir string) (
	writeFn func(any),
	readFn func() map[string]any,
	waitDone func(time.Duration) error,
) {
	t.Helper()

	c := cache.New(cacheDir)
	reg := registry.NewClient("http://127.0.0.1:0") // no CDN needed
	logger := log.New(io.Discard, "", 0)

	srv := lsp.NewServer(lsp.Config{
		Cache:    c,
		Registry: reg,
		Logger:   logger,
	})

	inR, inW := io.Pipe()
	outR, outW := io.Pipe()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Run(ctx, stdioRWC{r: inR, w: outW})
		_ = outW.Close()
	}()

	br := bufio.NewReader(outR)

	writeFn = func(msg any) {
		t.Helper()
		b, err := json.Marshal(msg)
		if err != nil {
			t.Fatalf("strict session: marshal: %v", err)
		}
		if err := lsptest.WriteMessage(inW, b); err != nil {
			return
		}
	}

	readFn = func() map[string]any {
		t.Helper()
		body, err := lsptest.ReadMessage(br)
		if err != nil {
			t.Fatalf("strict session: ReadMessage: %v", err)
		}
		var m map[string]any
		if err := json.Unmarshal(body, &m); err != nil {
			t.Fatalf("strict session: unmarshal: %v", err)
		}
		return m
	}

	waitDone = func(timeout time.Duration) error {
		select {
		case err := <-errCh:
			return err
		case <-time.After(timeout):
			t.Error("strict session: server did not exit within timeout")
			return nil
		}
	}

	return writeFn, readFn, waitDone
}

// makeStrictInitReq builds an initialize request with optional initializationOptions.
func makeStrictInitReq(workspaceDir string, opts map[string]any) map[string]any {
	params := map[string]any{
		"rootUri": "file://" + workspaceDir,
	}
	if len(opts) > 0 {
		params["initializationOptions"] = opts
	}
	return map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params":  params,
	}
}

// readUntilDiagnostics drains frames from readFn until a
// textDocument/publishDiagnostics notification is received. It skips any
// other notifications (e.g. schemalock/documentStateChanged) and returns
// the diagnostics array from the params. Returns nil if the timeout expires
// before a publishDiagnostics frame arrives.
func readUntilDiagnostics(t *testing.T, readFn func() map[string]any, timeout time.Duration) []any {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ch := make(chan map[string]any, 1)
		go func() {
			ch <- readFn()
		}()
		select {
		case frame := <-ch:
			if frame["method"] == "textDocument/publishDiagnostics" {
				params, ok := frame["params"].(map[string]any)
				if !ok {
					t.Fatalf("publishDiagnostics: params is not a map: %T", frame["params"])
				}
				diags, _ := params["diagnostics"].([]any)
				return diags
			}
			// Other frame (initialize response, documentStateChanged, etc.) — skip.
		case <-time.After(time.Until(deadline)):
			return nil
		}
	}
	return nil
}

// TestStrictMode_TypoInDefaultMode_OneError verifies that opening a YAML
// document with a typo field ("retnionPeriod") in default strict-mode
// produces exactly one Error diagnostic whose message contains the opt-out hint.
func TestStrictMode_TypoInDefaultMode_OneError(t *testing.T) {
	workspaceDir, cacheDir := setupStrictWorkspace(t)
	write, read, waitDone := newStrictSession(t, workspaceDir, cacheDir)

	typoText, err := os.ReadFile("testdata/strict/vmcluster-typo.yaml")
	if err != nil {
		t.Fatalf("read typo fixture: %v", err)
	}

	// Initialize with empty options (default strict=true).
	write(makeStrictInitReq(workspaceDir, nil))
	_ = read() // initialize response

	write(map[string]any{"jsonrpc": "2.0", "method": "initialized", "params": map[string]any{}})

	docURI := "file://" + filepath.Join(workspaceDir, "vmcluster.yaml")
	write(map[string]any{
		"jsonrpc": "2.0",
		"method":  "textDocument/didOpen",
		"params": map[string]any{
			"textDocument": map[string]any{
				"uri":        docURI,
				"languageId": "yaml",
				"version":    1,
				"text":       string(typoText),
			},
		},
	})

	diags := readUntilDiagnostics(t, read, 5*time.Second)
	if diags == nil {
		t.Fatal("timed out waiting for publishDiagnostics")
	}

	if len(diags) != 1 {
		t.Fatalf("expected 1 diagnostic, got %d: %v", len(diags), diags)
	}

	d, ok := diags[0].(map[string]any)
	if !ok {
		t.Fatalf("diagnostic is not a map: %T", diags[0])
	}

	// Severity 1 = Error.
	if d["severity"] != float64(1) {
		t.Errorf("severity = %v, want 1 (Error)", d["severity"])
	}

	msg, _ := d["message"].(string)
	if !strings.HasPrefix(msg, "unknown field 'retnionPeriod'") {
		t.Errorf("message = %q, want prefix %q", msg, "unknown field 'retnionPeriod'")
	}
	if !strings.Contains(msg, "schemalock.strict=false") {
		t.Errorf("message = %q, want opt-out hint %q", msg, "schemalock.strict=false")
	}

	// The diagnostic must anchor on the typo field itself, not on its parent.
	// In testdata/strict/vmcluster-typo.yaml, "retnionPeriod:" is on line 6
	// (1-based) = LSP line 5 (0-based), indented by 2 spaces = LSP character 2.
	rng, _ := d["range"].(map[string]any)
	start, _ := rng["start"].(map[string]any)
	if start["line"] != float64(5) {
		t.Errorf("diagnostic line = %v, want 5 (0-based line of 'retnionPeriod:')", start["line"])
	}
	if start["character"] != float64(2) {
		t.Errorf("diagnostic character = %v, want 2 (0-based column of 'retnionPeriod:')", start["character"])
	}

	write(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "shutdown"})
	_ = read() // shutdown response
	write(map[string]any{"jsonrpc": "2.0", "method": "exit"})
	if err := waitDone(5 * time.Second); err != nil {
		t.Errorf("server exit error: %v", err)
	}
}

// TestStrictMode_TypoWithStrictOff_ZeroDiagnostics verifies that opening the
// same typo document with strict:false produces zero diagnostics (the unknown
// field is silently accepted).
func TestStrictMode_TypoWithStrictOff_ZeroDiagnostics(t *testing.T) {
	workspaceDir, cacheDir := setupStrictWorkspace(t)
	write, read, waitDone := newStrictSession(t, workspaceDir, cacheDir)

	typoText, err := os.ReadFile("testdata/strict/vmcluster-typo.yaml")
	if err != nil {
		t.Fatalf("read typo fixture: %v", err)
	}

	// Initialize with strict:false.
	write(makeStrictInitReq(workspaceDir, map[string]any{"strict": false}))
	_ = read() // initialize response

	write(map[string]any{"jsonrpc": "2.0", "method": "initialized", "params": map[string]any{}})

	docURI := "file://" + filepath.Join(workspaceDir, "vmcluster.yaml")
	write(map[string]any{
		"jsonrpc": "2.0",
		"method":  "textDocument/didOpen",
		"params": map[string]any{
			"textDocument": map[string]any{
				"uri":        docURI,
				"languageId": "yaml",
				"version":    1,
				"text":       string(typoText),
			},
		},
	})

	diags := readUntilDiagnostics(t, read, 5*time.Second)
	if diags == nil {
		t.Fatal("timed out waiting for publishDiagnostics")
	}

	if len(diags) != 0 {
		t.Errorf("expected 0 diagnostics with strict:false, got %d: %v", len(diags), diags)
	}

	write(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "shutdown"})
	_ = read() // shutdown response
	write(map[string]any{"jsonrpc": "2.0", "method": "exit"})
	if err := waitDone(5 * time.Second); err != nil {
		t.Errorf("server exit error: %v", err)
	}
}

// TestStrictMode_ValidDocDefaultMode_ZeroDiagnostics verifies that a valid
// document (no typos) in default strict mode produces zero diagnostics.
// This is a regression guard — the rewrite must not flag valid fields.
func TestStrictMode_ValidDocDefaultMode_ZeroDiagnostics(t *testing.T) {
	workspaceDir, cacheDir := setupStrictWorkspace(t)
	write, read, waitDone := newStrictSession(t, workspaceDir, cacheDir)

	validText, err := os.ReadFile("testdata/strict/vmcluster-valid.yaml")
	if err != nil {
		t.Fatalf("read valid fixture: %v", err)
	}

	// Initialize with default options (strict=true by default).
	write(makeStrictInitReq(workspaceDir, nil))
	_ = read() // initialize response

	write(map[string]any{"jsonrpc": "2.0", "method": "initialized", "params": map[string]any{}})

	docURI := "file://" + filepath.Join(workspaceDir, "vmcluster.yaml")
	write(map[string]any{
		"jsonrpc": "2.0",
		"method":  "textDocument/didOpen",
		"params": map[string]any{
			"textDocument": map[string]any{
				"uri":        docURI,
				"languageId": "yaml",
				"version":    1,
				"text":       string(validText),
			},
		},
	})

	diags := readUntilDiagnostics(t, read, 5*time.Second)
	if diags == nil {
		t.Fatal("timed out waiting for publishDiagnostics")
	}

	if len(diags) != 0 {
		t.Errorf("expected 0 diagnostics for valid doc, got %d: %v", len(diags), diags)
	}

	write(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "shutdown"})
	_ = read() // shutdown response
	write(map[string]any{"jsonrpc": "2.0", "method": "exit"})
	if err := waitDone(5 * time.Second); err != nil {
		t.Errorf("server exit error: %v", err)
	}
}

// TestStrictMode_AnnotationsAlwaysAllowed_DefaultMode verifies that
// metadata.annotations with arbitrary keys produces no diagnostics.
// The annotations schema uses additionalProperties:{type:string}, which
// WrapStrict must leave unchanged (it already has an additionalProperties value).
func TestStrictMode_AnnotationsAlwaysAllowed_DefaultMode(t *testing.T) {
	workspaceDir, cacheDir := setupStrictWorkspace(t)
	write, read, waitDone := newStrictSession(t, workspaceDir, cacheDir)

	annotText, err := os.ReadFile("testdata/strict/vmcluster-annotations.yaml")
	if err != nil {
		t.Fatalf("read annotations fixture: %v", err)
	}

	// Initialize with default options (strict=true by default).
	write(makeStrictInitReq(workspaceDir, nil))
	_ = read() // initialize response

	write(map[string]any{"jsonrpc": "2.0", "method": "initialized", "params": map[string]any{}})

	docURI := "file://" + filepath.Join(workspaceDir, "vmcluster.yaml")
	write(map[string]any{
		"jsonrpc": "2.0",
		"method":  "textDocument/didOpen",
		"params": map[string]any{
			"textDocument": map[string]any{
				"uri":        docURI,
				"languageId": "yaml",
				"version":    1,
				"text":       string(annotText),
			},
		},
	})

	diags := readUntilDiagnostics(t, read, 5*time.Second)
	if diags == nil {
		t.Fatal("timed out waiting for publishDiagnostics")
	}

	if len(diags) != 0 {
		t.Errorf("expected 0 diagnostics for annotations doc, got %d: %v", len(diags), diags)
	}

	write(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "shutdown"})
	_ = read() // shutdown response
	write(map[string]any{"jsonrpc": "2.0", "method": "exit"})
	if err := waitDone(5 * time.Second); err != nil {
		t.Errorf("server exit error: %v", err)
	}
}

// TestStrictMode_MultipleTyposAtDifferentDepths_OneErrorEach verifies that
// two unknown fields at different nesting depths each produce their own
// diagnostic anchored on the typo field line, not on the shared parent.
//
// Fixture: testdata/strict/vmcluster-multi-typo.yaml contains:
//   - "retnionPeriod" at spec level  (LSP line 5, char 2)
//   - "requestsLBEnabled" inside spec.clusterConfig (LSP line 7, char 4)
func TestStrictMode_MultipleTyposAtDifferentDepths_OneErrorEach(t *testing.T) {
	workspaceDir, cacheDir := setupStrictWorkspace(t)
	write, read, waitDone := newStrictSession(t, workspaceDir, cacheDir)

	multiTypoText, err := os.ReadFile("testdata/strict/vmcluster-multi-typo.yaml")
	if err != nil {
		t.Fatalf("read multi-typo fixture: %v", err)
	}

	write(makeStrictInitReq(workspaceDir, nil))
	_ = read() // initialize response

	write(map[string]any{"jsonrpc": "2.0", "method": "initialized", "params": map[string]any{}})

	docURI := "file://" + filepath.Join(workspaceDir, "vmcluster.yaml")
	write(map[string]any{
		"jsonrpc": "2.0",
		"method":  "textDocument/didOpen",
		"params": map[string]any{
			"textDocument": map[string]any{
				"uri":        docURI,
				"languageId": "yaml",
				"version":    1,
				"text":       string(multiTypoText),
			},
		},
	})

	diags := readUntilDiagnostics(t, read, 5*time.Second)
	if diags == nil {
		t.Fatal("timed out waiting for publishDiagnostics")
	}

	if len(diags) != 2 {
		t.Fatalf("expected 2 diagnostics, got %d: %v", len(diags), diags)
	}

	// Collect (line, message) pairs — diagnostics are sorted by line.
	type diagSummary struct {
		line float64
		char float64
		msg  string
	}
	summaries := make([]diagSummary, 0, len(diags))
	for _, raw := range diags {
		d, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("diagnostic is not a map: %T", raw)
		}
		if d["severity"] != float64(1) {
			t.Errorf("severity = %v, want 1 (Error)", d["severity"])
		}
		rng, _ := d["range"].(map[string]any)
		start, _ := rng["start"].(map[string]any)
		msg, _ := d["message"].(string)
		summaries = append(summaries, diagSummary{
			line: start["line"].(float64),
			char: start["character"].(float64),
			msg:  msg,
		})
	}

	// Diagnostic 0: retnionPeriod at spec level — LSP line 5, char 2.
	if summaries[0].line != float64(5) {
		t.Errorf("diag[0] line = %v, want 5 (0-based line of 'retnionPeriod:')", summaries[0].line)
	}
	if summaries[0].char != float64(2) {
		t.Errorf("diag[0] char = %v, want 2", summaries[0].char)
	}
	if !strings.HasPrefix(summaries[0].msg, "unknown field 'retnionPeriod'") {
		t.Errorf("diag[0] message = %q, want prefix %q", summaries[0].msg, "unknown field 'retnionPeriod'")
	}
	if !strings.Contains(summaries[0].msg, "schemalock.strict=false") {
		t.Errorf("diag[0] missing opt-out hint: %q", summaries[0].msg)
	}

	// Diagnostic 1: requestsLBEnabled inside clusterConfig — LSP line 7, char 4.
	if summaries[1].line != float64(7) {
		t.Errorf("diag[1] line = %v, want 7 (0-based line of 'requestsLBEnabled:')", summaries[1].line)
	}
	if summaries[1].char != float64(4) {
		t.Errorf("diag[1] char = %v, want 4", summaries[1].char)
	}
	if !strings.HasPrefix(summaries[1].msg, "unknown field 'requestsLBEnabled'") {
		t.Errorf("diag[1] message = %q, want prefix %q", summaries[1].msg, "unknown field 'requestsLBEnabled'")
	}
	if !strings.Contains(summaries[1].msg, "schemalock.strict=false") {
		t.Errorf("diag[1] missing opt-out hint: %q", summaries[1].msg)
	}

	write(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "shutdown"})
	_ = read() // shutdown response
	write(map[string]any{"jsonrpc": "2.0", "method": "exit"})
	if err := waitDone(5 * time.Second); err != nil {
		t.Errorf("server exit error: %v", err)
	}
}

// TestStrictMode_PreserveUnknownFieldsRegion_ZeroDiagnostics verifies that
// unknown fields inside a sub-schema region with additionalProperties:true
// produce no diagnostics. This covers the x-kubernetes-preserve-unknown-fields
// expansion pattern.
func TestStrictMode_PreserveUnknownFieldsRegion_ZeroDiagnostics(t *testing.T) {
	workspaceDir, cacheDir := setupStrictWorkspace(t)
	write, read, waitDone := newStrictSession(t, workspaceDir, cacheDir)

	preservedText, err := os.ReadFile("testdata/strict/vmcluster-preserved.yaml")
	if err != nil {
		t.Fatalf("read preserved fixture: %v", err)
	}

	// Initialize with default options (strict=true by default).
	write(makeStrictInitReq(workspaceDir, nil))
	_ = read() // initialize response

	write(map[string]any{"jsonrpc": "2.0", "method": "initialized", "params": map[string]any{}})

	docURI := "file://" + filepath.Join(workspaceDir, "vmcluster.yaml")
	write(map[string]any{
		"jsonrpc": "2.0",
		"method":  "textDocument/didOpen",
		"params": map[string]any{
			"textDocument": map[string]any{
				"uri":        docURI,
				"languageId": "yaml",
				"version":    1,
				"text":       string(preservedText),
			},
		},
	})

	diags := readUntilDiagnostics(t, read, 5*time.Second)
	if diags == nil {
		t.Fatal("timed out waiting for publishDiagnostics")
	}

	if len(diags) != 0 {
		t.Errorf("expected 0 diagnostics for preserved-unknown-fields region, got %d: %v", len(diags), diags)
	}

	write(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "shutdown"})
	_ = read() // shutdown response
	write(map[string]any{"jsonrpc": "2.0", "method": "exit"})
	if err := waitDone(5 * time.Second); err != nil {
		t.Errorf("server exit error: %v", err)
	}
}

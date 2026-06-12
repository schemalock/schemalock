package lsp_test

// wire_smoke_test.go — absolute-assertion test for the new jsonrpc2-based Run path.
//
// Drives: initialize → initialized → shutdown → exit through the new Run only.
// Asserts:
//   - id field round-trips as the exact JSON type sent (int stays int).
//   - result: null present on shutdown response (not omitted).
//   - serverInfo.name == "schemalock", version == "0.1.0".
//   - Capabilities advertise textDocumentSync: 1 (integer form).
//   - completionProvider.triggerCharacters == [":"].
//   - hoverProvider == true.
//   - After mode rip-out: completionProvider and hoverProvider are always
//     advertised, regardless of initializationOptions content.
//   - Regression: { mode: "contributor" } in initializationOptions is silently
//     ignored (no error, full capabilities still advertised).

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"testing"
	"time"

	"github.com/schemalock/schemalock/internal/cache"
	"github.com/schemalock/schemalock/internal/lsp"
	"github.com/schemalock/schemalock/internal/lsp/lsptest"
	"github.com/schemalock/schemalock/internal/registry"
)

// stdioRWC wraps an io.Reader and io.Writer into an io.ReadWriteCloser.
// Close closes the write end of the output pipe to signal EOF to readers, and
// also closes the reader if it implements io.Closer. This allows the jsonrpc2
// stream to detect connection close when Exit calls conn.Close().
type stdioRWC struct {
	r io.ReadCloser
	w io.WriteCloser
}

func (s stdioRWC) Read(p []byte) (int, error)  { return s.r.Read(p) }
func (s stdioRWC) Write(p []byte) (int, error) { return s.w.Write(p) }
func (s stdioRWC) Close() error {
	// Close both sides to unblock any pending reads/writes.
	rerr := s.r.Close()
	werr := s.w.Close()
	if rerr != nil {
		return rerr
	}
	return werr
}

// newSmokeSession starts the new Run path and returns helpers for reading/writing frames.
func newSmokeSession(t *testing.T, workspaceDir string) (
	writeFn func(msg any),
	readFn func() map[string]any,
	waitDone func(timeout time.Duration) error,
) {
	t.Helper()

	cacheRoot := t.TempDir()
	c := cache.New(cacheRoot)
	// Point at a non-existent server — initialize will log "no lockfile found"
	// but still return a valid InitializeResult with capabilities.
	reg := registry.NewClient("http://127.0.0.1:0")
	logger := log.New(io.Discard, "", 0)

	srv := lsp.NewServer(lsp.Config{
		Cache:    c,
		Registry: reg,
		Logger:   logger,
	})

	// Wire pipes: test → server (in), server → test (out).
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	errCh := make(chan error, 1)
	go func() {
		// Pass both sides of the pipes as the RWC. Close() will close both
		// so the stream's Read loop can detect the connection is done.
		errCh <- srv.Run(ctx, stdioRWC{r: inR, w: outW})
		// Ensure outR sees EOF after the server is done writing.
		_ = outW.Close()
	}()

	br := bufio.NewReader(outR)

	writeFn = func(msg any) {
		t.Helper()
		b, err := json.Marshal(msg)
		if err != nil {
			t.Fatalf("smoke: marshal: %v", err)
		}
		if err := lsptest.WriteMessage(inW, b); err != nil {
			// Ignore errors from writing to a closed pipe (happens after exit).
			return
		}
	}

	readFn = func() map[string]any {
		t.Helper()
		body, err := lsptest.ReadMessage(br)
		if err != nil {
			t.Fatalf("smoke: ReadMessage: %v", err)
		}
		var m map[string]any
		if err := json.Unmarshal(body, &m); err != nil {
			t.Fatalf("smoke: unmarshal frame: %v", err)
		}
		return m
	}

	waitDone = func(timeout time.Duration) error {
		select {
		case err := <-errCh:
			return err
		case <-time.After(timeout):
			t.Error("smoke: server did not exit within timeout")
			return nil
		}
	}

	_ = workspaceDir
	return writeFn, readFn, waitDone
}

func TestWireSmoke_NewRunPath(t *testing.T) {
	workspaceDir := t.TempDir()
	write, read, waitDone := newSmokeSession(t, workspaceDir)

	// --- initialize ---
	initReq := map[string]any{
		"jsonrpc": "2.0",
		"id":      1, // integer ID
		"method":  "initialize",
		"params": map[string]any{
			"rootUri": "file://" + workspaceDir,
		},
	}
	write(initReq)
	initResp := read()

	// id round-trips as JSON number (float64 in Go's map[string]any)
	if got := initResp["id"]; got != float64(1) {
		t.Errorf("initialize: id = %v (%T), want float64(1)", got, got)
	}
	if _, hasErr := initResp["error"]; hasErr {
		t.Fatalf("initialize: got error response: %v", initResp["error"])
	}
	result, ok := initResp["result"].(map[string]any)
	if !ok {
		t.Fatalf("initialize: result is not an object: %T", initResp["result"])
	}

	// serverInfo assertions
	serverInfo, ok := result["serverInfo"].(map[string]any)
	if !ok {
		t.Fatalf("initialize: serverInfo missing or wrong type: %v", result["serverInfo"])
	}
	if got := serverInfo["name"]; got != "schemalock" {
		t.Errorf("serverInfo.name = %q, want %q", got, "schemalock")
	}
	if got := serverInfo["version"]; got != "0.1.0" {
		t.Errorf("serverInfo.version = %q, want %q", got, "0.1.0")
	}

	// capabilities assertions
	caps, ok := result["capabilities"].(map[string]any)
	if !ok {
		t.Fatalf("initialize: capabilities missing or wrong type: %v", result["capabilities"])
	}

	// textDocumentSync must be integer 1 (JSON number, so float64 in Go)
	if got := caps["textDocumentSync"]; got != float64(1) {
		t.Errorf("capabilities.textDocumentSync = %v (%T), want float64(1)", got, got)
	}

	// completionProvider must have triggerCharacters: [":"]
	cp, ok := caps["completionProvider"].(map[string]any)
	if !ok {
		t.Fatalf("capabilities.completionProvider missing or wrong type: %v", caps["completionProvider"])
	}
	trigChars, ok := cp["triggerCharacters"].([]any)
	if !ok || len(trigChars) != 1 || trigChars[0] != ":" {
		t.Errorf("completionProvider.triggerCharacters = %v, want [\":\"]", cp["triggerCharacters"])
	}

	// hoverProvider must be true
	if got := caps["hoverProvider"]; got != true {
		t.Errorf("capabilities.hoverProvider = %v, want true", got)
	}

	// --- initialized notification (no response expected) ---
	write(map[string]any{
		"jsonrpc": "2.0",
		"method":  "initialized",
		"params":  map[string]any{},
	})

	// --- shutdown ---
	write(map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "shutdown",
	})
	shutdownResp := read()

	// id round-trips
	if got := shutdownResp["id"]; got != float64(2) {
		t.Errorf("shutdown: id = %v (%T), want float64(2)", got, got)
	}

	// result: null must be present (not omitted) per JSON-RPC 2.0 spec
	if _, hasResult := shutdownResp["result"]; !hasResult {
		t.Errorf("shutdown: 'result' field absent, want explicit null")
	}
	if got := shutdownResp["result"]; got != nil {
		t.Errorf("shutdown: result = %v, want null", got)
	}
	if _, hasErr := shutdownResp["error"]; hasErr {
		t.Errorf("shutdown: unexpected error: %v", shutdownResp["error"])
	}

	// --- exit notification (closes connection) ---
	write(map[string]any{
		"jsonrpc": "2.0",
		"method":  "exit",
	})

	// Server should stop cleanly.
	if err := waitDone(5 * time.Second); err != nil {
		t.Errorf("smoke: Run returned error: %v", err)
	}
}

// TestWireSmoke_CapabilitiesAfterModeRipOut verifies that all five providers
// (hover, completion, definition, references, documentSymbol) are advertised
// unconditionally after the mode system has been removed. This replaces the
// old contributor-mode capability test.
func TestWireSmoke_CapabilitiesAfterModeRipOut(t *testing.T) {
	workspaceDir := t.TempDir()
	write, read, waitDone := newSmokeSession(t, workspaceDir)

	initReq := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"rootUri": "file://" + workspaceDir,
		},
	}
	write(initReq)
	initResp := read()

	result, ok := initResp["result"].(map[string]any)
	if !ok {
		t.Fatalf("capabilities: result is not an object: %T", initResp["result"])
	}
	caps, ok := result["capabilities"].(map[string]any)
	if !ok {
		t.Fatalf("capabilities: capabilities missing or wrong type")
	}

	// All five providers must be advertised unconditionally (no mode / yaml-ls-presence check).

	// completionProvider must be present.
	if _, has := caps["completionProvider"]; !has {
		t.Errorf("capabilities: completionProvider must be advertised; got caps=%v", caps)
	}
	// hoverProvider must be true.
	if got := caps["hoverProvider"]; got != true {
		t.Errorf("capabilities: hoverProvider = %v, want true", got)
	}
	// definitionProvider must be true.
	if got := caps["definitionProvider"]; got != true {
		t.Errorf("capabilities: definitionProvider = %v, want true", got)
	}
	// referencesProvider must be true.
	if got := caps["referencesProvider"]; got != true {
		t.Errorf("capabilities: referencesProvider = %v, want true", got)
	}
	// documentSymbolProvider must be true.
	if got := caps["documentSymbolProvider"]; got != true {
		t.Errorf("capabilities: documentSymbolProvider = %v, want true", got)
	}

	write(map[string]any{"jsonrpc": "2.0", "method": "initialized", "params": map[string]any{}})
	write(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "shutdown"})
	_ = read() // shutdown response
	write(map[string]any{"jsonrpc": "2.0", "method": "exit"})

	if err := waitDone(5 * time.Second); err != nil {
		t.Errorf("capabilities: Run returned error: %v", err)
	}
}

// TestWireSmoke_ContributorModeIsIgnored is a regression test that verifies
// the server silently accepts { mode: "contributor" } in initializationOptions
// without returning an error, and still advertises full capabilities. This
// covers the Chunk 3/5 transition period where the extension still sends the
// old payload.
func TestWireSmoke_ContributorModeIsIgnored(t *testing.T) {
	workspaceDir := t.TempDir()
	write, read, waitDone := newSmokeSession(t, workspaceDir)

	initReq := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"rootUri": "file://" + workspaceDir,
			// Extension still sends old-style options; server must ignore silently.
			"initializationOptions": map[string]any{
				"mode": "contributor",
			},
		},
	}
	write(initReq)
	initResp := read()

	if _, hasErr := initResp["error"]; hasErr {
		t.Fatalf("initialize must not error on old mode payload; got %v", initResp["error"])
	}
	result, ok := initResp["result"].(map[string]any)
	if !ok {
		t.Fatalf("ignored-mode: result is not an object: %T", initResp["result"])
	}
	caps, ok := result["capabilities"].(map[string]any)
	if !ok {
		t.Fatalf("ignored-mode: capabilities missing or wrong type")
	}
	// Full capabilities must still be advertised (mode is ignored, not obeyed).
	if _, has := caps["completionProvider"]; !has {
		t.Errorf("ignored-mode: completionProvider must be advertised even with old mode payload; caps=%v", caps)
	}
	if got := caps["hoverProvider"]; got != true {
		t.Errorf("ignored-mode: hoverProvider = %v, want true", got)
	}

	write(map[string]any{"jsonrpc": "2.0", "method": "initialized", "params": map[string]any{}})
	write(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "shutdown"})
	_ = read() // shutdown response
	write(map[string]any{"jsonrpc": "2.0", "method": "exit"})

	if err := waitDone(5 * time.Second); err != nil {
		t.Errorf("ignored-mode: Run returned error: %v", err)
	}
}

// TestWireSmoke_AllOutputIsFramed asserts that every byte written to the
// output pipe is valid Content-Length-framed LSP data (no rogue stderr to stdout).
func TestWireSmoke_AllOutputIsFramed(t *testing.T) {
	workspaceDir := t.TempDir()

	cacheRoot := t.TempDir()
	c := cache.New(cacheRoot)
	reg := registry.NewClient("http://127.0.0.1:0")
	logger := log.New(io.Discard, "", 0)
	srv := lsp.NewServer(lsp.Config{Cache: c, Registry: reg, Logger: logger})

	inR, inW := io.Pipe()
	var outBuf bytes.Buffer
	// Tee: capture all server-written bytes into outBuf while also forwarding
	// to outW so the pipe read side can unblock correctly.
	outR, outW := io.Pipe()
	tee := io.MultiWriter(outW, &outBuf)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	errCh := make(chan error, 1)
	go func() {
		// wrapW is not closeable separately from outW; use a non-closing teeWriter.
		errCh <- srv.Run(ctx, stdioRWC{r: inR, w: nopWriteCloser{tee}})
		_ = outW.Close()
	}()

	write := func(msg any) {
		b, _ := json.Marshal(msg)
		_ = lsptest.WriteMessage(inW, b)
	}

	br := bufio.NewReader(outR)
	readOne := func() {
		_, _ = lsptest.ReadMessage(br)
	}

	write(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{"rootUri": "file://" + workspaceDir}})
	readOne()
	write(map[string]any{"jsonrpc": "2.0", "method": "initialized", "params": map[string]any{}})
	write(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "shutdown"})
	readOne()
	write(map[string]any{"jsonrpc": "2.0", "method": "exit"})

	select {
	case <-errCh:
	case <-time.After(5 * time.Second):
		t.Fatal("server did not exit within timeout")
	}

	// Validate that every byte in outBuf is a properly framed message.
	data := outBuf.Bytes()
	br2 := bufio.NewReader(bytes.NewReader(data))
	for {
		_, err := lsptest.ReadMessage(br2)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Errorf("output contains non-framed data: %v", err)
			break
		}
	}
}

// TestWireSmoke_StrictFalseWithUnknownFields verifies that when
// initializationOptions contains { "strict": false, "mode": "contributor" },
// the server still returns full capabilities (graceful unknown-field handling
// regression) and does not return an error.
func TestWireSmoke_StrictFalseWithUnknownFields(t *testing.T) {
	workspaceDir := t.TempDir()
	write, read, waitDone := newSmokeSession(t, workspaceDir)

	initReq := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"rootUri": "file://" + workspaceDir,
			"initializationOptions": map[string]any{
				"strict": false,
				"mode":   "contributor", // unknown field — must be silently ignored
			},
		},
	}
	write(initReq)
	initResp := read()

	if _, hasErr := initResp["error"]; hasErr {
		t.Fatalf("initialize must not error with strict:false + unknown fields; got %v", initResp["error"])
	}
	result, ok := initResp["result"].(map[string]any)
	if !ok {
		t.Fatalf("strict-false: result is not an object: %T", initResp["result"])
	}
	caps, ok := result["capabilities"].(map[string]any)
	if !ok {
		t.Fatalf("strict-false: capabilities missing or wrong type")
	}
	// Full capabilities must still be advertised.
	if _, has := caps["completionProvider"]; !has {
		t.Errorf("strict-false: completionProvider must be advertised; got caps=%v", caps)
	}
	if got := caps["hoverProvider"]; got != true {
		t.Errorf("strict-false: hoverProvider = %v, want true", got)
	}

	write(map[string]any{"jsonrpc": "2.0", "method": "initialized", "params": map[string]any{}})
	write(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "shutdown"})
	_ = read() // shutdown response
	write(map[string]any{"jsonrpc": "2.0", "method": "exit"})

	if err := waitDone(5 * time.Second); err != nil {
		t.Errorf("strict-false: Run returned error: %v", err)
	}
}

// nopWriteCloser wraps an io.Writer so it satisfies io.WriteCloser with a no-op Close.
type nopWriteCloser struct{ w io.Writer }

func (n nopWriteCloser) Write(p []byte) (int, error) { return n.w.Write(p) }
func (n nopWriteCloser) Close() error                { return nil }

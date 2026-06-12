package lsp_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	lspprot "go.lsp.dev/protocol"

	"github.com/schemalock/schemalock/internal/cache"
	"github.com/schemalock/schemalock/internal/lsp"
	"github.com/schemalock/schemalock/internal/lsp/lsptest"
	"github.com/schemalock/schemalock/internal/registry"
)

// --------------------------------------------------------------------------
// Helpers
// --------------------------------------------------------------------------

// schemaBytes is the content of the tiny VMCluster schema, loaded once.
var schemaBytes []byte

// nestedSchemaBytes is the content of the tiny TinyNested schema, loaded once.
var nestedSchemaBytes []byte

func init() {
	b, err := os.ReadFile("testdata/schemas/tiny_vmcluster.json")
	if err != nil {
		panic("cannot load tiny_vmcluster.json: " + err.Error())
	}
	schemaBytes = b

	nb, err := os.ReadFile("testdata/schemas/tiny_nested.json")
	if err != nil {
		panic("cannot load tiny_nested.json: " + err.Error())
	}
	nestedSchemaBytes = nb
}

// newTestRegistry starts an httptest.Server that serves a tiny manifest and
// the VMCluster schema. Callers must call Close on the returned server.
func newTestRegistry(t *testing.T) *httptest.Server {
	t.Helper()

	// Pre-compute integrity for the schema.
	reg := registry.NewClient("http://placeholder")
	_ = reg
	schemaIntegrity := registry.ComputeIntegrity(schemaBytes)

	manifest := fmt.Sprintf(`{
		"version": 1,
		"kinds": {
			"VMCluster": {
				"integrity": %q,
				"size": %d
			}
		}
	}`, schemaIntegrity, len(schemaBytes))

	mux := http.NewServeMux()
	mux.HandleFunc("/kubernetes/operator.victoriametrics.com/versions.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`["0.70.0"]`))
	})
	mux.HandleFunc("/kubernetes/operator.victoriametrics.com/0.70.0/manifest.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(manifest))
	})
	mux.HandleFunc("/kubernetes/operator.victoriametrics.com/0.70.0/VMCluster.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(schemaBytes)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// newTestRegistryNested starts an httptest.Server that serves the TinyNested
// schema in addition to the VMCluster schema. Used by Task 5/6 integration tests.
func newTestRegistryNested(t *testing.T) *httptest.Server {
	t.Helper()

	schemaIntegrity := registry.ComputeIntegrity(schemaBytes)
	nestedIntegrity := registry.ComputeIntegrity(nestedSchemaBytes)

	vmManifest := fmt.Sprintf(`{
		"version": 1,
		"kinds": {
			"VMCluster": {
				"integrity": %q,
				"size": %d
			}
		}
	}`, schemaIntegrity, len(schemaBytes))

	nestedManifest := fmt.Sprintf(`{
		"version": 1,
		"kinds": {
			"TinyNested": {
				"integrity": %q,
				"size": %d
			}
		}
	}`, nestedIntegrity, len(nestedSchemaBytes))

	mux := http.NewServeMux()
	// VMCluster routes (unchanged).
	mux.HandleFunc("/kubernetes/operator.victoriametrics.com/versions.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`["0.70.0"]`))
	})
	mux.HandleFunc("/kubernetes/operator.victoriametrics.com/0.70.0/manifest.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(vmManifest))
	})
	mux.HandleFunc("/kubernetes/operator.victoriametrics.com/0.70.0/VMCluster.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(schemaBytes)
	})
	// TinyNested routes.
	mux.HandleFunc("/kubernetes/certmanager.io/versions.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`["1.0.0"]`))
	})
	mux.HandleFunc("/kubernetes/certmanager.io/1.0.0/manifest.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(nestedManifest))
	})
	mux.HandleFunc("/kubernetes/certmanager.io/1.0.0/TinyNested.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(nestedSchemaBytes)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// newSessionNested builds a server session backed by the nested-schema registry.
func newSessionNested(t *testing.T) *session {
	t.Helper()
	srv := newTestRegistryNested(t)

	cacheRoot := t.TempDir()
	c := cache.New(cacheRoot)
	regClient := registry.NewClient(srv.URL)
	logger := log.New(io.Discard, "", 0)

	server := lsp.NewServer(lsp.Config{
		Cache:    c,
		Registry: regClient,
		Logger:   logger,
	})

	pr, pw := io.Pipe()
	or, ow := io.Pipe()

	errCh := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	vw := &validatingWriter{t: t, inner: ow}

	go func() {
		rwc := &testRWC{r: pr, w: vw, onClose: func() {
			pr.CloseWithError(io.EOF)
			ow.Close()
		}}
		errCh <- server.Run(ctx, rwc)
		ow.Close()
	}()

	s := &session{
		server: server,
		pr:     pr,
		pw:     pw,
		or:     or,
		ow:     ow,
		br:     bufio.NewReader(or),
		errCh:  errCh,
		vw:     vw,
	}
	t.Cleanup(func() { vw.Validate() })
	return s
}

// session drives a Server through an in-memory pipe.
// It returns a collect function that reads all framed output until the pipe
// closes, and a write function that sends framed messages.
type session struct {
	server    *lsp.Server
	pr        *io.PipeReader // server reads from here
	pw        *io.PipeWriter // test writes to here
	or        *io.PipeReader // test reads from here
	ow        *io.PipeWriter // server writes to here
	br        *bufio.Reader  // single persistent reader for s.or
	errCh     chan error
	outFrames [][]byte
	vw        *validatingWriter
}

// newSession builds a Server and wires it to a pair of io.Pipe for I/O.
// srv.Run is started in a goroutine. The caller must call s.Close() to shut
// down.
func newSession(t *testing.T, lockDir string) *session {
	t.Helper()
	srv := newTestRegistry(t)

	cacheRoot := t.TempDir()
	c := cache.New(cacheRoot)
	regClient := registry.NewClient(srv.URL)
	logger := log.New(io.Discard, "", 0)

	server := lsp.NewServer(lsp.Config{
		Cache:    c,
		Registry: regClient,
		Logger:   logger,
	})

	// Pipe for stdin (test → server).
	pr, pw := io.Pipe()
	// Pipe for stdout (server → test).
	or, ow := io.Pipe()

	errCh := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	// validatingWriter wraps ow and fails the test if any non-framed bytes are
	// written (only Content-Length-framed messages are allowed on stdout).
	// We check that every write starts with "Content-Length: ".
	vw := &validatingWriter{t: t, inner: ow}

	go func() {
		// testRWC wraps the pipe pair into an io.ReadWriteCloser for Run.
		// Close must close both the read pipe (pr) and write pipe (ow) so the
		// blocked stream.Read in the jsonrpc2 reader loop unblocks and Run returns.
		rwc := &testRWC{r: pr, w: vw, onClose: func() {
			pr.CloseWithError(io.EOF)
			ow.Close()
		}}
		errCh <- server.Run(ctx, rwc)
		ow.Close()
	}()

	_ = lockDir // used in makeInitializeReq

	s := &session{
		server: server,
		pr:     pr,
		pw:     pw,
		or:     or,
		ow:     ow,
		br:     bufio.NewReader(or),
		errCh:  errCh,
		vw:     vw,
	}

	// Validate that all stdout bytes are properly framed at test cleanup.
	t.Cleanup(func() {
		vw.Validate()
	})

	return s
}

// send writes one LSP request as a Content-Length-framed message.
func (s *session) send(t *testing.T, msg any) {
	t.Helper()
	b, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := lsptest.WriteMessage(s.pw, b); err != nil {
		t.Fatalf("WriteMessage: %v", err)
	}
}

// readFrame reads the next Content-Length-framed message from the server.
// Not safe for concurrent use within the same session.
func (s *session) readFrame(t *testing.T) map[string]any {
	t.Helper()
	body, err := lsptest.ReadMessage(s.br)
	if err != nil {
		t.Fatalf("readFrame: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("unmarshal frame: %v", err)
	}
	return m
}

// readFrameTimeout reads the next frame within the given timeout.
// Not safe for concurrent use within the same session.
func (s *session) readFrameTimeout(t *testing.T, timeout time.Duration) (map[string]any, bool) {
	t.Helper()
	// Set a deadline on the underlying pipe reader so ReadMessage can return.
	// io.PipeReader does not support SetDeadline, so we use a goroutine with
	// a channel and rely on the test to not call this concurrently.
	ch := make(chan map[string]any, 1)
	go func() {
		body, err := lsptest.ReadMessage(s.br)
		if err != nil {
			return
		}
		var m map[string]any
		_ = json.Unmarshal(body, &m)
		ch <- m
	}()
	select {
	case m := <-ch:
		return m, true
	case <-time.After(timeout):
		return nil, false
	}
}

// close shuts down the session by closing the write pipe and waiting for the
// server goroutine.
func (s *session) close(t *testing.T) {
	t.Helper()
	s.pw.Close()
}

// waitDone blocks until the server goroutine exits or the timeout expires.
func (s *session) waitDone(t *testing.T, timeout time.Duration) error {
	t.Helper()
	select {
	case err := <-s.errCh:
		return err
	case <-time.After(timeout):
		t.Error("server did not exit within timeout")
		return nil
	}
}

// makeInitializeReq builds an initialize request with the given workspace dir.
func makeInitializeReq(id int64, workspaceDir string) map[string]any {
	return map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  "initialize",
		"params": map[string]any{
			"rootUri": "file://" + workspaceDir,
		},
	}
}

// makeInitializedNotif builds an initialized notification.
func makeInitializedNotif() map[string]any {
	return map[string]any{
		"jsonrpc": "2.0",
		"method":  "initialized",
		"params":  map[string]any{},
	}
}

// makeDidOpenReq builds a textDocument/didOpen notification.
func makeDidOpenReq(uri, text string, version int) map[string]any {
	return map[string]any{
		"jsonrpc": "2.0",
		"method":  "textDocument/didOpen",
		"params": map[string]any{
			"textDocument": map[string]any{
				"uri":        uri,
				"languageId": "yaml",
				"version":    version,
				"text":       text,
			},
		},
	}
}

// makeShutdownReq builds a shutdown request.
func makeShutdownReq(id int64) map[string]any {
	return map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  "shutdown",
	}
}

// makeExitNotif builds an exit notification.
func makeExitNotif() map[string]any {
	return map[string]any{
		"jsonrpc": "2.0",
		"method":  "exit",
	}
}

// testRWC wraps an io.Reader and io.Writer into an io.ReadWriteCloser for use
// in test sessions that call server.Run. Close invokes the onClose callback
// (typically closing the write pipe so the jsonrpc2 stream sees EOF).
type testRWC struct {
	r       io.Reader
	w       io.Writer
	onClose func()
}

func (t *testRWC) Read(p []byte) (int, error)  { return t.r.Read(p) }
func (t *testRWC) Write(p []byte) (int, error) { return t.w.Write(p) }
func (t *testRWC) Close() error {
	if t.onClose != nil {
		t.onClose()
	}
	return nil
}

// validatingWriter wraps an io.Writer and fails the test if any bytes that
// don't belong to a properly framed LSP message are written to stdout.
//
// The LSP framing protocol means every complete frame starts with a
// "Content-Length: N\r\n\r\n" header written by lsptest.WriteMessage.
// We accumulate all bytes into a buffer and validate the complete output at
// the end of each test (via Validate). This catches any rogue writes (e.g.
// accidental log output to stdout) that aren't part of the framing protocol.
type validatingWriter struct {
	t     *testing.T
	inner io.Writer
	mu    bytes.Buffer // accumulates all written bytes
}

func (vw *validatingWriter) Write(p []byte) (int, error) {
	vw.mu.Write(p)
	return vw.inner.Write(p)
}

// Validate checks that all bytes written so far are valid Content-Length
// framed messages. Any non-frame content causes the test to fail.
func (vw *validatingWriter) Validate() {
	data := vw.mu.Bytes()
	pos := 0
	for pos < len(data) {
		// Every frame must start with "Content-Length: ".
		if len(data)-pos < 16 || string(data[pos:pos+16]) != "Content-Length: " {
			vw.t.Errorf("non-framed data written to stdout at offset %d: %q", pos, data[pos:min(pos+64, len(data))])
			return
		}
		// Find the \r\n\r\n separator.
		headerEnd := bytes.Index(data[pos:], []byte("\r\n\r\n"))
		if headerEnd < 0 {
			// Incomplete frame — could happen if the test ends mid-write;
			// not a violation.
			return
		}
		headerEnd += pos
		// Parse Content-Length.
		header := string(data[pos : headerEnd+2]) // up to first \r\n
		var contentLen int
		if _, err := fmt.Sscanf(header, "Content-Length: %d", &contentLen); err != nil {
			vw.t.Errorf("cannot parse Content-Length in frame header %q", header)
			return
		}
		bodyStart := headerEnd + 4 // after \r\n\r\n
		bodyEnd := bodyStart + contentLen
		if bodyEnd > len(data) {
			// Truncated — end of stream; not a violation.
			return
		}
		pos = bodyEnd
	}
}

// --------------------------------------------------------------------------
// Tests
// --------------------------------------------------------------------------

// TestInitialize verifies the initialize handshake returns the expected
// capabilities.
func TestInitialize(t *testing.T) {
	workspaceDir, err := filepath.Abs("testdata/workspace")
	if err != nil {
		t.Fatal(err)
	}

	s := newSession(t, workspaceDir)
	defer s.close(t)

	s.send(t, makeInitializeReq(1, workspaceDir))
	resp := s.readFrame(t)

	if resp["id"] == nil {
		t.Fatal("response missing id")
	}
	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("result not a map: %v", resp["result"])
	}
	caps, ok := result["capabilities"].(map[string]any)
	if !ok {
		t.Fatalf("capabilities not a map: %v", result["capabilities"])
	}
	if caps["textDocumentSync"] != float64(1) {
		t.Errorf("textDocumentSync = %v, want 1", caps["textDocumentSync"])
	}
	serverInfo, ok := result["serverInfo"].(map[string]any)
	if !ok {
		t.Fatalf("serverInfo not a map: %v", result["serverInfo"])
	}
	if serverInfo["name"] != "schemalock" {
		t.Errorf("serverInfo.name = %v, want schemalock", serverInfo["name"])
	}

	// Send initialized notification (no response expected).
	s.send(t, makeInitializedNotif())

	// Shutdown sequence.
	s.send(t, makeShutdownReq(2))
	shutResp := s.readFrame(t)
	if shutResp["id"] == nil {
		t.Fatal("shutdown response missing id")
	}
	s.send(t, makeExitNotif())
	if err := s.waitDone(t, 3*time.Second); err != nil {
		t.Errorf("server exit error: %v", err)
	}
}

// TestDidOpen_GoodDoc verifies that opening a valid VMCluster YAML produces
// exactly one publishDiagnostics notification with an empty diagnostics list.
func TestDidOpen_GoodDoc(t *testing.T) {
	workspaceDir, err := filepath.Abs("testdata/workspace")
	if err != nil {
		t.Fatal(err)
	}

	s := newSession(t, workspaceDir)
	defer s.close(t)

	// Initialize.
	s.send(t, makeInitializeReq(1, workspaceDir))
	s.readFrame(t) // initialize response
	s.send(t, makeInitializedNotif())

	goodYAML := strings.Join([]string{
		"apiVersion: operator.victoriametrics.com/v1beta1",
		"kind: VMCluster",
		"metadata:",
		"  name: my-cluster",
		"spec:",
		"  retentionPeriod: 30d",
	}, "\n")

	docURI := "file:///workspace/good.yaml"
	s.send(t, makeDidOpenReq(docURI, goodYAML, 1))

	// Wait for publishDiagnostics (the worker validates asynchronously).
	var diagNotif map[string]any
	for range 10 {
		frame, ok := s.readFrameTimeout(t, 5*time.Second)
		if !ok {
			t.Fatal("timed out waiting for publishDiagnostics")
		}
		if frame["method"] == "textDocument/publishDiagnostics" {
			diagNotif = frame
			break
		}
	}
	if diagNotif == nil {
		t.Fatal("never received publishDiagnostics")
	}

	params, ok := diagNotif["params"].(map[string]any)
	if !ok {
		t.Fatalf("params not a map: %v", diagNotif["params"])
	}
	diags, ok := params["diagnostics"].([]any)
	if !ok {
		t.Fatalf("diagnostics not an array: %v", params["diagnostics"])
	}
	if len(diags) != 0 {
		t.Errorf("expected 0 diagnostics for good doc, got %d: %v", len(diags), diags)
	}

	s.send(t, makeShutdownReq(2))
	s.readFrame(t)
	s.send(t, makeExitNotif())
	s.waitDone(t, 3*time.Second)
}

// TestDidOpen_BadDoc verifies that opening an invalid VMCluster YAML produces
// exactly one publishDiagnostics notification with at least one diagnostic.
func TestDidOpen_BadDoc(t *testing.T) {
	workspaceDir, err := filepath.Abs("testdata/workspace")
	if err != nil {
		t.Fatal(err)
	}

	s := newSession(t, workspaceDir)
	defer s.close(t)

	// Initialize.
	s.send(t, makeInitializeReq(1, workspaceDir))
	s.readFrame(t)
	s.send(t, makeInitializedNotif())

	// A doc missing the required "metadata" field.
	badYAML := strings.Join([]string{
		"apiVersion: operator.victoriametrics.com/v1beta1",
		"kind: VMCluster",
		"spec:",
		"  retentionPeriod: 30d",
	}, "\n")

	docURI := "file://" + workspaceDir + "/bad.yaml"
	s.send(t, makeDidOpenReq(docURI, badYAML, 1))

	// Wait for publishDiagnostics.
	var diagNotif map[string]any
	for range 10 {
		frame, ok := s.readFrameTimeout(t, 5*time.Second)
		if !ok {
			t.Fatal("timed out waiting for publishDiagnostics")
		}
		if frame["method"] == "textDocument/publishDiagnostics" {
			diagNotif = frame
			break
		}
	}
	if diagNotif == nil {
		t.Fatal("never received publishDiagnostics")
	}

	params, ok := diagNotif["params"].(map[string]any)
	if !ok {
		t.Fatalf("params not a map: %v", diagNotif["params"])
	}
	diags, ok := params["diagnostics"].([]any)
	if !ok {
		t.Fatalf("diagnostics not an array: %v", params["diagnostics"])
	}
	if len(diags) == 0 {
		t.Error("expected at least one diagnostic for bad doc, got 0")
	}

	// Verify the diagnostic has a range field and sane 0-based LSP positions.
	if len(diags) > 0 {
		diag, ok := diags[0].(map[string]any)
		if !ok {
			t.Fatal("diagnostic not a map")
		}
		rng, hasRange := diag["range"]
		if !hasRange {
			t.Error("diagnostic missing 'range' field")
		}
		if _, hasMessage := diag["message"]; !hasMessage {
			t.Error("diagnostic missing 'message' field")
		}

		// Assert that start.line and start.character are non-negative — this
		// catches a buggy "- 1" without clamping that would produce -1.
		// The missing-metadata error is not present in the YAML node map, so
		// the validator falls back to position (1,1) which toLSPDiagnostic
		// converts to LSP (0,0). Assert the exact expected values.
		if rng != nil {
			rngMap, ok := rng.(map[string]any)
			if !ok {
				t.Fatal("range not a map")
			}
			start, ok := rngMap["start"].(map[string]any)
			if !ok {
				t.Fatal("range.start not a map")
			}
			// JSON numbers decode as float64.
			startLine, ok := start["line"].(float64)
			if !ok {
				t.Fatalf("range.start.line not a float64: %T %v", start["line"], start["line"])
			}
			startChar, ok := start["character"].(float64)
			if !ok {
				t.Fatalf("range.start.character not a float64: %T %v", start["character"], start["character"])
			}
			if startLine < 0 {
				t.Errorf("range.start.line = %v, want >= 0 (negative indicates missing clamp)", startLine)
			}
			if startChar < 0 {
				t.Errorf("range.start.character = %v, want >= 0 (negative indicates missing clamp)", startChar)
			}
			// The missing-metadata pointer is not in the YAML node map, so the
			// validator falls back to yamldoc position (1,1), which
			// toLSPDiagnostic converts to LSP (0,0).
			if startLine != 0 {
				t.Errorf("range.start.line = %v, want 0 (fallback position maps to LSP line 0)", startLine)
			}
			if startChar != 0 {
				t.Errorf("range.start.character = %v, want 0 (fallback position maps to LSP character 0)", startChar)
			}
		}
	}

	s.send(t, makeShutdownReq(2))
	s.readFrame(t)
	s.send(t, makeExitNotif())
	s.waitDone(t, 3*time.Second)
}

// TestReaderLoop_NeverBlocks verifies that the reader loop exits promptly even
// when a worker goroutine is potentially mid-fetch. We send didOpen, then
// immediately shutdown+exit, and assert the server exits within 2 seconds.
func TestReaderLoop_NeverBlocks(t *testing.T) {
	workspaceDir, err := filepath.Abs("testdata/workspace")
	if err != nil {
		t.Fatal(err)
	}

	s := newSession(t, workspaceDir)
	defer s.close(t)

	// Initialize.
	s.send(t, makeInitializeReq(1, workspaceDir))
	s.readFrame(t)
	s.send(t, makeInitializedNotif())

	// Send didOpen (triggers async worker).
	longYAML := strings.Join([]string{
		"apiVersion: operator.victoriametrics.com/v1beta1",
		"kind: VMCluster",
		"metadata:",
		"  name: my-cluster",
	}, "\n")
	s.send(t, makeDidOpenReq("file://"+workspaceDir+"/test.yaml", longYAML, 1))

	// Immediately send shutdown + exit without waiting for publishDiagnostics.
	// This simulates an editor that shuts down mid-validation.
	start := time.Now()
	s.send(t, makeShutdownReq(2))

	// Read the shutdown response (not the publishDiagnostics notification).
	for {
		frame, ok := s.readFrameTimeout(t, 3*time.Second)
		if !ok {
			t.Fatal("timed out waiting for shutdown response")
		}
		// The frame could be either publishDiagnostics or the shutdown response.
		if frame["id"] != nil {
			// This is a response (has id) — must be the shutdown response.
			break
		}
		// Otherwise it's a notification (publishDiagnostics); keep reading.
	}

	s.send(t, makeExitNotif())

	if err := s.waitDone(t, 2*time.Second); err != nil {
		t.Errorf("server exit error: %v", err)
	}

	elapsed := time.Since(start)
	if elapsed > 2*time.Second {
		t.Errorf("server took too long to exit: %v (want < 2s)", elapsed)
	}
}

// TestDidClose_ClearsDiagnostics verifies that closing a document publishes
// empty diagnostics.
func TestDidClose_ClearsDiagnostics(t *testing.T) {
	workspaceDir, err := filepath.Abs("testdata/workspace")
	if err != nil {
		t.Fatal(err)
	}

	s := newSession(t, workspaceDir)
	defer s.close(t)

	s.send(t, makeInitializeReq(1, workspaceDir))
	s.readFrame(t)
	s.send(t, makeInitializedNotif())

	// Open a valid doc.
	docURI := "file:///workspace/closeme.yaml"
	validYAML := strings.Join([]string{
		"apiVersion: operator.victoriametrics.com/v1beta1",
		"kind: VMCluster",
		"metadata:",
		"  name: my-cluster",
	}, "\n")
	s.send(t, makeDidOpenReq(docURI, validYAML, 1))

	// Drain the publishDiagnostics for the open event.
	for {
		frame, ok := s.readFrameTimeout(t, 5*time.Second)
		if !ok {
			t.Fatal("timed out waiting for open publishDiagnostics")
		}
		if frame["method"] == "textDocument/publishDiagnostics" {
			break
		}
	}

	// Close the document.
	s.send(t, map[string]any{
		"jsonrpc": "2.0",
		"method":  "textDocument/didClose",
		"params": map[string]any{
			"textDocument": map[string]any{
				"uri": docURI,
			},
		},
	})

	// The server should immediately publish empty diagnostics on close.
	frame, ok := s.readFrameTimeout(t, 3*time.Second)
	if !ok {
		t.Fatal("timed out waiting for didClose publishDiagnostics")
	}
	if frame["method"] != "textDocument/publishDiagnostics" {
		t.Fatalf("expected publishDiagnostics, got method=%v", frame["method"])
	}
	params, _ := frame["params"].(map[string]any)
	diags, _ := params["diagnostics"].([]any)
	if len(diags) != 0 {
		t.Errorf("expected 0 diagnostics after close, got %d", len(diags))
	}

	s.send(t, makeShutdownReq(2))
	s.readFrame(t)
	s.send(t, makeExitNotif())
	s.waitDone(t, 3*time.Second)
}

// TestFraming_CRLF verifies the framing package round-trips with CRLF headers.
func TestFraming_CRLF(t *testing.T) {
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize"}`
	raw := "Content-Length: " + itoa(len(body)) + "\r\n\r\n" + body
	got, err := lsptest.ReadMessage(bufio.NewReader(strings.NewReader(raw)))
	if err != nil {
		t.Fatalf("ReadMessage CRLF: %v", err)
	}
	if string(got) != body {
		t.Errorf("body mismatch: got %q, want %q", got, body)
	}
}

// TestProtocol_DiagnosticSeverity verifies that the go.lsp.dev/protocol severity
// constants have the LSP-specified wire values. These are the values SchemaLock
// now uses after the M10a migration from internal/lsp/protocol.
func TestProtocol_DiagnosticSeverity(t *testing.T) {
	if lspprot.DiagnosticSeverityError != 1 {
		t.Errorf("DiagnosticSeverityError = %d, want 1", int(lspprot.DiagnosticSeverityError))
	}
	if lspprot.DiagnosticSeverityWarning != 2 {
		t.Errorf("DiagnosticSeverityWarning = %d, want 2", int(lspprot.DiagnosticSeverityWarning))
	}
	if lspprot.TextDocumentSyncKindFull != 1 {
		t.Errorf("TextDocumentSyncKindFull = %d, want 1", int(lspprot.TextDocumentSyncKindFull))
	}
}

// --------------------------------------------------------------------------
// M9 Tests: completion, hover, watched-files reload
// --------------------------------------------------------------------------

// goodYAML is a valid VMCluster YAML shared across M9 tests.
// Line numbers (1-based, as yamldoc sees them):
//
//	line 1: "apiVersion: operator.victoriametrics.com/v1beta1"
//	line 2: "kind: VMCluster"
//	line 3: "metadata:"
//	line 4: "  name: my-cluster"
//	line 5: "spec:"
//	line 6: "  retentionPeriod: 30d"
const goodYAMLForM9 = `apiVersion: operator.victoriametrics.com/v1beta1
kind: VMCluster
metadata:
  name: my-cluster
spec:
  retentionPeriod: 30d`

// makeCompletionReq builds a textDocument/completion request.
func makeCompletionReq(id int64, uri string, line, char int) map[string]any {
	return map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  "textDocument/completion",
		"params": map[string]any{
			"textDocument": map[string]any{
				"uri": uri,
			},
			"position": map[string]any{
				"line":      line,
				"character": char,
			},
		},
	}
}

// makeHoverReq builds a textDocument/hover request.
func makeHoverReq(id int64, uri string, line, char int) map[string]any {
	return map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  "textDocument/hover",
		"params": map[string]any{
			"textDocument": map[string]any{
				"uri": uri,
			},
			"position": map[string]any{
				"line":      line,
				"character": char,
			},
		},
	}
}

// makeWatchedFilesNotif builds a workspace/didChangeWatchedFiles notification.
func makeWatchedFilesNotif(uris ...string) map[string]any {
	changes := make([]any, len(uris))
	for i, uri := range uris {
		changes[i] = map[string]any{
			"uri":  uri,
			"type": 2, // Changed
		}
	}
	return map[string]any{
		"jsonrpc": "2.0",
		"method":  "workspace/didChangeWatchedFiles",
		"params": map[string]any{
			"changes": changes,
		},
	}
}

// initSession initialises the session (initialize + initialized) and returns
// the initialize response result map.
func initSession(t *testing.T, s *session, workspaceDir string) map[string]any {
	t.Helper()
	s.send(t, makeInitializeReq(1, workspaceDir))
	resp := s.readFrame(t)
	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("initialize result not a map: %v", resp["result"])
	}
	s.send(t, makeInitializedNotif())
	return result
}

// waitForDiagnostics reads frames until a textDocument/publishDiagnostics
// notification is received for the given URI, or fails the test on timeout.
func waitForDiagnostics(t *testing.T, s *session, uri string) map[string]any {
	t.Helper()
	for range 20 {
		frame, ok := s.readFrameTimeout(t, 5*time.Second)
		if !ok {
			t.Fatal("timed out waiting for publishDiagnostics")
		}
		if frame["method"] == "textDocument/publishDiagnostics" {
			params, _ := frame["params"].(map[string]any)
			if params["uri"] == uri {
				return params
			}
		}
	}
	t.Fatal("never received publishDiagnostics for " + uri)
	return nil
}

// TestInitialize_AdvertisesM9Capabilities verifies that after the M9 changes
// the initialize response advertises completionProvider and hoverProvider.
func TestInitialize_AdvertisesM9Capabilities(t *testing.T) {
	workspaceDir, err := filepath.Abs("testdata/workspace")
	if err != nil {
		t.Fatal(err)
	}

	s := newSession(t, workspaceDir)
	defer s.close(t)

	result := initSession(t, s, workspaceDir)
	caps, ok := result["capabilities"].(map[string]any)
	if !ok {
		t.Fatalf("capabilities not a map: %v", result["capabilities"])
	}

	// Verify completionProvider is present with triggerCharacters.
	cp, ok := caps["completionProvider"].(map[string]any)
	if !ok {
		t.Fatalf("completionProvider not a map: %v", caps["completionProvider"])
	}
	triggers, ok := cp["triggerCharacters"].([]any)
	if !ok {
		t.Fatalf("triggerCharacters not an array: %v", cp["triggerCharacters"])
	}
	found := false
	for _, tc := range triggers {
		if tc == ":" {
			found = true
		}
	}
	if !found {
		t.Errorf("triggerCharacters does not contain ':': %v", triggers)
	}

	// Verify hoverProvider is true.
	if caps["hoverProvider"] != true {
		t.Errorf("hoverProvider = %v, want true", caps["hoverProvider"])
	}

	s.send(t, makeShutdownReq(2))
	s.readFrame(t)
	s.send(t, makeExitNotif())
	s.waitDone(t, 3*time.Second)
}

// TestCompletion_PropertyNames verifies that completion at a key position
// inside "spec:" returns property names from the schema's spec.properties.
//
// Cursor is at LSP (line=5, char=2) which maps to 1-based line 6, col 3 —
// the start of "retentionPeriod" in "  retentionPeriod: 30d". Since we're
// inside the key token (targetCol 3 <= keyEndCol 3+15=18), IsKeyPosition=true.
// The parent pointer is "/spec", whose existing keys are ["retentionPeriod"].
// Expected completions: "clusterVersion" and "replicationFactor" (not "retentionPeriod").
func TestCompletion_PropertyNames(t *testing.T) {
	workspaceDir, err := filepath.Abs("testdata/workspace")
	if err != nil {
		t.Fatal(err)
	}

	s := newSession(t, workspaceDir)
	defer s.close(t)

	initSession(t, s, workspaceDir)

	docURI := "file:///workspace/comp_props.yaml"
	s.send(t, makeDidOpenReq(docURI, goodYAMLForM9, 1))
	// Wait for the validation worker to fetch and cache the schema.
	waitForDiagnostics(t, s, docURI)

	// Request completion at the key position of "retentionPeriod" (line 5, char 2,
	// 0-based — inside the key token so IsKeyPosition=true).
	s.send(t, makeCompletionReq(10, docURI, 5, 2))
	resp := s.readFrame(t)

	if resp["error"] != nil {
		t.Fatalf("unexpected error: %v", resp["error"])
	}
	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("result not a map: %T %v", resp["result"], resp["result"])
	}
	items, ok := result["items"].([]any)
	if !ok {
		t.Fatalf("items not an array: %v", result["items"])
	}

	// Collect label strings.
	labels := make(map[string]bool, len(items))
	for _, it := range items {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		if lbl, ok := m["label"].(string); ok {
			labels[lbl] = true
		}
	}

	// Expect spec properties excluding the already-present "retentionPeriod".
	if labels["retentionPeriod"] {
		t.Error("completion should not include already-present 'retentionPeriod'")
	}
	if !labels["replicationFactor"] {
		t.Errorf("completion missing 'replicationFactor'; got: %v", labels)
	}
	if !labels["clusterVersion"] {
		t.Errorf("completion missing 'clusterVersion'; got: %v", labels)
	}

	s.send(t, makeShutdownReq(11))
	s.readFrame(t)
	s.send(t, makeExitNotif())
	s.waitDone(t, 3*time.Second)
}

// TestCompletion_BlankLineNewSibling verifies that Ctrl+Space on a blank
// line at sibling indent (the most common autocomplete trigger in YAML)
// returns the parent object's remaining properties.
//
// Without this, users typing `<newline><indent><Ctrl+Space>` after the last
// spec field see "No suggestions" — the LSP misclassifies the empty line as
// a value position inside the previous key.
func TestCompletion_BlankLineNewSibling(t *testing.T) {
	workspaceDir, err := filepath.Abs("testdata/workspace")
	if err != nil {
		t.Fatal(err)
	}

	s := newSession(t, workspaceDir)
	defer s.close(t)

	initSession(t, s, workspaceDir)

	// Document with a blank line at sibling indent after `retentionPeriod`.
	// 0-based lines:
	//   0: apiVersion: ...
	//   1: kind: VMCluster
	//   2: metadata:
	//   3:   name: my-cluster
	//   4: spec:
	//   5:   retentionPeriod: 30d
	//   6:   [blank, two spaces of indent → col 3 in 1-based]
	yamlWithBlank := "apiVersion: operator.victoriametrics.com/v1beta1\n" +
		"kind: VMCluster\n" +
		"metadata:\n" +
		"  name: my-cluster\n" +
		"spec:\n" +
		"  retentionPeriod: 30d\n" +
		"  "

	docURI := "file:///workspace/comp_blank_sibling.yaml"
	s.send(t, makeDidOpenReq(docURI, yamlWithBlank, 1))
	waitForDiagnostics(t, s, docURI)

	// Cursor at LSP (line=6, char=2) → 1-based line 7, col 3 — the blank
	// line at sibling indent of retentionPeriod.
	s.send(t, makeCompletionReq(70, docURI, 6, 2))
	resp := s.readFrame(t)

	if resp["error"] != nil {
		t.Fatalf("unexpected error: %v", resp["error"])
	}
	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("result not a map: %T %v", resp["result"], resp["result"])
	}
	items, ok := result["items"].([]any)
	if !ok {
		t.Fatalf("items not an array: %v", result["items"])
	}

	labels := make(map[string]bool, len(items))
	for _, it := range items {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		if lbl, ok := m["label"].(string); ok {
			labels[lbl] = true
		}
	}

	if labels["retentionPeriod"] {
		t.Error("blank-line sibling completion should not include already-present 'retentionPeriod'")
	}
	if !labels["replicationFactor"] {
		t.Errorf("blank-line sibling completion missing 'replicationFactor'; got: %v", labels)
	}
	if !labels["clusterVersion"] {
		t.Errorf("blank-line sibling completion missing 'clusterVersion'; got: %v", labels)
	}

	// Verify textEdit.newText shape: scalar properties get "name: ", object/array
	// properties get the snippet form. Picking a completion must produce
	// parseable YAML — the original bug was that bare-key insertions broke
	// the YAML parser.
	// Note: InsertText is no longer set; TextEdit.NewText carries the insert
	// text (LSP §3.17: when textEdit is present, insertText is ignored).
	sawAnyTextEdit := false
	for _, it := range items {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		lbl, _ := m["label"].(string)
		te, hasTe := m["textEdit"].(map[string]any)
		if !hasTe {
			continue
		}
		newText, _ := te["newText"].(string)
		sawAnyTextEdit = true
		if !strings.HasPrefix(newText, lbl+":") {
			t.Errorf("textEdit.newText for %q must start with %q:; got %q", lbl, lbl, newText)
		}
	}
	if !sawAnyTextEdit {
		t.Error("no completion item had textEdit; YAML would break on acceptance")
	}

	s.send(t, makeShutdownReq(71))
	s.readFrame(t)
	s.send(t, makeExitNotif())
	s.waitDone(t, 3*time.Second)
}

// TestCompletion_NestIntoEmptyObject verifies that Ctrl+Space on a blank
// line at deeper indent than the deepest known key offers that key's child
// properties — i.e. the user is nesting into an otherwise-empty object.
//
// Uses `metadata:` as the parent since the test stub schema declares
// metadata.properties = {name, namespace}.
func TestCompletion_NestIntoEmptyObject(t *testing.T) {
	workspaceDir, err := filepath.Abs("testdata/workspace")
	if err != nil {
		t.Fatal(err)
	}

	s := newSession(t, workspaceDir)
	defer s.close(t)

	initSession(t, s, workspaceDir)

	// Document with `metadata:` as an empty object, cursor below at 2-space
	// indent (one level deeper than metadata's column 1).
	//   0: apiVersion: ...
	//   1: kind: VMCluster
	//   2: metadata:
	//   3:   [blank, 2-space indent → col 3 in 1-based]
	yamlNested := "apiVersion: operator.victoriametrics.com/v1beta1\n" +
		"kind: VMCluster\n" +
		"metadata:\n" +
		"  "

	docURI := "file:///workspace/comp_nest.yaml"
	s.send(t, makeDidOpenReq(docURI, yamlNested, 1))
	waitForDiagnostics(t, s, docURI)

	// Cursor at LSP (line=3, char=2) → 1-based line 4, col 3.
	s.send(t, makeCompletionReq(80, docURI, 3, 2))
	resp := s.readFrame(t)

	if resp["error"] != nil {
		t.Fatalf("unexpected error: %v", resp["error"])
	}
	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("result not a map: %T %v", resp["result"], resp["result"])
	}
	items, ok := result["items"].([]any)
	if !ok {
		t.Fatalf("items not an array: %v", result["items"])
	}

	labels := make(map[string]bool, len(items))
	for _, it := range items {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		if lbl, ok := m["label"].(string); ok {
			labels[lbl] = true
		}
	}
	if !labels["name"] {
		t.Errorf("nesting completion missing 'name'; got: %v", labels)
	}
	if !labels["namespace"] {
		t.Errorf("nesting completion missing 'namespace'; got: %v", labels)
	}

	s.send(t, makeShutdownReq(81))
	s.readFrame(t)
	s.send(t, makeExitNotif())
	s.waitDone(t, 3*time.Second)
}

// TestCompletion_RootProperties verifies that completion at a root-level key
// position returns the top-level schema properties.
//
// The fixture YAML contains only "kind: VMCluster", which gives the resolver
// enough to identify the schema. Cursor at LSP (line=0, char=0) maps to
// 1-based line 1, col 1 — the start of the "kind" key. positionAt classifies
// this as IsKeyPosition=true with parentPointer="" (root level). The schema
// has four root properties: apiVersion, kind, metadata, spec. Since "kind" is
// already present in the doc, completion must include at least apiVersion,
// metadata, and spec.
func TestCompletion_RootProperties(t *testing.T) {
	workspaceDir, err := filepath.Abs("testdata/workspace")
	if err != nil {
		t.Fatal(err)
	}

	s := newSession(t, workspaceDir)
	defer s.close(t)

	initSession(t, s, workspaceDir)

	// Minimal YAML: only kind is present so the resolver can identify the schema.
	// apiVersion is also needed for resolver.Resolve to work (group extraction).
	minimalYAML := "apiVersion: operator.victoriametrics.com/v1beta1\nkind: VMCluster\n"

	docURI := "file:///workspace/root_comp.yaml"
	s.send(t, makeDidOpenReq(docURI, minimalYAML, 1))
	// Wait for the validation worker to fetch and cache the schema.
	waitForDiagnostics(t, s, docURI)

	// Request completion at (line=1, char=0) — the "kind:" key on line 2 (0-based
	// line 1). positionAt classifies this as a key position with parentPointer=""
	// (root level). "kind" is already in existingKeys so it will be filtered.
	// The returned items must include "metadata" and "spec" (and "apiVersion" is
	// filtered since it is also present on line 0).
	s.send(t, makeCompletionReq(60, docURI, 1, 0))
	resp := s.readFrame(t)

	if resp["error"] != nil {
		t.Fatalf("unexpected error: %v", resp["error"])
	}
	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("result not a map: %T %v", resp["result"], resp["result"])
	}
	items, ok := result["items"].([]any)
	if !ok {
		t.Fatalf("items not an array: %v", result["items"])
	}

	labels := make(map[string]bool, len(items))
	for _, it := range items {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		if lbl, ok := m["label"].(string); ok {
			labels[lbl] = true
		}
	}

	// "kind" and "apiVersion" are already in the doc so they should be filtered.
	// "metadata" and "spec" are root-level properties not yet present.
	for _, want := range []string{"metadata", "spec"} {
		if !labels[want] {
			t.Errorf("root completion missing %q; got: %v", want, labels)
		}
	}
	if labels["kind"] {
		t.Error("root completion should not include already-present 'kind'")
	}
	if labels["apiVersion"] {
		t.Error("root completion should not include already-present 'apiVersion'")
	}

	s.send(t, makeShutdownReq(61))
	s.readFrame(t)
	s.send(t, makeExitNotif())
	s.waitDone(t, 3*time.Second)
}

// TestCompletion_EnumValues verifies that completion at a value position where
// the schema has an enum returns the enum members.
//
// Cursor is at LSP (line=5, char=18) — past the "retentionPeriod:" key
// (key column=3, length=15, keyEndCol=18; targetCol=19 > 18 → value position).
// The schema at "/spec/retentionPeriod" has enum: [1d, 7d, 30d, 90d, 1y].
func TestCompletion_EnumValues(t *testing.T) {
	workspaceDir, err := filepath.Abs("testdata/workspace")
	if err != nil {
		t.Fatal(err)
	}

	s := newSession(t, workspaceDir)
	defer s.close(t)

	initSession(t, s, workspaceDir)

	docURI := "file:///workspace/comp_enum.yaml"
	s.send(t, makeDidOpenReq(docURI, goodYAMLForM9, 1))
	waitForDiagnostics(t, s, docURI)

	// Cursor at (line=5, char=18) — past the "retentionPeriod:" separator.
	// targetCol = 19 > keyEndCol 18 → value position.
	s.send(t, makeCompletionReq(20, docURI, 5, 18))
	resp := s.readFrame(t)

	if resp["error"] != nil {
		t.Fatalf("unexpected error: %v", resp["error"])
	}
	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("result not a map: %T %v", resp["result"], resp["result"])
	}
	items, ok := result["items"].([]any)
	if !ok {
		t.Fatalf("items not an array: %v", result["items"])
	}

	labels := make(map[string]bool, len(items))
	for _, it := range items {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		if lbl, ok := m["label"].(string); ok {
			labels[lbl] = true
		}
	}

	wantEnum := []string{"1d", "7d", "30d", "90d", "1y"}
	for _, e := range wantEnum {
		if !labels[e] {
			t.Errorf("completion missing enum value %q; got: %v", e, labels)
		}
	}

	// Verify kind is EnumMember (13).
	for _, it := range items {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		kind, _ := m["kind"].(float64)
		if kind != 13 {
			t.Errorf("item %q has kind %v, want 13 (EnumMember)", m["label"], m["kind"])
		}
	}

	s.send(t, makeShutdownReq(21))
	s.readFrame(t)
	s.send(t, makeExitNotif())
	s.waitDone(t, 3*time.Second)
}

// TestHover_FieldDescription verifies that hovering over a field with a schema
// description returns a Hover with non-empty Markdown content.
//
// Cursor is at LSP (line=2, char=0) which maps to 1-based line 3, col 1 —
// the "metadata:" key. The schema's metadata property has description
// "Standard object metadata.".
func TestHover_FieldDescription(t *testing.T) {
	workspaceDir, err := filepath.Abs("testdata/workspace")
	if err != nil {
		t.Fatal(err)
	}

	s := newSession(t, workspaceDir)
	defer s.close(t)

	initSession(t, s, workspaceDir)

	docURI := "file:///workspace/hover.yaml"
	s.send(t, makeDidOpenReq(docURI, goodYAMLForM9, 1))
	waitForDiagnostics(t, s, docURI)

	// Hover over "metadata" key at LSP (line=2, char=0).
	s.send(t, makeHoverReq(30, docURI, 2, 0))
	resp := s.readFrame(t)

	if resp["error"] != nil {
		t.Fatalf("unexpected hover error: %v", resp["error"])
	}

	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("hover result not a map: %T %v", resp["result"], resp["result"])
	}

	contents, ok := result["contents"].(map[string]any)
	if !ok {
		t.Fatalf("hover contents not a map: %v", result["contents"])
	}
	if contents["kind"] != "markdown" {
		t.Errorf("hover kind = %v, want 'markdown'", contents["kind"])
	}
	value, _ := contents["value"].(string)
	if value == "" {
		t.Error("hover contents.value is empty, want non-empty description")
	}
	// Rich hover: signature fence must be present.
	if !strings.Contains(value, "```yaml") {
		t.Errorf("hover value missing YAML fence block; got:\n%s", value)
	}
	// Signature line must contain the field name and its type.
	if !strings.Contains(value, "metadata:") || !strings.Contains(value, "object") {
		t.Errorf("hover signature does not mention 'metadata' and 'object'; got:\n%s", value)
	}
	// Footer must contain the kind and apiVersion.
	if !strings.Contains(value, "VMCluster") {
		t.Errorf("hover footer missing 'VMCluster'; got:\n%s", value)
	}
	if !strings.Contains(value, "operator.victoriametrics.com/v1beta1") {
		t.Errorf("hover footer missing apiVersion; got:\n%s", value)
	}
	// Description must still be present.
	if !strings.Contains(strings.ToLower(value), "metadata") && !strings.Contains(strings.ToLower(value), "object") {
		t.Errorf("hover value does not mention 'metadata' or 'object'; got:\n%s", value)
	}

	s.send(t, makeShutdownReq(31))
	s.readFrame(t)
	s.send(t, makeExitNotif())
	s.waitDone(t, 3*time.Second)
}

// TestHover_Unknown verifies that hovering over a field not described by the
// schema returns a null result per the LSP convention "no hover info".
//
// We use a YAML with an "unknownField" key that is not present in the schema's
// properties map. schemaAtPointer cannot navigate to it and returns nil, so
// handleHover returns a null result.
//
// The tiny VMCluster schema does not have additionalProperties: false, so the
// document is still valid — we just get no hover info for the unknown key.
func TestHover_Unknown(t *testing.T) {
	workspaceDir, err := filepath.Abs("testdata/workspace")
	if err != nil {
		t.Fatal(err)
	}

	s := newSession(t, workspaceDir)
	defer s.close(t)

	initSession(t, s, workspaceDir)

	// YAML with a top-level field not present in the schema.
	// "unknownField" is on line 4 (0-based line 3).
	yamlWithUnknown := strings.Join([]string{
		"apiVersion: operator.victoriametrics.com/v1beta1",
		"kind: VMCluster",
		"metadata:",
		"  name: my-cluster",
		"unknownField: somevalue",
	}, "\n")

	docURI := "file:///workspace/hover_unknown.yaml"
	s.send(t, makeDidOpenReq(docURI, yamlWithUnknown, 1))
	waitForDiagnostics(t, s, docURI)

	// Hover at LSP (line=4, char=0) → 1-based line 5, col 1 — the
	// "unknownField" key. schemaAtPointer("/unknownField") returns nil because
	// the schema has no such property. handleHover returns null.
	s.send(t, makeHoverReq(40, docURI, 4, 0))
	resp := s.readFrame(t)

	if resp["error"] != nil {
		t.Fatalf("unexpected hover error: %v", resp["error"])
	}

	// result must be null (nil) when the schema has no coverage for this field.
	if resp["result"] != nil {
		t.Errorf("hover result = %v, want null for field not in schema", resp["result"])
	}

	s.send(t, makeShutdownReq(41))
	s.readFrame(t)
	s.send(t, makeExitNotif())
	s.waitDone(t, 3*time.Second)
}

// TestHover_RequiredField verifies that hovering over a field that appears in
// the schema's required list produces a signature line ending with "# required".
//
// The tiny VMCluster schema marks apiVersion, kind, and metadata as required at
// the root. We hover over "kind" (LSP line=1, char=0) and assert the marker.
func TestHover_RequiredField(t *testing.T) {
	workspaceDir, err := filepath.Abs("testdata/workspace")
	if err != nil {
		t.Fatal(err)
	}

	s := newSession(t, workspaceDir)
	defer s.close(t)

	initSession(t, s, workspaceDir)

	docURI := "file:///workspace/hover_required.yaml"
	s.send(t, makeDidOpenReq(docURI, goodYAMLForM9, 1))
	waitForDiagnostics(t, s, docURI)

	// Hover over "kind" key at LSP (line=1, char=0).
	s.send(t, makeHoverReq(50, docURI, 1, 0))
	resp := s.readFrame(t)

	if resp["error"] != nil {
		t.Fatalf("unexpected hover error: %v", resp["error"])
	}

	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("hover result not a map: %T %v", resp["result"], resp["result"])
	}

	contents, ok := result["contents"].(map[string]any)
	if !ok {
		t.Fatalf("hover contents not a map: %v", result["contents"])
	}

	value, _ := contents["value"].(string)
	if value == "" {
		t.Fatal("hover contents.value is empty")
	}
	if !strings.Contains(value, "# required") {
		t.Errorf("hover value does not contain '# required' marker for a required field;\ngot:\n%s", value)
	}

	s.send(t, makeShutdownReq(51))
	s.readFrame(t)
	s.send(t, makeExitNotif())
	s.waitDone(t, 3*time.Second)
}

// TestWatchedFiles_ReloadsLockfile verifies that sending
// workspace/didChangeWatchedFiles for schemalock.lock triggers re-validation
// of all open documents.
//
// We open a valid doc, wait for the first publishDiagnostics (empty), then
// send a watched-files notification for schemalock.lock, and expect a second
// publishDiagnostics (also empty, since we didn't actually change the lockfile
// on disk — but the server re-queued and published). This confirms the reload
// path was exercised.
func TestWatchedFiles_ReloadsLockfile(t *testing.T) {
	workspaceDir, err := filepath.Abs("testdata/workspace")
	if err != nil {
		t.Fatal(err)
	}

	s := newSession(t, workspaceDir)
	defer s.close(t)

	initSession(t, s, workspaceDir)

	docURI := "file:///workspace/watched.yaml"
	s.send(t, makeDidOpenReq(docURI, goodYAMLForM9, 1))

	// Wait for first publishDiagnostics.
	params1 := waitForDiagnostics(t, s, docURI)
	diags1, _ := params1["diagnostics"].([]any)
	if len(diags1) != 0 {
		t.Errorf("first diagnostics: expected 0, got %d", len(diags1))
	}

	// Construct the lockfile URI.
	lockfileURI := "file://" + filepath.Join(workspaceDir, "schemalock.lock")

	// Send a watched-files notification for the lockfile.
	s.send(t, makeWatchedFilesNotif(lockfileURI))

	// The server should re-queue validation and produce another publishDiagnostics.
	params2 := waitForDiagnostics(t, s, docURI)
	diags2, _ := params2["diagnostics"].([]any)
	// Same lockfile, same schema → still 0 diagnostics.
	if len(diags2) != 0 {
		t.Errorf("second diagnostics (after reload): expected 0, got %d: %v", len(diags2), diags2)
	}

	s.send(t, makeShutdownReq(50))
	s.readFrame(t)
	s.send(t, makeExitNotif())
	s.waitDone(t, 3*time.Second)
}

// TestCompletion_IsIncompleteFalse_WhenItemsPresent verifies that when the
// server returns a non-empty completion list (property names at a key position),
// the CompletionList.isIncomplete field is false so VS Code filters locally
// against the returned items as the user types rather than re-querying the server.
func TestCompletion_IsIncompleteFalse_WhenItemsPresent(t *testing.T) {
	workspaceDir, err := filepath.Abs("testdata/workspace")
	if err != nil {
		t.Fatal(err)
	}

	s := newSession(t, workspaceDir)
	defer s.close(t)

	initSession(t, s, workspaceDir)

	docURI := "file:///workspace/comp_incomplete.yaml"
	s.send(t, makeDidOpenReq(docURI, goodYAMLForM9, 1))
	waitForDiagnostics(t, s, docURI)

	// Cursor at (line=5, char=2) — inside the key position for "retentionPeriod".
	// The parent schema has remaining properties so items will be non-empty.
	s.send(t, makeCompletionReq(100, docURI, 5, 2))
	resp := s.readFrame(t)

	if resp["error"] != nil {
		t.Fatalf("unexpected error: %v", resp["error"])
	}
	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("result not a map: %T %v", resp["result"], resp["result"])
	}
	items, ok := result["items"].([]any)
	if !ok {
		t.Fatalf("items not an array: %v", result["items"])
	}
	// Guard: ensure this test only asserts isIncomplete when items are present.
	if len(items) == 0 {
		t.Fatal("expected non-empty items list for this completion position")
	}
	if result["isIncomplete"] != false {
		t.Errorf("isIncomplete = %v, want false (VS Code must filter locally as user types)", result["isIncomplete"])
	}
	// Cursor at (line=5, char=2) is inside "retentionPeriod" which starts at
	// char=2 and ends at char=17 (2 + len("retentionPeriod")).
	// The TextEdit.Range must cover that full word so VS Code uses it as both
	// the filter prefix derivation source and the replace target on accept.
	const wantStartChar = uint32(2)
	const wantEndChar = uint32(17) // 2 + len("retentionPeriod")
	for _, it := range items {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		if m["filterText"] != m["label"] {
			t.Errorf("filterText %v, want label %v", m["filterText"], m["label"])
		}
		// Each item must carry a textEdit with the word range.
		te, ok := m["textEdit"].(map[string]any)
		if !ok {
			t.Errorf("item %v: textEdit missing or wrong type: %T", m["label"], m["textEdit"])
			continue
		}
		rng, ok := te["range"].(map[string]any)
		if !ok {
			t.Errorf("item %v: textEdit.range missing: %v", m["label"], te["range"])
			continue
		}
		start, _ := rng["start"].(map[string]any)
		end, _ := rng["end"].(map[string]any)
		// JSON numbers decode as float64.
		if start["line"] != float64(5) || start["character"] != float64(wantStartChar) {
			t.Errorf("item %v: textEdit.range.start = %v, want {line:5, character:%d}",
				m["label"], start, wantStartChar)
		}
		if end["line"] != float64(5) || end["character"] != float64(wantEndChar) {
			t.Errorf("item %v: textEdit.range.end = %v, want {line:5, character:%d}",
				m["label"], end, wantEndChar)
		}
		// insertText must be absent (empty string) — TextEdit takes precedence.
		if it, ok := m["insertText"]; ok && it != "" && it != nil {
			t.Errorf("item %v: insertText = %v, want absent/empty", m["label"], it)
		}
	}

	s.send(t, makeShutdownReq(101))
	s.readFrame(t)
	s.send(t, makeExitNotif())
	s.waitDone(t, 3*time.Second)
}

// TestCompletion_IsIncompleteFalse_EnumValues verifies that when the server
// returns enum completion values, isIncomplete is false so VS Code filters
// locally as the user types.
func TestCompletion_IsIncompleteFalse_EnumValues(t *testing.T) {
	workspaceDir, err := filepath.Abs("testdata/workspace")
	if err != nil {
		t.Fatal(err)
	}

	s := newSession(t, workspaceDir)
	defer s.close(t)

	initSession(t, s, workspaceDir)

	docURI := "file:///workspace/comp_enum_incomplete.yaml"
	s.send(t, makeDidOpenReq(docURI, goodYAMLForM9, 1))
	waitForDiagnostics(t, s, docURI)

	// Cursor at (line=5, char=18) — value position for "retentionPeriod" which
	// has an enum. This triggers the enum completion path.
	s.send(t, makeCompletionReq(110, docURI, 5, 18))
	resp := s.readFrame(t)

	if resp["error"] != nil {
		t.Fatalf("unexpected error: %v", resp["error"])
	}
	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("result not a map: %T %v", resp["result"], resp["result"])
	}
	items, ok := result["items"].([]any)
	if !ok {
		t.Fatalf("items not an array: %v", result["items"])
	}
	if len(items) == 0 {
		t.Fatal("expected non-empty enum items for retentionPeriod")
	}
	if result["isIncomplete"] != false {
		t.Errorf("isIncomplete = %v, want false for enum completion", result["isIncomplete"])
	}
	// Cursor at (line=5, char=18) is on the space after "retentionPeriod: ".
	// The word range at whitespace is zero-width: start == end == {5, 18}.
	for _, it := range items {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		if m["filterText"] != m["label"] {
			t.Errorf("filterText %v, want label %v", m["filterText"], m["label"])
		}
		// Each enum item must carry a textEdit with zero-width range at cursor.
		te, ok := m["textEdit"].(map[string]any)
		if !ok {
			t.Errorf("item %v: textEdit missing or wrong type: %T", m["label"], m["textEdit"])
			continue
		}
		rng, ok := te["range"].(map[string]any)
		if !ok {
			t.Errorf("item %v: textEdit.range missing: %v", m["label"], te["range"])
			continue
		}
		start, _ := rng["start"].(map[string]any)
		end, _ := rng["end"].(map[string]any)
		// Zero-width range: start == end == {5, 18}.
		if start["line"] != float64(5) || start["character"] != float64(18) {
			t.Errorf("item %v: textEdit.range.start = %v, want {line:5, character:18}",
				m["label"], start)
		}
		if end["line"] != float64(5) || end["character"] != float64(18) {
			t.Errorf("item %v: textEdit.range.end = %v, want {line:5, character:18}",
				m["label"], end)
		}
	}

	s.send(t, makeShutdownReq(111))
	s.readFrame(t)
	s.send(t, makeExitNotif())
	s.waitDone(t, 3*time.Second)
}

// TestCompletion_IsIncompleteFalse_WhenEmpty verifies that when the server
// returns an empty completion list (no schema match), isIncomplete remains
// false. This is the "cold start" or "no match" behaviour — the empty sentinel
// list must not claim to be a partial list.
func TestCompletion_IsIncompleteFalse_WhenEmpty(t *testing.T) {
	workspaceDir, err := filepath.Abs("testdata/workspace")
	if err != nil {
		t.Fatal(err)
	}

	s := newSession(t, workspaceDir)
	defer s.close(t)

	initSession(t, s, workspaceDir)

	// Use a YAML with no apiVersion/kind so the resolver returns no match and
	// handleCompletion returns the empty sentinel.
	emptyYAML := "foo: bar\n"
	docURI := "file:///workspace/comp_empty_sentinel.yaml"
	s.send(t, makeDidOpenReq(docURI, emptyYAML, 1))
	// A publishDiagnostics notification is NOT guaranteed here (no apiVersion/kind
	// means the worker skips validation). Use a short timeout to be safe.
	_, _ = s.readFrameTimeout(t, 500*time.Millisecond)

	s.send(t, makeCompletionReq(120, docURI, 0, 0))
	resp := s.readFrame(t)

	if resp["error"] != nil {
		t.Fatalf("unexpected error: %v", resp["error"])
	}
	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("result not a map: %T %v", resp["result"], resp["result"])
	}
	if result["isIncomplete"] != false {
		t.Errorf("isIncomplete = %v, want false for empty completion list", result["isIncomplete"])
	}
	items, _ := result["items"].([]any)
	if len(items) != 0 {
		t.Errorf("expected 0 items, got %d", len(items))
	}

	s.send(t, makeShutdownReq(121))
	s.readFrame(t)
	s.send(t, makeExitNotif())
	s.waitDone(t, 3*time.Second)
}

// TestCompletion_KindBootstrap verifies the bootstrap kind-value completion
// path: opening a YAML with apiVersion set but kind empty, then sending a
// completion request at the kind value position, returns a non-empty list of
// Kind names for the apiVersion's group with isIncomplete=false.
//
// Cursor is at LSP (line=1, char=5) — just past the "kind:" separator on line
// 1 (0-based). positionAt maps this to the value position of /kind
// (IsKeyPosition=false), which is the bootstrap trigger predicate.
//
// The testdata/workspace lockfile contains exactly one entry under
// operator.victoriametrics.com (VMCluster at v0.70.0), so the assertion
// checks for VMCluster's label and kind==13 (EnumMember) without asserting
// the exact list, keeping the test robust to seed-data changes.
func TestCompletion_KindBootstrap(t *testing.T) {
	workspaceDir, err := filepath.Abs("testdata/workspace")
	if err != nil {
		t.Fatal(err)
	}

	s := newSession(t, workspaceDir)
	defer s.close(t)

	initSession(t, s, workspaceDir)

	// A document with apiVersion set but kind empty — the bootstrap trigger.
	bootstrapYAML := "apiVersion: operator.victoriametrics.com/v1beta1\nkind:\n"
	docURI := "file:///workspace/bootstrap_kind.yaml"
	s.send(t, makeDidOpenReq(docURI, bootstrapYAML, 1))

	// The worker skips validation when Kind == "" (silent skip, no
	// publishDiagnostics). Drain any unexpected notifications with a short
	// timeout before sending the completion request.
	_, _ = s.readFrameTimeout(t, 500*time.Millisecond)

	// Cursor at (line=1, char=5) — just past the "kind:" colon.
	// positionAt: targetLine=2, targetCol=6; kind key starts at col 1,
	// keyEndCol = 1 + len("kind") = 5; targetCol(6) > keyEndCol(5) → value
	// position, Pointer="/kind", IsKeyPosition=false.
	s.send(t, makeCompletionReq(130, docURI, 1, 5))
	resp := s.readFrame(t)

	if resp["error"] != nil {
		t.Fatalf("unexpected error: %v", resp["error"])
	}
	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("result not a map: %T %v", resp["result"], resp["result"])
	}
	items, ok := result["items"].([]any)
	if !ok {
		t.Fatalf("items not an array: %v", result["items"])
	}
	if len(items) == 0 {
		t.Fatal("expected non-empty kind bootstrap completion list, got 0 items")
	}

	// isIncomplete must be false — bootstrap completions use a closed list so
	// VS Code filters locally as the user types.
	if result["isIncomplete"] != false {
		t.Errorf("isIncomplete = %v, want false for bootstrap kind completion", result["isIncomplete"])
	}

	// At least VMCluster must appear (it is in the testdata lockfile).
	labels := make(map[string]bool, len(items))
	for _, it := range items {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		if lbl, ok := m["label"].(string); ok {
			labels[lbl] = true
		}
	}
	if !labels["VMCluster"] {
		t.Errorf("expected VMCluster in kind bootstrap completions; got labels: %v", labels)
	}

	// Every item must have kind==13 (CompletionItemKindEnumMember).
	for _, it := range items {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		kind, _ := m["kind"].(float64)
		if kind != 13 {
			t.Errorf("item %q has completion kind %v, want 13 (EnumMember)", m["label"], m["kind"])
		}
	}

	// textEdit.range must be zero-width at cursor (1,5) — cursor is exactly
	// after "kind:" with no value yet, so wordRangeAt returns zero-width.
	// textEdit.newText must equal the label.
	for _, it := range items {
		m, _ := it.(map[string]any)
		te, ok := m["textEdit"].(map[string]any)
		if !ok {
			t.Errorf("item %q missing textEdit", m["label"])
			continue
		}
		rng, _ := te["range"].(map[string]any)
		start, _ := rng["start"].(map[string]any)
		end, _ := rng["end"].(map[string]any)
		if start["line"] != float64(1) || start["character"] != float64(5) ||
			end["line"] != float64(1) || end["character"] != float64(5) {
			t.Errorf("item %q textEdit.range = %v, want zero-width {1,5}", m["label"], rng)
		}
		if newText, _ := te["newText"].(string); newText != m["label"] {
			t.Errorf("item %q textEdit.newText = %q, want label", m["label"], newText)
		}
	}

	s.send(t, makeShutdownReq(131))
	s.readFrame(t)
	s.send(t, makeExitNotif())
	s.waitDone(t, 3*time.Second)
}

// TestCompletion_KindBootstrap_CDNFallback verifies bootstrap kind
// completion works when there is no schemalock.yaml in the hierarchy —
// the resolver falls back to cdn.versions()[0] and fetches the manifest
// from there.
func TestCompletion_KindBootstrap_CDNFallback(t *testing.T) {
	// An empty temp dir → no schemalock.yaml above it → PinFor returns
	// ("", false, nil), forcing the CDN fallback path.
	emptyDir := t.TempDir()

	s := newSession(t, emptyDir)
	defer s.close(t)

	initSession(t, s, emptyDir)

	bootstrapYAML := "apiVersion: operator.victoriametrics.com/v1beta1\nkind:\n"
	docURI := "file://" + filepath.Join(emptyDir, "bootstrap_kind.yaml")
	s.send(t, makeDidOpenReq(docURI, bootstrapYAML, 1))
	_, _ = s.readFrameTimeout(t, 500*time.Millisecond)

	s.send(t, makeCompletionReq(140, docURI, 1, 5))
	resp := s.readFrame(t)

	if resp["error"] != nil {
		t.Fatalf("unexpected error: %v", resp["error"])
	}
	result, _ := resp["result"].(map[string]any)
	items, _ := result["items"].([]any)
	if len(items) == 0 {
		t.Fatal("expected non-empty CDN-fallback bootstrap items, got 0")
	}
	found := false
	for _, it := range items {
		m, _ := it.(map[string]any)
		if m["label"] == "VMCluster" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected VMCluster via CDN fallback; got items: %v", items)
	}

	s.send(t, makeShutdownReq(141))
	s.readFrame(t)
	s.send(t, makeExitNotif())
	s.waitDone(t, 3*time.Second)
}

// --------------------------------------------------------------------------
// Task 5: Integration tests — nested object + array navigation (TinyNested)
// --------------------------------------------------------------------------

// goodYAMLNested is the base TinyNested document used across Task 5/6 tests.
// 0-based line numbers:
//   0: apiVersion: certmanager.io/v1
//   1: kind: TinyNested
//   2: spec:
//   3:   issuerRef:
//   4:     name: my-issuer
//   5:   dnsNames:
//   6:     - example.com
//   7:   subjects:
//   8:     - name: alice
//   9:       kind: User
const goodYAMLNested = `apiVersion: certmanager.io/v1
kind: TinyNested
spec:
  issuerRef:
    name: my-issuer
  dnsNames:
    - example.com
  subjects:
    - name: alice
      kind: User`

// initSessionNested initialises a TinyNested session (initialize + initialized).
func initSessionNested(t *testing.T, s *session) {
	t.Helper()
	workspaceDir, err := filepath.Abs("testdata/workspace")
	if err != nil {
		t.Fatal(err)
	}
	s.send(t, makeInitializeReq(1, workspaceDir))
	s.readFrame(t)
	s.send(t, makeInitializedNotif())
}

// TestNested_NestedObject_KeyPosition_Properties verifies that completion at a
// blank line nested inside spec.issuerRef (value side of an object reached via
// $ref) returns the issuerRef properties: name, kind, group.
func TestNested_NestedObject_KeyPosition_Properties(t *testing.T) {
	s := newSessionNested(t)
	defer s.close(t)
	initSessionNested(t, s)

	// YAML: cursor on a blank line indented inside issuerRef (after name: my-issuer).
	// 0-based lines:
	//   0: apiVersion: certmanager.io/v1
	//   1: kind: TinyNested
	//   2: spec:
	//   3:   issuerRef:
	//   4:     name: my-issuer
	//   5:   [blank, 4-space indent → cursor at col 5 in 1-based]
	yamlDoc := "apiVersion: certmanager.io/v1\n" +
		"kind: TinyNested\n" +
		"spec:\n" +
		"  issuerRef:\n" +
		"    name: my-issuer\n" +
		"    "

	docURI := "file:///workspace/nested_key.yaml"
	s.send(t, makeDidOpenReq(docURI, yamlDoc, 1))
	waitForDiagnostics(t, s, docURI)

	// Cursor at LSP (line=5, char=4) — inside issuerRef's child scope.
	s.send(t, makeCompletionReq(300, docURI, 5, 4))
	resp := s.readFrame(t)

	if resp["error"] != nil {
		t.Fatalf("unexpected error: %v", resp["error"])
	}
	result, _ := resp["result"].(map[string]any)
	items, _ := result["items"].([]any)

	labels := make(map[string]bool, len(items))
	for _, it := range items {
		m, _ := it.(map[string]any)
		if lbl, _ := m["label"].(string); lbl != "" {
			labels[lbl] = true
		}
	}

	for _, want := range []string{"kind", "group"} {
		if !labels[want] {
			t.Errorf("issuerRef key-position completion missing %q; got: %v", want, labels)
		}
	}
	// "name" is already present so it should be filtered.
	if labels["name"] {
		t.Error("issuerRef key-position completion should not include already-present 'name'")
	}

	s.send(t, makeShutdownReq(301))
	s.readFrame(t)
	s.send(t, makeExitNotif())
	s.waitDone(t, 3*time.Second)
}

// TestNested_NestedObject_ValuePosition_Enum verifies that completion at the
// value position of spec.issuerRef.kind returns the enum values Issuer and
// ClusterIssuer.
func TestNested_NestedObject_ValuePosition_Enum(t *testing.T) {
	s := newSessionNested(t)
	defer s.close(t)
	initSessionNested(t, s)

	// YAML with issuerRef.kind present on line 5 (0-based).
	// 0-based lines:
	//   4:     kind: Issuer
	yamlDoc := "apiVersion: certmanager.io/v1\n" +
		"kind: TinyNested\n" +
		"spec:\n" +
		"  issuerRef:\n" +
		"    kind: Issuer\n"

	docURI := "file:///workspace/nested_enum.yaml"
	s.send(t, makeDidOpenReq(docURI, yamlDoc, 1))
	waitForDiagnostics(t, s, docURI)

	// "kind" starts at col 5 (1-based) = char 4 (0-based); length=4; keyEndCol=8.
	// Cursor at char=10 (past ": ") → value position.
	s.send(t, makeCompletionReq(310, docURI, 4, 10))
	resp := s.readFrame(t)

	if resp["error"] != nil {
		t.Fatalf("unexpected error: %v", resp["error"])
	}
	result, _ := resp["result"].(map[string]any)
	items, _ := result["items"].([]any)

	labels := make(map[string]bool, len(items))
	for _, it := range items {
		m, _ := it.(map[string]any)
		if lbl, _ := m["label"].(string); lbl != "" {
			labels[lbl] = true
		}
	}

	for _, want := range []string{"Issuer", "ClusterIssuer"} {
		if !labels[want] {
			t.Errorf("issuerRef.kind enum completion missing %q; got: %v", want, labels)
		}
	}

	s.send(t, makeShutdownReq(311))
	s.readFrame(t)
	s.send(t, makeExitNotif())
	s.waitDone(t, 3*time.Second)
}

// TestNested_ArrayOfObjects_KeyPosition verifies that completion on a blank line
// inside an array-of-objects item (spec.subjects[0]) returns the item's
// object properties: name, kind.
func TestNested_ArrayOfObjects_KeyPosition(t *testing.T) {
	s := newSessionNested(t)
	defer s.close(t)
	initSessionNested(t, s)

	// YAML with one subjects item that only has "name", cursor on next line.
	// 0-based lines:
	//   0: apiVersion: certmanager.io/v1
	//   1: kind: TinyNested
	//   2: spec:
	//   3:   subjects:
	//   4:     - name: alice
	//   5:       [blank, 6-space indent → sibling of name inside item]
	yamlDoc := "apiVersion: certmanager.io/v1\n" +
		"kind: TinyNested\n" +
		"spec:\n" +
		"  subjects:\n" +
		"    - name: alice\n" +
		"      "

	docURI := "file:///workspace/arr_obj_key.yaml"
	s.send(t, makeDidOpenReq(docURI, yamlDoc, 1))
	waitForDiagnostics(t, s, docURI)

	// Cursor at (line=5, char=6) — sibling indent of "name" inside item 0.
	s.send(t, makeCompletionReq(320, docURI, 5, 6))
	resp := s.readFrame(t)

	if resp["error"] != nil {
		t.Fatalf("unexpected error: %v", resp["error"])
	}
	result, _ := resp["result"].(map[string]any)
	items, _ := result["items"].([]any)

	labels := make(map[string]bool, len(items))
	for _, it := range items {
		m, _ := it.(map[string]any)
		if lbl, _ := m["label"].(string); lbl != "" {
			labels[lbl] = true
		}
	}

	if !labels["kind"] {
		t.Errorf("subjects[0] key-position completion missing 'kind'; got: %v", labels)
	}
	// "name" should be filtered (already present).
	if labels["name"] {
		t.Error("subjects[0] completion should not include already-present 'name'")
	}

	s.send(t, makeShutdownReq(321))
	s.readFrame(t)
	s.send(t, makeExitNotif())
	s.waitDone(t, 3*time.Second)
}

// TestNested_ArrayOfObjects_EnumValue verifies that completion at the value
// position of spec.subjects[0].kind returns the enum values Group and User.
func TestNested_ArrayOfObjects_EnumValue(t *testing.T) {
	s := newSessionNested(t)
	defer s.close(t)
	initSessionNested(t, s)

	// YAML with subjects[0].kind present.
	// 0-based lines:
	//   4:       kind: Group
	yamlDoc := "apiVersion: certmanager.io/v1\n" +
		"kind: TinyNested\n" +
		"spec:\n" +
		"  subjects:\n" +
		"    - kind: Group\n"

	docURI := "file:///workspace/arr_obj_enum.yaml"
	s.send(t, makeDidOpenReq(docURI, yamlDoc, 1))
	waitForDiagnostics(t, s, docURI)

	// "kind" starts at char 6 (0-based); length=4; keyEndCol=10.
	// Cursor at char=12 (past ": ") → value position.
	s.send(t, makeCompletionReq(330, docURI, 4, 12))
	resp := s.readFrame(t)

	if resp["error"] != nil {
		t.Fatalf("unexpected error: %v", resp["error"])
	}
	result, _ := resp["result"].(map[string]any)
	items, _ := result["items"].([]any)

	labels := make(map[string]bool, len(items))
	for _, it := range items {
		m, _ := it.(map[string]any)
		if lbl, _ := m["label"].(string); lbl != "" {
			labels[lbl] = true
		}
	}

	for _, want := range []string{"Group", "User"} {
		if !labels[want] {
			t.Errorf("subjects[0].kind enum completion missing %q; got: %v", want, labels)
		}
	}

	s.send(t, makeShutdownReq(331))
	s.readFrame(t)
	s.send(t, makeExitNotif())
	s.waitDone(t, 3*time.Second)
}

// --------------------------------------------------------------------------
// Task 6: Hover regression guard — array + $ref navigation
// --------------------------------------------------------------------------

// TestNested_Hover_ArrayItemEnum verifies that hovering over
// spec.subjects[0].kind on the TinyNested fixture returns a hover body
// that contains the enum summary (Group | User), confirming that Tasks 1–2
// fix the ownedHover path automatically (it shares schemaAtPointer).
func TestNested_Hover_ArrayItemEnum(t *testing.T) {
	s := newSessionNested(t)
	defer s.close(t)
	initSessionNested(t, s)

	// YAML with subjects[0].kind on line 4 (0-based).
	// 0-based lines:
	//   0: apiVersion: certmanager.io/v1
	//   1: kind: TinyNested
	//   2: spec:
	//   3:   subjects:
	//   4:     - kind: User
	yamlDoc := "apiVersion: certmanager.io/v1\n" +
		"kind: TinyNested\n" +
		"spec:\n" +
		"  subjects:\n" +
		"    - kind: User\n"

	docURI := "file:///workspace/hover_arr_enum.yaml"
	s.send(t, makeDidOpenReq(docURI, yamlDoc, 1))
	waitForDiagnostics(t, s, docURI)

	// Hover over "kind" key inside subjects[0] at LSP (line=4, char=6).
	// "kind" is at char 6 (0-based) inside the list item.
	s.send(t, makeHoverReq(400, docURI, 4, 6))
	resp := s.readFrame(t)

	if resp["error"] != nil {
		t.Fatalf("unexpected hover error: %v", resp["error"])
	}

	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("hover result not a map (got nil or wrong type): %T %v", resp["result"], resp["result"])
	}

	contents, ok := result["contents"].(map[string]any)
	if !ok {
		t.Fatalf("hover contents not a map: %v", result["contents"])
	}

	value, _ := contents["value"].(string)
	if value == "" {
		t.Fatal("hover contents.value is empty; want enum summary")
	}
	// The hover body should mention the enum values from the subjects[0].kind schema.
	if !strings.Contains(value, "Group") || !strings.Contains(value, "User") {
		t.Errorf("hover value does not contain enum members 'Group' and 'User';\ngot:\n%s", value)
	}

	s.send(t, makeShutdownReq(401))
	s.readFrame(t)
	s.send(t, makeExitNotif())
	s.waitDone(t, 3*time.Second)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	for n > 0 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[pos:])
}

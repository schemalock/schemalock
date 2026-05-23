package lsp_test

// Tests for the schemalock/resolveSchema LSP method.
//
// Five cases per the plan's Milestone 4:
//   - happy_path_cached  — schema already in cache; no fetch attempted
//   - happy_path_cold    — cache empty; mock registry returns valid bytes
//   - no_match           — apiVersion+kind not in lockfile
//   - fetch_failed       — cache empty; mock registry returns HTTP error
//   - invalid_params     — malformed JSON params (array instead of object)

import (
	"bufio"
	"context"
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

	"github.com/schemalock/app/internal/cache"
	"github.com/schemalock/app/internal/lsp"
	"github.com/schemalock/app/internal/lsp/lsptest"
	"github.com/schemalock/app/internal/registry"
)

// -------------------------------------------------------------------------------
// Session builder for resolveSchema tests
// -------------------------------------------------------------------------------

// newResolveSession starts a Server session using the provided cache and
// registry. It wires in-memory pipes and starts server.Run in a goroutine.
// The caller must call sess.close(t) when done.
func newResolveSession(t *testing.T, c *cache.Cache, reg *registry.Client) *session {
	t.Helper()

	logger := log.New(io.Discard, "", 0)
	server := lsp.NewServer(lsp.Config{
		Cache:    c,
		Registry: reg,
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

	t.Cleanup(func() {
		vw.Validate()
	})

	return &session{
		server: server,
		pr:     pr,
		pw:     pw,
		or:     or,
		ow:     ow,
		br:     bufio.NewReader(or),
		errCh:  errCh,
		vw:     vw,
	}
}

// -------------------------------------------------------------------------------
// Request builders
// -------------------------------------------------------------------------------

// makeResolveSchemaReq builds a schemalock/resolveSchema request map.
func makeResolveSchemaReq(id int64, docURI, apiVersion, kind string) map[string]any {
	return map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  "schemalock/resolveSchema",
		"params": map[string]any{
			"textDocumentUri": docURI,
			"apiVersion":      apiVersion,
			"kind":            kind,
		},
	}
}

// writeRawFrame writes a raw (pre-built) byte slice as a Content-Length-framed
// message to the session pipe without any JSON marshalling.
func writeRawFrame(t *testing.T, s *session, body []byte) {
	t.Helper()
	if err := lsptest.WriteMessage(s.pw, body); err != nil {
		t.Fatalf("writeRawFrame: %v", err)
	}
}

// -------------------------------------------------------------------------------
// Assertion helpers
// -------------------------------------------------------------------------------

// assertResolveOK checks that resp has a result.schemaUri that:
//   - starts with "file://"
//   - ends with "VMCluster.json"
//   - points to a file whose SHA-256 matches wantIntegrity
func assertResolveOK(t *testing.T, resp map[string]any, wantIntegrity string) {
	t.Helper()
	if resp["error"] != nil {
		t.Fatalf("unexpected error response: %v", resp["error"])
	}
	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("result not a map: %T %v", resp["result"], resp["result"])
	}
	uri, _ := result["schemaUri"].(string)
	if uri == "" {
		t.Fatal("result.schemaUri is empty")
	}
	if !strings.HasPrefix(uri, "file://") {
		t.Errorf("schemaUri does not start with file://: %q", uri)
	}
	if !strings.HasSuffix(uri, "VMCluster.json") {
		t.Errorf("schemaUri does not end with VMCluster.json: %q", uri)
	}
	// Verify the file exists and has the expected integrity.
	path := strings.TrimPrefix(uri, "file://")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("schema file not readable at %s: %v", path, err)
	}
	gotIntegrity := registry.ComputeIntegrity(data)
	if gotIntegrity != wantIntegrity {
		t.Errorf("on-disk schema integrity = %s, want %s", gotIntegrity, wantIntegrity)
	}
}

// assertResolveErr checks that resp carries an error with the expected code.
func assertResolveErr(t *testing.T, resp map[string]any, wantCode float64) {
	t.Helper()
	errObj, ok := resp["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected error response (code %v), got result: %v", wantCode, resp)
	}
	code, _ := errObj["code"].(float64)
	if code != wantCode {
		t.Errorf("error code = %v, want %v; full error: %v", code, wantCode, errObj)
	}
}

// -------------------------------------------------------------------------------
// Tests
// -------------------------------------------------------------------------------

// TestResolveSchema_HappyCached verifies that when the schema is already in
// the cache the handler returns the correct file:// URI and does not contact
// the CDN.
func TestResolveSchema_HappyCached(t *testing.T) {
	workspaceDir, err := filepath.Abs("testdata/workspace")
	if err != nil {
		t.Fatal(err)
	}

	schemaIntegrity := registry.ComputeIntegrity(schemaBytes)

	// Registry that fails the test if contacted.
	neverFetch := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("happy_cached: unexpected CDN request to %s", r.URL.Path)
		http.Error(w, "should not be fetched", http.StatusInternalServerError)
	}))
	t.Cleanup(neverFetch.Close)

	cacheRoot := t.TempDir()
	c := cache.New(cacheRoot)
	if err := c.WriteSchema(
		"kubernetes", "operator.victoriametrics.com", "0.70.0", "VMCluster",
		schemaIntegrity, schemaBytes,
	); err != nil {
		t.Fatalf("pre-populate cache: %v", err)
	}

	s := newResolveSession(t, c, registry.NewClient(neverFetch.URL))
	defer s.close(t)

	initSession(t, s, workspaceDir)

	s.send(t, makeResolveSchemaReq(100, "file:///ws/test.yaml", "operator.victoriametrics.com/v1beta1", "VMCluster"))
	resp := s.readFrame(t)

	assertResolveOK(t, resp, schemaIntegrity)

	s.send(t, makeShutdownReq(101))
	s.readFrame(t)
	s.send(t, makeExitNotif())
	s.waitDone(t, 3*time.Second)
}

// TestResolveSchema_HappyCold verifies that when the cache is empty the handler
// fetches from the CDN, writes the schema to cache, and returns the file:// URI.
func TestResolveSchema_HappyCold(t *testing.T) {
	workspaceDir, err := filepath.Abs("testdata/workspace")
	if err != nil {
		t.Fatal(err)
	}

	schemaIntegrity := registry.ComputeIntegrity(schemaBytes)

	fetchSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/VMCluster.json") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(schemaBytes)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(fetchSrv.Close)

	cacheRoot := t.TempDir()
	c := cache.New(cacheRoot)
	s := newResolveSession(t, c, registry.NewClient(fetchSrv.URL))
	defer s.close(t)

	initSession(t, s, workspaceDir)

	s.send(t, makeResolveSchemaReq(200, "file:///ws/test.yaml", "operator.victoriametrics.com/v1beta1", "VMCluster"))
	resp := s.readFrame(t)

	assertResolveOK(t, resp, schemaIntegrity)

	s.send(t, makeShutdownReq(201))
	s.readFrame(t)
	s.send(t, makeExitNotif())
	s.waitDone(t, 3*time.Second)
}

// TestResolveSchema_NoMatch verifies that requesting an (apiVersion, kind) pair
// not present in the lockfile returns an error with code -32001 (CodeNoMatch).
func TestResolveSchema_NoMatch(t *testing.T) {
	workspaceDir, err := filepath.Abs("testdata/workspace")
	if err != nil {
		t.Fatal(err)
	}

	// Registry is irrelevant for this case but must be valid.
	dummy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	t.Cleanup(dummy.Close)

	cacheRoot := t.TempDir()
	c := cache.New(cacheRoot)
	s := newResolveSession(t, c, registry.NewClient(dummy.URL))
	defer s.close(t)

	initSession(t, s, workspaceDir)

	// Request a kind that is not in testdata/workspace/schemalock.lock.
	s.send(t, makeResolveSchemaReq(300, "file:///ws/test.yaml", "unknown.example.com/v1", "NoSuchKind"))
	resp := s.readFrame(t)

	assertResolveErr(t, resp, -32001)

	s.send(t, makeShutdownReq(301))
	s.readFrame(t)
	s.send(t, makeExitNotif())
	s.waitDone(t, 3*time.Second)
}

// TestResolveSchema_FetchFailed verifies that when the cache is empty and the
// CDN returns an HTTP error, the handler returns code -32003 (CodeFetchFailed)
// and leaves the cache empty.
func TestResolveSchema_FetchFailed(t *testing.T) {
	workspaceDir, err := filepath.Abs("testdata/workspace")
	if err != nil {
		t.Fatal(err)
	}

	errorSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}))
	t.Cleanup(errorSrv.Close)

	cacheRoot := t.TempDir()
	c := cache.New(cacheRoot)
	s := newResolveSession(t, c, registry.NewClient(errorSrv.URL))
	defer s.close(t)

	initSession(t, s, workspaceDir)

	s.send(t, makeResolveSchemaReq(400, "file:///ws/test.yaml", "operator.victoriametrics.com/v1beta1", "VMCluster"))
	resp := s.readFrame(t)

	assertResolveErr(t, resp, -32003)

	// Verify the cache is still empty — WriteSchema must not have been called.
	path := c.SchemaPath("kubernetes", "operator.victoriametrics.com", "0.70.0", "VMCluster")
	if _, statErr := os.Stat(path); statErr == nil {
		t.Error("expected cache to be empty after fetch failure, but schema file exists")
	}

	s.send(t, makeShutdownReq(401))
	s.readFrame(t)
	s.send(t, makeExitNotif())
	s.waitDone(t, 3*time.Second)
}

// TestResolveSchema_InvalidParams verifies that sending malformed JSON params
// (an array instead of an object) returns a JSON-RPC error with code -32602
// (CodeInvalidParams).
func TestResolveSchema_InvalidParams(t *testing.T) {
	workspaceDir, err := filepath.Abs("testdata/workspace")
	if err != nil {
		t.Fatal(err)
	}

	dummy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	t.Cleanup(dummy.Close)

	cacheRoot := t.TempDir()
	c := cache.New(cacheRoot)
	s := newResolveSession(t, c, registry.NewClient(dummy.URL))
	defer s.close(t)

	initSession(t, s, workspaceDir)

	// Send params as a JSON array — cannot be unmarshalled into ResolveSchemaParams.
	malformed := fmt.Appendf(nil,
		`{"jsonrpc":"2.0","id":500,"method":"schemalock/resolveSchema","params":%s}`,
		`[1,2,3]`,
	)
	writeRawFrame(t, s, malformed)
	resp := s.readFrame(t)

	assertResolveErr(t, resp, -32602)

	s.send(t, makeShutdownReq(501))
	s.readFrame(t)
	s.send(t, makeExitNotif())
	s.waitDone(t, 3*time.Second)
}

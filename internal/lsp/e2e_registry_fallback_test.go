package lsp

// e2e_registry_fallback_test.go — end-to-end test for the registry-fallback
// resolution lifecycle. Drives a Server through DidOpen, version-override set,
// version-override clear, listVersionsForGroup, and getDocumentState, asserting
// the full State-transition sequence surfaced via schemalock/documentStateChanged.
//
// This test lives in package lsp (not lsp_test) so it can access unexported
// Server fields and reuse helpers from cdn_resolver_test.go (integrityOf, sizeStr).

import (
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	lspprot "go.lsp.dev/protocol"

	"github.com/schemalock/app/internal/cache"
	"github.com/schemalock/app/internal/lsp/protocol"
	"github.com/schemalock/app/internal/registry"
	"github.com/schemalock/app/internal/validator"
)

// newTestServerWithCDN constructs a Server wired with a real cdnResolver
// pointing at cdnBaseURL and fallback enabled/disabled as specified. It does
// NOT start Run() — callers invoke handler methods directly.
//
// The returned server has:
//   - cdnResolver pointing at cdnBaseURL (TTLs: 5m pos / 24h neg / 30s err)
//   - a real OverrideStore
//   - a router built from newResolverChain(nil lockfile, overrides, cdn, fallbackEnabled, cacheDir)
//   - a no-op WorkerPool so enqueueValidation does not panic
//   - notifyClient wired to a caller-supplied function (replace after construction)
//   - a no-op logger
func newTestServerWithCDN(t *testing.T, cdnBaseURL string, fallbackEnabled bool) *Server {
	t.Helper()

	cacheDir := t.TempDir()
	c := cache.New(cacheDir)
	regClient := registry.NewClient(cdnBaseURL)
	logger := log.New(io.Discard, "", 0)

	cdn := newCDNResolver(regClient, 5*time.Minute, 24*time.Hour, 30*time.Second)
	cdn.cacheDir = cacheDir

	ov := NewOverrideStore()
	chain := newResolverChain(nil /* no lockfile */, ov, cdn, fallbackEnabled, cacheDir)
	router := NewRouter(chain)

	srv := &Server{
		log:         logger,
		docs:        newDocumentStore(),
		cacheDir:    c,
		reg:         regClient,
		compiler:    validator.NewCompiler(),
		cancelFuncs: make(map[string]context.CancelFunc),
		cdnResolver: cdn,
		overrides:   ov,
		router:      router,
		fallback:    fallbackEnabled,
	}

	// Wire a no-op worker pool so enqueueValidation does not panic.
	srv.workers = NewWorkerPool(1, WorkerDeps{
		Cache:    c,
		Registry: regClient,
		Compiler: srv.compiler,
		Publish:  func(context.Context, string, uint32, []lspprot.Diagnostic) {},
	})
	t.Cleanup(srv.workers.Stop)

	return srv
}

// waitNotifyE2E drains one notification from ch within d, failing the test on timeout.
func waitNotifyE2E(t *testing.T, ch <-chan protocol.DocumentStateChangedParams, d time.Duration) protocol.DocumentStateChangedParams {
	t.Helper()
	select {
	case p := <-ch:
		return p
	case <-time.After(d):
		t.Fatal("timed out waiting for schemalock/documentStateChanged notification")
		return protocol.DocumentStateChangedParams{}
	}
}

// TestRegistryFallback_EndToEnd drives a Server through all five lifecycle
// transitions of the registry-fallback flow and verifies that each step emits
// the expected state via schemalock/documentStateChanged:
//
//  1. DidOpen → StateUnpinned "0.70.0" (CDN fallback resolves latest version)
//  2. setDocumentVersionOverride to "0.68.4" → StateError (version not on CDN)
//  3. setDocumentVersionOverride to "" → StateUnpinned (override cleared)
//  4. listVersionsForGroup → Indexed=true, Versions=["0.70.0"]
//  5. getDocumentState → State=Unpinned, Group/Kind/Version correct
func TestRegistryFallback_EndToEnd(t *testing.T) {
	schemaBody := `{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","required":["apiVersion","kind"]}`

	// Fake CDN that serves only 0.70.0 for operator.victoriametrics.com.
	cdnSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/kubernetes/operator.victoriametrics.com/versions.json":
			_, _ = w.Write([]byte(`["0.70.0"]`))
		case "/kubernetes/operator.victoriametrics.com/0.70.0/manifest.json":
			body := `{"version":1,"kinds":{"VMCluster":{"integrity":"` + integrityOf(schemaBody) + `","size":` + sizeStr(schemaBody) + `}}}`
			_, _ = w.Write([]byte(body))
		case "/kubernetes/operator.victoriametrics.com/0.70.0/VMCluster.json":
			_, _ = w.Write([]byte(schemaBody))
		default:
			http.NotFound(w, r)
		}
	}))
	defer cdnSrv.Close()

	// Channel to receive documentStateChanged notifications.
	notified := make(chan protocol.DocumentStateChangedParams, 8)

	s := newTestServerWithCDN(t, cdnSrv.URL, true)
	s.notifyClient = func(_ context.Context, m string, p any) error {
		if m == protocol.MethodDocumentStateChanged {
			notified <- p.(protocol.DocumentStateChangedParams)
		}
		return nil
	}

	uri := "file:///vmcluster.yaml"
	text := "apiVersion: operator.victoriametrics.com/v1beta1\nkind: VMCluster\n"
	ctx := context.Background()

	// --- 1. DidOpen → Unpinned 0.70.0 ---
	if err := s.DidOpen(ctx, &lspprot.DidOpenTextDocumentParams{
		TextDocument: lspprot.TextDocumentItem{
			URI:        lspprot.DocumentURI(uri),
			LanguageID: "yaml",
			Version:    1,
			Text:       text,
		},
	}); err != nil {
		t.Fatalf("DidOpen: %v", err)
	}
	got := waitNotifyE2E(t, notified, 5*time.Second)
	if got.State != protocol.StateUnpinned || got.Version != "0.70.0" {
		t.Fatalf("step 1 (DidOpen): got State=%v Version=%q, want StateUnpinned 0.70.0", got.State, got.Version)
	}

	// --- 2. Override to missing version → StateError ---
	if _, err := s.handleSetDocumentVersionOverride(ctx, protocol.SetDocumentVersionOverrideParams{
		URI:     uri,
		Version: "0.68.4",
	}); err != nil {
		t.Fatalf("handleSetDocumentVersionOverride(0.68.4): %v", err)
	}
	got = waitNotifyE2E(t, notified, 5*time.Second)
	if got.State != protocol.StateError {
		t.Fatalf("step 2 (bad override): got State=%v, want StateError", got.State)
	}

	// --- 3. Clear override → back to Unpinned ---
	if _, err := s.handleSetDocumentVersionOverride(ctx, protocol.SetDocumentVersionOverrideParams{
		URI:     uri,
		Version: "",
	}); err != nil {
		t.Fatalf("handleSetDocumentVersionOverride(clear): %v", err)
	}
	got = waitNotifyE2E(t, notified, 5*time.Second)
	if got.State != protocol.StateUnpinned {
		t.Fatalf("step 3 (clear override): got State=%v, want StateUnpinned", got.State)
	}

	// --- 4. listVersionsForGroup ---
	vRes, err := s.handleListVersionsForGroup(ctx, protocol.ListVersionsForGroupParams{
		Group: "operator.victoriametrics.com",
	})
	if err != nil {
		t.Fatalf("handleListVersionsForGroup: %v", err)
	}
	if !vRes.Indexed {
		t.Fatalf("step 4 (listVersions): Indexed=false, want true")
	}
	if len(vRes.Versions) != 1 || vRes.Versions[0] != "0.70.0" {
		t.Fatalf("step 4 (listVersions): Versions=%v, want [0.70.0]", vRes.Versions)
	}

	// --- 5. getDocumentState ---
	gRes, err := s.handleGetDocumentState(ctx, protocol.GetDocumentStateParams{URI: uri})
	if err != nil {
		t.Fatalf("handleGetDocumentState: %v", err)
	}
	if gRes.State != protocol.StateUnpinned {
		t.Fatalf("step 5 (getDocumentState): State=%v, want StateUnpinned", gRes.State)
	}
	if gRes.Group != "operator.victoriametrics.com" {
		t.Fatalf("step 5 (getDocumentState): Group=%q, want operator.victoriametrics.com", gRes.Group)
	}
	if gRes.Kind != "VMCluster" {
		t.Fatalf("step 5 (getDocumentState): Kind=%q, want VMCluster", gRes.Kind)
	}
	if gRes.Version != "0.70.0" {
		t.Fatalf("step 5 (getDocumentState): Version=%q, want 0.70.0", gRes.Version)
	}
}

// TestRegistryFallback_CompletionHoverDiagnostics verifies that CDN-fallback
// (Unpinned) documents receive schema-driven completion items, hover contents,
// and validation diagnostics. This tests the bug fix from Task 2.3: the
// resolverChain's res.Schema bytes must reach the validation worker and the
// ownedCompletion/ownedHover helpers, not just the lifecycle state notification.
//
// The fake CDN serves a richer schema with a known property ("replicas") and a
// required field ("spec") so we can assert:
//   - Completion returns "replicas" as a property suggestion.
//   - Hover on "replicas" returns non-empty markdown.
//   - A document missing "spec" produces at least one diagnostic.
func TestRegistryFallback_CompletionHoverDiagnostics(t *testing.T) {
	// Schema with a known property "replicas" (integer) and required "spec".
	// Using draft 2020-12 as required by the validator Compiler.
	schemaBody := `{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"type": "object",
		"description": "VMCluster is a Victoria Metrics cluster.",
		"required": ["apiVersion", "kind", "spec"],
		"properties": {
			"apiVersion": {"type": "string"},
			"kind":       {"type": "string"},
			"replicas":   {"type": "integer", "description": "Number of replicas."},
			"spec":       {"type": "object", "description": "Spec defines desired state."}
		}
	}`

	// Fake CDN serving the richer schema at 0.70.0.
	cdnSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/kubernetes/operator.victoriametrics.com/versions.json":
			_, _ = w.Write([]byte(`["0.70.0"]`))
		case "/kubernetes/operator.victoriametrics.com/0.70.0/manifest.json":
			body := `{"version":1,"kinds":{"VMCluster":{"integrity":"` + integrityOf(schemaBody) + `","size":` + sizeStr(schemaBody) + `}}}`
			_, _ = w.Write([]byte(body))
		case "/kubernetes/operator.victoriametrics.com/0.70.0/VMCluster.json":
			_, _ = w.Write([]byte(schemaBody))
		default:
			http.NotFound(w, r)
		}
	}))
	defer cdnSrv.Close()

	// Build the server directly (not via newTestServerWithCDN) so we control
	// the worker pool's Publish function and can capture diagnostics.
	// This avoids the double-Stop problem that arises when replacing the pool.
	diagCh := make(chan []lspprot.Diagnostic, 8)

	cacheDir := t.TempDir()
	c := cache.New(cacheDir)
	regClient := registry.NewClient(cdnSrv.URL)
	comp := validator.NewCompiler()
	logger := log.New(io.Discard, "", 0)

	cdn := newCDNResolver(regClient, 5*time.Minute, 24*time.Hour, 30*time.Second)
	cdn.cacheDir = cacheDir

	ov := NewOverrideStore()
	chain := newResolverChain(nil /* no lockfile */, ov, cdn, true, cacheDir)
	router := NewRouter(chain)

	s := &Server{
		log:         logger,
		docs:        newDocumentStore(),
		cacheDir:    c,
		reg:         regClient,
		compiler:    comp,
		cancelFuncs: make(map[string]context.CancelFunc),
		cdnResolver: cdn,
		overrides:   ov,
		router:      router,
		fallback:    true,
	}
	s.workers = NewWorkerPool(1, WorkerDeps{
		Cache:    c,
		Registry: regClient,
		Compiler: comp,
		Publish: func(_ context.Context, _ string, _ uint32, diags []lspprot.Diagnostic) {
			diagCh <- diags
		},
	})
	t.Cleanup(s.workers.Stop)

	uri := "file:///vmcluster.yaml"
	// Document intentionally missing "spec" so validation fires at least one diagnostic.
	// It is also valid YAML with a root-level key "replicas" for hover/completion.
	text := "apiVersion: operator.victoriametrics.com/v1beta1\nkind: VMCluster\nreplicas: 3\n"
	ctx := context.Background()

	// --- DidOpen: resolve via CDN, enqueue validation with res.Schema ---
	if err := s.DidOpen(ctx, &lspprot.DidOpenTextDocumentParams{
		TextDocument: lspprot.TextDocumentItem{
			URI:        lspprot.DocumentURI(uri),
			LanguageID: "yaml",
			Version:    1,
			Text:       text,
		},
	}); err != nil {
		t.Fatalf("DidOpen: %v", err)
	}

	// Wait for diagnostics from the validation worker (missing "spec" → at
	// least one required-field diagnostic).
	select {
	case diags := <-diagCh:
		// Diagnostics may be empty if the schema doesn't flag missing spec at
		// the root level — but with required:["spec"] it should produce one.
		// Accept either: non-zero diags means validation ran; zero means the
		// schema compiled but no violations found (acceptable). What we must NOT
		// see is a permanent hang (ErrNoMatch silent skip).
		t.Logf("diagnostics received: %d", len(diags))
		// We expect at least one diagnostic for the missing required "spec" field.
		if len(diags) == 0 {
			t.Error("expected at least one diagnostic for missing required field 'spec', got 0")
		} else {
			found := false
			for _, d := range diags {
				if strings.Contains(d.Message, "spec") || strings.Contains(d.Message, "required") {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("expected a 'spec'/'required' diagnostic, got: %v", diags)
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for validation diagnostics for Unpinned document")
	}

	// --- Completion: cursor at root key position should suggest schema properties ---
	// Position the cursor on a new key line after "replicas: 3\n" (line 3, char 0).
	completionResult, err := s.Completion(ctx, &lspprot.CompletionParams{
		TextDocumentPositionParams: lspprot.TextDocumentPositionParams{
			TextDocument: lspprot.TextDocumentIdentifier{URI: lspprot.DocumentURI(uri)},
			Position:     lspprot.Position{Line: 3, Character: 0},
		},
	})
	if err != nil {
		t.Fatalf("Completion: %v", err)
	}
	if completionResult == nil {
		t.Fatal("Completion: got nil result")
	}
	if len(completionResult.Items) == 0 {
		t.Error("Completion: expected non-empty Items for Unpinned CDN document, got 0")
	} else {
		// Verify at least one known property appears.
		found := false
		for _, item := range completionResult.Items {
			if item.Label == "replicas" || item.Label == "spec" {
				found = true
				break
			}
		}
		if !found {
			labels := make([]string, 0, len(completionResult.Items))
			for _, item := range completionResult.Items {
				labels = append(labels, item.Label)
			}
			t.Errorf("Completion: 'replicas' or 'spec' not in items %v", labels)
		}
	}

	// --- Hover: hover over "replicas" key (line 2, char 0) should return markdown ---
	// Line 2 (0-based) is "replicas: 3".
	hoverResult, err := s.Hover(ctx, &lspprot.HoverParams{
		TextDocumentPositionParams: lspprot.TextDocumentPositionParams{
			TextDocument: lspprot.TextDocumentIdentifier{URI: lspprot.DocumentURI(uri)},
			Position:     lspprot.Position{Line: 2, Character: 0},
		},
	})
	if err != nil {
		t.Fatalf("Hover: %v", err)
	}
	if hoverResult == nil {
		t.Fatal("Hover: got nil result for Unpinned CDN document")
	}
	if hoverResult.Contents.Value == "" {
		t.Error("Hover: got empty markdown content for Unpinned CDN document")
	}
	if !strings.Contains(hoverResult.Contents.Value, "replicas") {
		t.Errorf("Hover: expected 'replicas' in hover content, got: %s", hoverResult.Contents.Value)
	}
}

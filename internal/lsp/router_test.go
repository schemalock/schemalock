package lsp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/schemalock/app/internal/intent"
	"github.com/schemalock/app/internal/registry"
)

// routerTestSchema is a minimal valid JSON Schema used for all router tests.
const routerTestSchema = `{"type":"object"}`

// setupRouterFixture builds an intent workspace + fake CDN for the router
// tests. Returns the workspaceDir containing schemalock.yaml and the
// *resolverChain backed by the intent lookup and CDN.
//
// Use ownedURIInDir(workspaceDir) to build file URIs that the intent walk can
// resolve; URIs outside workspaceDir will fall through to CDN-latest which
// requires versions.json (not served by this fixture).
func setupRouterFixture(t *testing.T) (workspaceDir string, chain *resolverChain) {
	t.Helper()
	const (
		group   = "operator.victoriametrics.com"
		version = "0.70.0"
		kind    = "VMCluster"
	)
	schemaBody := routerTestSchema

	// Write a root schemalock.yaml pinning operator.victoriametrics.com@0.70.0.
	workspaceDir = t.TempDir()
	content := "version: 1\necosystems:\n  kubernetes:\n    - " + group + "@" + version + "\n"
	if err := os.WriteFile(filepath.Join(workspaceDir, "schemalock.yaml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	// Fake CDN serving the manifest and schema for the pinned version.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/kubernetes/" + group + "/" + version + "/manifest.json":
			body := `{"version":1,"kinds":{"` + kind + `":{"integrity":"` + integrityOf(schemaBody) + `","size":` + sizeStr(schemaBody) + `}}}`
			_, _ = w.Write([]byte(body))
		case "/kubernetes/" + group + "/" + version + "/" + kind + ".json":
			_, _ = w.Write([]byte(schemaBody))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	rc := newCDNResolver(registry.NewClient(srv.URL), 5*time.Minute, 24*time.Hour, 30*time.Second)
	rc.cacheDir = t.TempDir()

	il := intent.NewLookup()
	return workspaceDir, newResolverChain(il, NewOverrideStore(), rc, true)
}

// ownedURIInDir returns a file URI inside dir so the intent walk finds
// the schemalock.yaml written by setupRouterFixture.
func ownedURIInDir(dir, name string) string {
	return "file://" + filepath.Join(dir, name)
}

const vmClusterDoc = `apiVersion: operator.victoriametrics.com/v1beta1
kind: VMCluster
metadata:
  name: test
`

const unownedDoc = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: test
`

const coreTypeDoc = `apiVersion: v1
kind: Pod
metadata:
  name: test
`

const noKindDoc = `apiVersion: operator.victoriametrics.com/v1beta1
metadata:
  name: test
`

const emptyDoc = ``

const invalidYAML = `apiVersion: [unclosed
`

// TestRouter_OwnedURI verifies that IsOwned returns true for a document whose
// apiVersion+kind resolves via the intent-pinned path.
func TestRouter_OwnedURI(t *testing.T) {
	dir, chain := setupRouterFixture(t)
	r := NewRouter(chain)
	uri := ownedURIInDir(dir, "a.yaml")
	if !r.IsOwned(context.Background(), uri, vmClusterDoc) {
		t.Error("IsOwned returned false for an owned document (intent-pinned group)")
	}
}

// TestRouter_UnownedURI verifies that IsOwned returns false when the
// apiVersion+kind is not present in the CDN. Uses file:///a.yaml (outside the
// workspace) to exercise the CDN-fallback path; apps/v1 is not indexed so
// StateUnindexable is returned.
func TestRouter_UnownedURI(t *testing.T) {
	_, chain := setupRouterFixture(t)
	r := NewRouter(chain)
	if r.IsOwned(context.Background(), "file:///a.yaml", unownedDoc) {
		t.Error("IsOwned returned true for an unowned document (apps/v1 Deployment)")
	}
}

// TestRouter_CoreType verifies that core Kubernetes types (no "/" in apiVersion)
// are treated as unowned because the resolver does not support them.
func TestRouter_CoreType(t *testing.T) {
	dir, chain := setupRouterFixture(t)
	r := NewRouter(chain)
	if r.IsOwned(context.Background(), ownedURIInDir(dir, "a.yaml"), coreTypeDoc) {
		t.Error("IsOwned returned true for a core type (v1/Pod)")
	}
}

// TestRouter_InvalidYAML verifies that malformed YAML does not panic and
// returns false.
func TestRouter_InvalidYAML(t *testing.T) {
	dir, chain := setupRouterFixture(t)
	r := NewRouter(chain)
	if r.IsOwned(context.Background(), ownedURIInDir(dir, "a.yaml"), invalidYAML) {
		t.Error("IsOwned returned true for invalid YAML")
	}
}

// TestRouter_EmptyDocument verifies that an empty document returns false.
func TestRouter_EmptyDocument(t *testing.T) {
	dir, chain := setupRouterFixture(t)
	r := NewRouter(chain)
	if r.IsOwned(context.Background(), ownedURIInDir(dir, "a.yaml"), emptyDoc) {
		t.Error("IsOwned returned true for an empty document")
	}
}

// TestRouter_NoKind verifies that a document with only apiVersion (no kind)
// returns false.
func TestRouter_NoKind(t *testing.T) {
	dir, chain := setupRouterFixture(t)
	r := NewRouter(chain)
	if r.IsOwned(context.Background(), ownedURIInDir(dir, "a.yaml"), noKindDoc) {
		t.Error("IsOwned returned true for a document with apiVersion but no kind")
	}
}

// TestRouter_NilChain verifies that a Router with a nil chain always
// returns false.
func TestRouter_NilChain(t *testing.T) {
	r := NewRouter(nil)
	if r.IsOwned(context.Background(), "file:///a.yaml", vmClusterDoc) {
		t.Error("IsOwned returned true when chain is nil")
	}
}

// TestRouter_CacheStability verifies that repeated IsOwned calls for the same
// URI return a consistent result without re-parsing.
func TestRouter_CacheStability(t *testing.T) {
	dir, chain := setupRouterFixture(t)
	r := NewRouter(chain)
	uri := ownedURIInDir(dir, "cache.yaml")
	first := r.IsOwned(context.Background(), uri, vmClusterDoc)
	second := r.IsOwned(context.Background(), uri, vmClusterDoc)
	if first != second {
		t.Errorf("IsOwned: first=%v second=%v, expected stable result", first, second)
	}
	if !first {
		t.Error("IsOwned expected true for owned document")
	}
}

// TestRouter_Invalidate verifies that Invalidate clears the cached decision so
// that the next call re-evaluates from the provided text.
func TestRouter_Invalidate(t *testing.T) {
	dir, chain := setupRouterFixture(t)
	r := NewRouter(chain)
	uri := ownedURIInDir(dir, "inv.yaml")

	// Populate cache for owned document.
	if !r.IsOwned(context.Background(), uri, vmClusterDoc) {
		t.Fatal("pre-condition: expected owned")
	}

	// Invalidate, then call with unowned text — should pick up new text.
	r.Invalidate(uri)
	if r.IsOwned(context.Background(), uri, unownedDoc) {
		t.Error("after Invalidate, IsOwned should return false for unowned text")
	}
}

// TestRouter_InvalidateAll verifies that InvalidateAll clears the entire cache.
func TestRouter_InvalidateAll(t *testing.T) {
	dir, chain := setupRouterFixture(t)
	r := NewRouter(chain)
	uriA := ownedURIInDir(dir, "a.yaml")
	uriB := ownedURIInDir(dir, "b.yaml")

	// Populate cache for both URIs.
	r.IsOwned(context.Background(), uriA, vmClusterDoc)
	r.IsOwned(context.Background(), uriB, unownedDoc)

	// Clear all.
	r.InvalidateAll()

	// After clearing, re-evaluation with swapped text should reflect new state.
	// uriA was owned; now pass unowned text → should be false.
	if r.IsOwned(context.Background(), uriA, unownedDoc) {
		t.Error("after InvalidateAll, uriA with unowned text should return false")
	}
	// uriB was unowned; now pass owned text → should be true.
	if !r.IsOwned(context.Background(), uriB, vmClusterDoc) {
		t.Error("after InvalidateAll, uriB with owned text should return true")
	}
}

// TestRouter_Concurrent runs IsOwned and Invalidate concurrently on the same
// URI to verify race-freedom under -race.
func TestRouter_Concurrent(t *testing.T) {
	dir, chain := setupRouterFixture(t)
	r := NewRouter(chain)
	uri := ownedURIInDir(dir, "race.yaml")

	const goroutines = 20
	var wg sync.WaitGroup
	wg.Add(goroutines * 2)

	// goroutines calling IsOwned
	for range goroutines {
		go func() {
			defer wg.Done()
			_ = r.IsOwned(context.Background(), uri, vmClusterDoc)
		}()
	}

	// goroutines calling Invalidate and InvalidateAll
	for i := range goroutines {
		go func() {
			defer wg.Done()
			if i%2 == 0 {
				r.Invalidate(uri)
			} else {
				r.InvalidateAll()
			}
		}()
	}

	wg.Wait()
}

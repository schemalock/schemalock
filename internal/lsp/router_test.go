package lsp

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/schemalock/app/internal/lockfile"
)

// routerTestSchema is a minimal valid JSON Schema used for all router tests.
const routerTestSchema = `{"type":"object"}`

// setupRouterFixture builds a LockFile + on-disk schema cache for the router
// tests. Returns the *resolverChain backed by the lockfile and disk cache.
//
// The schema is written to disk so the chain's Pinned path succeeds without a
// CDN fetch (cdn is nil). cdn=nil, fallbackEnabled=false → lockfile-only
// behavior identical to the old Router.
func setupRouterFixture(t *testing.T) *resolverChain {
	t.Helper()
	const (
		group   = "operator.victoriametrics.com"
		version = "0.70.0"
		kind    = "VMCluster"
	)
	schemaBody := routerTestSchema
	integ := integrityOf(schemaBody)
	cacheDir := t.TempDir()

	lf := lockfile.LockFile{
		Version:     1,
		Registry:    "https://cdn.schemalock.dev",
		GeneratedAt: "2026-01-01T00:00:00Z",
		Entries: []lockfile.LockEntry{
			{
				ID:   "kubernetes/" + group + "@" + version,
				Base: "kubernetes/" + group + "/" + version + "/",
				Schemas: map[string]lockfile.SchemaEntry{
					kind: {Integrity: integ, Size: len(schemaBody)},
				},
			},
		},
	}

	// Write schema bytes to disk cache so the Pinned path succeeds.
	path := filepath.Join(cacheDir, "kubernetes", group, version, kind+".json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(schemaBody), 0o644); err != nil {
		t.Fatal(err)
	}

	sr, err := NewSchemaResolver(lf)
	if err != nil {
		t.Fatalf("NewSchemaResolver: %v", err)
	}
	return newResolverChain(sr, NewOverrideStore(), nil, false, cacheDir)
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
// apiVersion+kind resolves to a lock entry.
func TestRouter_OwnedURI(t *testing.T) {
	r := NewRouter(setupRouterFixture(t))
	if !r.IsOwned(context.Background(), "file:///a.yaml", vmClusterDoc) {
		t.Error("IsOwned returned false for an owned document")
	}
}

// TestRouter_UnownedURI verifies that IsOwned returns false when the
// apiVersion+kind is not present in the lock file.
func TestRouter_UnownedURI(t *testing.T) {
	r := NewRouter(setupRouterFixture(t))
	if r.IsOwned(context.Background(), "file:///a.yaml", unownedDoc) {
		t.Error("IsOwned returned true for an unowned document (apps/v1 Deployment)")
	}
}

// TestRouter_CoreType verifies that core Kubernetes types (no "/" in apiVersion)
// are treated as unowned because the resolver does not support them.
func TestRouter_CoreType(t *testing.T) {
	r := NewRouter(setupRouterFixture(t))
	if r.IsOwned(context.Background(), "file:///a.yaml", coreTypeDoc) {
		t.Error("IsOwned returned true for a core type (v1/Pod)")
	}
}

// TestRouter_InvalidYAML verifies that malformed YAML does not panic and
// returns false.
func TestRouter_InvalidYAML(t *testing.T) {
	r := NewRouter(setupRouterFixture(t))
	if r.IsOwned(context.Background(), "file:///a.yaml", invalidYAML) {
		t.Error("IsOwned returned true for invalid YAML")
	}
}

// TestRouter_EmptyDocument verifies that an empty document returns false.
func TestRouter_EmptyDocument(t *testing.T) {
	r := NewRouter(setupRouterFixture(t))
	if r.IsOwned(context.Background(), "file:///a.yaml", emptyDoc) {
		t.Error("IsOwned returned true for an empty document")
	}
}

// TestRouter_NoKind verifies that a document with only apiVersion (no kind)
// returns false.
func TestRouter_NoKind(t *testing.T) {
	r := NewRouter(setupRouterFixture(t))
	if r.IsOwned(context.Background(), "file:///a.yaml", noKindDoc) {
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
	r := NewRouter(setupRouterFixture(t))
	uri := "file:///cache.yaml"
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
	r := NewRouter(setupRouterFixture(t))
	uri := "file:///inv.yaml"

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
	r := NewRouter(setupRouterFixture(t))
	uriA := "file:///a.yaml"
	uriB := "file:///b.yaml"

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
	r := NewRouter(setupRouterFixture(t))
	uri := "file:///race.yaml"

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

package lsp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/schemalock/app/internal/lockfile"
	"github.com/schemalock/app/internal/registry"
)

// setupLockfileFixture builds a lockfile + on-disk schema cache for the
// Pinned tests. The ID format must satisfy lockfile.ResolveID:
// "ecosystem/group@version".
func setupLockfileFixture(t *testing.T, group, version, kind, schemaBody string) (lockfile.LockFile, string) {
	t.Helper()
	cacheDir := t.TempDir()
	integ := integrityOf(schemaBody)

	lf := lockfile.LockFile{
		Version: 1,
		Entries: []lockfile.LockEntry{{
			ID:   "kubernetes/" + group + "@" + version,
			Base: "kubernetes/" + group + "/" + version + "/",
			Schemas: map[string]lockfile.SchemaEntry{
				kind: {Integrity: integ, Size: len(schemaBody)},
			},
		}},
	}

	// Write schema bytes to disk cache so Pinned hits without a fetch.
	path := filepath.Join(cacheDir, "kubernetes", group, version, kind+".json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(schemaBody), 0o644); err != nil {
		t.Fatal(err)
	}
	return lf, cacheDir
}

func TestResolve_Pinned(t *testing.T) {
	schema := `{"type":"object"}`
	lf, cacheDir := setupLockfileFixture(t, "operator.victoriametrics.com", "0.68.4", "VMCluster", schema)
	sr, err := NewSchemaResolver(lf)
	if err != nil {
		t.Fatal(err)
	}
	chain := newResolverChain(sr, NewOverrideStore(), nil, true, cacheDir)
	got := chain.Resolve(
		context.Background(),
		"file:///vmcluster.yaml",
		"apiVersion: operator.victoriametrics.com/v1beta1\nkind: VMCluster\n",
	)
	if got.State != StatePinned {
		t.Fatalf("State = %v, want StatePinned", got.State)
	}
	if got.Group != "operator.victoriametrics.com" {
		t.Fatalf("Group = %q, want operator.victoriametrics.com", got.Group)
	}
	if got.Kind != "VMCluster" {
		t.Fatalf("Kind = %q, want VMCluster", got.Kind)
	}
	if got.Version != "0.68.4" {
		t.Fatalf("Version = %q, want 0.68.4", got.Version)
	}
	if string(got.Schema) != schema {
		t.Fatalf("Schema body mismatch")
	}
}

func TestResolve_OverrideBeatsCDN(t *testing.T) {
	body684 := `{}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/kubernetes/operator.victoriametrics.com/versions.json":
			_, _ = w.Write([]byte(`["0.70.0"]`))
		case "/kubernetes/operator.victoriametrics.com/0.68.4/manifest.json":
			_, _ = w.Write([]byte(`{"version":1,"kinds":{"VMCluster":{"integrity":"` + integrityOf(body684) + `","size":` + sizeStr(body684) + `}}}`))
		case "/kubernetes/operator.victoriametrics.com/0.68.4/VMCluster.json":
			_, _ = w.Write([]byte(body684))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	rc := newCDNResolver(registry.NewClient(srv.URL), 5*time.Minute, 24*time.Hour, 30*time.Second)
	rc.cacheDir = t.TempDir()
	ov := NewOverrideStore()
	ov.Set("file:///vmcluster.yaml", "operator.victoriametrics.com", "0.68.4")
	chain := newResolverChain(nil, ov, rc, true, rc.cacheDir)

	got := chain.Resolve(
		context.Background(),
		"file:///vmcluster.yaml",
		"apiVersion: operator.victoriametrics.com/v1beta1\nkind: VMCluster\n",
	)
	if got.State != StatePreview {
		t.Fatalf("State = %v, want StatePreview", got.State)
	}
	if got.Version != "0.68.4" {
		t.Fatalf("Version = %q, want 0.68.4", got.Version)
	}
}

func TestResolve_CDNFallback(t *testing.T) {
	schema := `{}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/kubernetes/operator.victoriametrics.com/versions.json":
			_, _ = w.Write([]byte(`["0.70.0"]`))
		case "/kubernetes/operator.victoriametrics.com/0.70.0/manifest.json":
			_, _ = w.Write([]byte(`{"version":1,"kinds":{"VMCluster":{"integrity":"` + integrityOf(schema) + `","size":` + sizeStr(schema) + `}}}`))
		case "/kubernetes/operator.victoriametrics.com/0.70.0/VMCluster.json":
			_, _ = w.Write([]byte(schema))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	rc := newCDNResolver(registry.NewClient(srv.URL), 5*time.Minute, 24*time.Hour, 30*time.Second)
	rc.cacheDir = t.TempDir()
	chain := newResolverChain(nil, NewOverrideStore(), rc, true, rc.cacheDir)

	got := chain.Resolve(
		context.Background(),
		"file:///vmcluster.yaml",
		"apiVersion: operator.victoriametrics.com/v1beta1\nkind: VMCluster\n",
	)
	if got.State != StateUnpinned {
		t.Fatalf("State = %v, want StateUnpinned", got.State)
	}
	if got.Version != "0.70.0" {
		t.Fatalf("Version = %q, want 0.70.0", got.Version)
	}
}

func TestResolve_Unindexable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	rc := newCDNResolver(registry.NewClient(srv.URL), 5*time.Minute, 24*time.Hour, 30*time.Second)
	rc.cacheDir = t.TempDir()
	chain := newResolverChain(nil, NewOverrideStore(), rc, true, rc.cacheDir)

	got := chain.Resolve(
		context.Background(),
		"file:///unknown.yaml",
		"apiVersion: unknown.example.com/v1\nkind: Foo\n",
	)
	if got.State != StateUnindexable {
		t.Fatalf("State = %v, want StateUnindexable", got.State)
	}
}

func TestResolve_FallbackDisabled(t *testing.T) {
	rc := newCDNResolver(registry.NewClient("http://no.cdn.example"), 5*time.Minute, 24*time.Hour, 30*time.Second)
	rc.cacheDir = t.TempDir()
	chain := newResolverChain(nil, NewOverrideStore(), rc, false, rc.cacheDir)

	got := chain.Resolve(
		context.Background(),
		"file:///vmcluster.yaml",
		"apiVersion: operator.victoriametrics.com/v1beta1\nkind: VMCluster\n",
	)
	if got.State != StateUnindexable {
		t.Fatalf("State = %v, want StateUnindexable (fallback disabled)", got.State)
	}
}

// TestResolve_OverrideBeatsPinned asserts that a picker-set override takes
// precedence over a lockfile pin. The user clicking "preview 0.68.4" must
// override the lockfile's 0.70.0 pin for the active session, otherwise the
// picker UI is silently no-op for any pinned document.
func TestResolve_OverrideBeatsPinned(t *testing.T) {
	const group, kind = "operator.victoriametrics.com", "VMCluster"
	const pinned, preview = "0.70.0", "0.68.4"
	const pinnedBody = `{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","title":"pinned"}`
	const previewBody = `{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","title":"preview"}`

	// Lockfile + on-disk schema for the pinned version.
	lf := lockfile.LockFile{
		Version: 1,
		Entries: []lockfile.LockEntry{{
			ID:   "kubernetes/" + group + "@" + pinned,
			Base: "kubernetes/" + group + "/" + pinned + "/",
			Schemas: map[string]lockfile.SchemaEntry{
				kind: {Integrity: integrityOf(pinnedBody), Size: len(pinnedBody)},
			},
		}},
	}
	sr, err := NewSchemaResolver(lf)
	if err != nil {
		t.Fatalf("NewSchemaResolver: %v", err)
	}

	// Fake CDN serves the preview-version manifest + schema; the picker should
	// reach it via the override branch.
	cdnSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/kubernetes/" + group + "/" + preview + "/manifest.json":
			_, _ = w.Write([]byte(`{"version":1,"kinds":{"` + kind + `":{"integrity":"` + integrityOf(previewBody) + `","size":` + sizeStr(previewBody) + `}}}`))
		case "/kubernetes/" + group + "/" + preview + "/" + kind + ".json":
			_, _ = w.Write([]byte(previewBody))
		default:
			http.NotFound(w, r)
		}
	}))
	defer cdnSrv.Close()

	cacheDir := t.TempDir()
	// Plant the pinned schema on disk so the lockfile branch is fully primed
	// (i.e. the test isn't accidentally relying on a disk miss to bypass it).
	pinnedPath := filepath.Join(cacheDir, "kubernetes", group, pinned, kind+".json")
	if err := os.MkdirAll(filepath.Dir(pinnedPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pinnedPath, []byte(pinnedBody), 0o644); err != nil {
		t.Fatal(err)
	}

	rc := newCDNResolver(registry.NewClient(cdnSrv.URL), 5*time.Minute, 24*time.Hour, 30*time.Second)
	rc.cacheDir = cacheDir
	ov := NewOverrideStore()
	ov.Set("file:///vmcluster.yaml", group, preview)
	chain := newResolverChain(sr, ov, rc, true, cacheDir)

	got := chain.Resolve(
		context.Background(),
		"file:///vmcluster.yaml",
		"apiVersion: "+group+"/v1beta1\nkind: "+kind+"\n",
	)
	if got.State != StatePreview {
		t.Fatalf("State = %v, want StatePreview (override must beat lockfile pin)", got.State)
	}
	if got.Version != preview {
		t.Fatalf("Version = %q, want %q", got.Version, preview)
	}
	if string(got.Schema) != previewBody {
		t.Fatalf("Schema mismatch: got %q, want %q", string(got.Schema), previewBody)
	}

	// Clearing the override returns to Pinned with the lockfile schema.
	ov.Clear("file:///vmcluster.yaml")
	got = chain.Resolve(
		context.Background(),
		"file:///vmcluster.yaml",
		"apiVersion: "+group+"/v1beta1\nkind: "+kind+"\n",
	)
	if got.State != StatePinned {
		t.Fatalf("after clear: State = %v, want StatePinned", got.State)
	}
	if got.Version != pinned {
		t.Fatalf("after clear: Version = %q, want %q", got.Version, pinned)
	}
}

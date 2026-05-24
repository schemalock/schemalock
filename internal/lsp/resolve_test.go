package lsp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/schemalock/app/internal/intent"
	"github.com/schemalock/app/internal/registry"
)

// writeIntentFile writes a minimal root schemalock.yaml to dir pinning group@version.
func writeIntentFile(t *testing.T, dir, group, version string) {
	t.Helper()
	content := "version: 1\necosystems:\n  kubernetes:\n    - " + group + "@" + version + "\n"
	path := filepath.Join(dir, "schemalock.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestResolverChain_intentPinned verifies that a document whose directory
// contains a schemalock.yaml pinning operator.victoriametrics.com resolves
// to StatePinned with the correct version.
func TestResolverChain_intentPinned(t *testing.T) {
	const (
		group   = "operator.victoriametrics.com"
		version = "0.70.0"
		kind    = "VMCluster"
	)
	schemaBody := `{"type":"object"}`

	// Write schemalock.yaml to a tempdir (simulates a workspace root).
	workspaceDir := t.TempDir()
	writeIntentFile(t, workspaceDir, group, version)

	// Fake CDN serving manifest + schema for the pinned version.
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
	defer srv.Close()

	rc := newCDNResolver(registry.NewClient(srv.URL), 5*time.Minute, 24*time.Hour, 30*time.Second)
	rc.cacheDir = t.TempDir()

	il := intent.NewLookup()
	chain := newResolverChain(il, NewOverrideStore(), rc, true)

	// Document URI inside the workspace dir.
	docURI := "file://" + filepath.Join(workspaceDir, "vmcluster.yaml")
	got := chain.Resolve(
		context.Background(),
		docURI,
		"apiVersion: "+group+"/v1beta1\nkind: "+kind+"\n",
	)
	if got.State != StatePinned {
		t.Fatalf("State = %v, want StatePinned", got.State)
	}
	if got.Source != "intent" {
		t.Fatalf("Source = %q, want \"intent\"", got.Source)
	}
	if got.Version != version {
		t.Fatalf("Version = %q, want %q", got.Version, version)
	}
	if got.Group != group {
		t.Fatalf("Group = %q, want %q", got.Group, group)
	}
}

// TestResolverChain_overlayWinsOverRoot verifies that a nested schemalock.yaml
// (overlay) that overrides the root's pin for a group is used for documents
// in the overlay's subtree.
func TestResolverChain_overlayWinsOverRoot(t *testing.T) {
	const (
		group       = "operator.victoriametrics.com"
		rootVersion = "0.68.0"
		leafVersion = "0.70.0"
		kind        = "VMCluster"
	)
	schemaBody := `{"type":"object"}`

	// Root dir with schemalock.yaml pinning rootVersion.
	rootDir := t.TempDir()
	writeIntentFile(t, rootDir, group, rootVersion)

	// Overlay dir nested inside root, pinning leafVersion.
	overlayDir := filepath.Join(rootDir, "subteam")
	if err := os.MkdirAll(overlayDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "ecosystems:\n  kubernetes:\n    - " + group + "@" + leafVersion + "\n"
	if err := os.WriteFile(filepath.Join(overlayDir, "schemalock.yaml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	// Fake CDN serving both versions.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, v := range []string{rootVersion, leafVersion} {
			switch r.URL.Path {
			case "/kubernetes/" + group + "/" + v + "/manifest.json":
				body := `{"version":1,"kinds":{"` + kind + `":{"integrity":"` + integrityOf(schemaBody) + `","size":` + sizeStr(schemaBody) + `}}}`
				_, _ = w.Write([]byte(body))
				return
			case "/kubernetes/" + group + "/" + v + "/" + kind + ".json":
				_, _ = w.Write([]byte(schemaBody))
				return
			}
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	rc := newCDNResolver(registry.NewClient(srv.URL), 5*time.Minute, 24*time.Hour, 30*time.Second)
	rc.cacheDir = t.TempDir()

	il := intent.NewLookup()
	chain := newResolverChain(il, NewOverrideStore(), rc, true)

	// Document URI in the overlay subtree.
	docURI := "file://" + filepath.Join(overlayDir, "vmcluster.yaml")
	got := chain.Resolve(
		context.Background(),
		docURI,
		"apiVersion: "+group+"/v1beta1\nkind: "+kind+"\n",
	)
	if got.State != StatePinned {
		t.Fatalf("State = %v, want StatePinned", got.State)
	}
	if got.Version != leafVersion {
		t.Fatalf("Version = %q, want %q (overlay must win over root)", got.Version, leafVersion)
	}
}

// TestResolverChain_unpinnedFallsThroughToCDNLatest verifies that a group
// not pinned by the intent hierarchy falls through to the CDN latest path,
// returning StateUnpinned.
func TestResolverChain_unpinnedFallsThroughToCDNLatest(t *testing.T) {
	const (
		pinnedGroup    = "operator.victoriametrics.com"
		unpinnedGroup  = "cert-manager.io"
		kind           = "Certificate"
		latestVersion  = "1.13.0"
	)
	schemaBody := `{"type":"object"}`

	// schemalock.yaml pins the VM operator only, not cert-manager.
	workspaceDir := t.TempDir()
	writeIntentFile(t, workspaceDir, pinnedGroup, "0.70.0")

	// CDN serves cert-manager at latestVersion.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/kubernetes/" + unpinnedGroup + "/versions.json":
			_, _ = w.Write([]byte(`["` + latestVersion + `"]`))
		case "/kubernetes/" + unpinnedGroup + "/" + latestVersion + "/manifest.json":
			body := `{"version":1,"kinds":{"` + kind + `":{"integrity":"` + integrityOf(schemaBody) + `","size":` + sizeStr(schemaBody) + `}}}`
			_, _ = w.Write([]byte(body))
		case "/kubernetes/" + unpinnedGroup + "/" + latestVersion + "/" + kind + ".json":
			_, _ = w.Write([]byte(schemaBody))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	rc := newCDNResolver(registry.NewClient(srv.URL), 5*time.Minute, 24*time.Hour, 30*time.Second)
	rc.cacheDir = t.TempDir()

	il := intent.NewLookup()
	chain := newResolverChain(il, NewOverrideStore(), rc, true)

	docURI := "file://" + filepath.Join(workspaceDir, "cert.yaml")
	got := chain.Resolve(
		context.Background(),
		docURI,
		"apiVersion: "+unpinnedGroup+"/v1\nkind: "+kind+"\n",
	)
	if got.State != StateUnpinned {
		t.Fatalf("State = %v, want StateUnpinned (cert-manager not pinned in intent)", got.State)
	}
	if got.Version != latestVersion {
		t.Fatalf("Version = %q, want %q", got.Version, latestVersion)
	}
}

// TestResolve_OverrideBeatsCDN verifies that a per-document version override
// takes priority over the CDN latest path.
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
	chain := newResolverChain(nil, ov, rc, true)

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

// TestResolve_CDNFallback verifies that when no intent pin exists, the CDN
// latest path is used, returning StateUnpinned.
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
	chain := newResolverChain(nil, NewOverrideStore(), rc, true)

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

// TestResolve_Unindexable verifies that a group not known to the CDN
// resolves to StateUnindexable.
func TestResolve_Unindexable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	rc := newCDNResolver(registry.NewClient(srv.URL), 5*time.Minute, 24*time.Hour, 30*time.Second)
	rc.cacheDir = t.TempDir()
	chain := newResolverChain(nil, NewOverrideStore(), rc, true)

	got := chain.Resolve(
		context.Background(),
		"file:///unknown.yaml",
		"apiVersion: unknown.example.com/v1\nkind: Foo\n",
	)
	if got.State != StateUnindexable {
		t.Fatalf("State = %v, want StateUnindexable", got.State)
	}
}

// TestResolve_FallbackDisabled verifies that CDN fallback is skipped when
// fallbackEnabled is false.
func TestResolve_FallbackDisabled(t *testing.T) {
	rc := newCDNResolver(registry.NewClient("http://no.cdn.example"), 5*time.Minute, 24*time.Hour, 30*time.Second)
	rc.cacheDir = t.TempDir()
	chain := newResolverChain(nil, NewOverrideStore(), rc, false)

	got := chain.Resolve(
		context.Background(),
		"file:///vmcluster.yaml",
		"apiVersion: operator.victoriametrics.com/v1beta1\nkind: VMCluster\n",
	)
	if got.State != StateUnindexable {
		t.Fatalf("State = %v, want StateUnindexable (fallback disabled)", got.State)
	}
}

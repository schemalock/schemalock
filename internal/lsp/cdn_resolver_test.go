package lsp

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/schemalock/app/internal/registry"
)

// fakeCDN returns a httptest server that serves versions.json/manifest.json
// for a single group. The hit counter lets tests assert coalescing.
func fakeCDN(t *testing.T, group string, versions []string) (*httptest.Server, *int64) {
	t.Helper()
	var hits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		switch r.URL.Path {
		case "/kubernetes/" + group + "/versions.json":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`["` + versions[0] + `"]`))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func TestCDNResolver_VersionsCacheHit(t *testing.T) {
	srv, hits := fakeCDN(t, "operator.victoriametrics.com", []string{"0.70.0"})
	rc := newCDNResolver(registry.NewClient(srv.URL), 5*time.Minute, 24*time.Hour, 30*time.Second)

	ctx := context.Background()
	for i := 0; i < 3; i++ {
		got, err := rc.versions(ctx, "kubernetes", "operator.victoriametrics.com")
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
		if len(got) != 1 || got[0] != "0.70.0" {
			t.Fatalf("got %v", got)
		}
	}
	if atomic.LoadInt64(hits) != 1 {
		t.Fatalf("expected 1 CDN hit (cache), got %d", atomic.LoadInt64(hits))
	}
}

func TestCDNResolver_VersionsNotFoundCachedAsNegative(t *testing.T) {
	var hits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		http.NotFound(w, r)
	}))
	defer srv.Close()
	rc := newCDNResolver(registry.NewClient(srv.URL), 5*time.Minute, 24*time.Hour, 30*time.Second)

	ctx := context.Background()
	for i := 0; i < 2; i++ {
		_, err := rc.versions(ctx, "kubernetes", "unknown.example.com")
		if err == nil {
			t.Fatalf("call %d: expected ErrNotFound", i)
		}
	}
	if atomic.LoadInt64(&hits) != 1 {
		t.Fatalf("expected 1 CDN hit (negative cache), got %d", atomic.LoadInt64(&hits))
	}
}

func fakeCDNFull(t *testing.T, group, version string, kinds map[string]string) (*httptest.Server, *int64) {
	t.Helper()
	var hits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		switch r.URL.Path {
		case "/kubernetes/" + group + "/versions.json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`["` + version + `"]`))
			return
		case "/kubernetes/" + group + "/" + version + "/manifest.json":
			w.Header().Set("Content-Type", "application/json")
			body := `{"version":1,"kinds":{`
			first := true
			for k, integ := range kinds {
				if !first {
					body += ","
				}
				first = false
				body += `"` + k + `":{"integrity":"` + integ + `","size":42}`
			}
			body += `}}`
			_, _ = w.Write([]byte(body))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func TestCDNResolver_ManifestCacheHit(t *testing.T) {
	srv, hits := fakeCDNFull(t, "operator.victoriametrics.com", "0.70.0", map[string]string{
		"VMCluster": "sha256-abc",
	})
	rc := newCDNResolver(registry.NewClient(srv.URL), 5*time.Minute, 24*time.Hour, 30*time.Second)

	ctx := context.Background()
	for i := 0; i < 3; i++ {
		m, err := rc.manifest(ctx, "kubernetes", "operator.victoriametrics.com", "0.70.0")
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
		if _, ok := m.Kinds["VMCluster"]; !ok {
			t.Fatalf("VMCluster missing in manifest")
		}
	}
	// 3 manifest calls + 0 versions calls = 1 CDN hit total (manifest only).
	if atomic.LoadInt64(hits) != 1 {
		t.Fatalf("expected 1 CDN hit, got %d", atomic.LoadInt64(hits))
	}
}

func TestCDNResolver_TransientErrorsNotCachedLong(t *testing.T) {
	var hits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	rc := newCDNResolver(registry.NewClient(srv.URL), 5*time.Minute, 24*time.Hour, 30*time.Second)

	ctx := context.Background()
	_, err := rc.versions(ctx, "kubernetes", "flaky.example.com")
	if err == nil {
		t.Fatal("expected error")
	}

	// IsTransientError tags the most recent failure; subsequent calls within errTTL
	// are short-circuited (no second CDN hit during the back-off window).
	if !rc.IsTransientError("kubernetes", "flaky.example.com") {
		t.Fatal("expected transient error flag")
	}

	// A second call within the errTTL window short-circuits without another CDN call.
	_, _ = rc.versions(ctx, "kubernetes", "flaky.example.com")
	if atomic.LoadInt64(&hits) != 1 {
		t.Fatalf("expected 1 hit during back-off, got %d", atomic.LoadInt64(&hits))
	}

	// Calling ClearTransientErrors releases the back-off; next call hits the CDN again.
	rc.ClearTransientErrors("kubernetes", "flaky.example.com")
	_, _ = rc.versions(ctx, "kubernetes", "flaky.example.com")
	if atomic.LoadInt64(&hits) != 2 {
		t.Fatalf("expected 2 hits after retry, got %d", atomic.LoadInt64(&hits))
	}
}

func TestCDNResolver_Resolve(t *testing.T) {
	schemaBody := `{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object"}`

	var hits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		switch r.URL.Path {
		case "/kubernetes/operator.victoriametrics.com/versions.json":
			_, _ = w.Write([]byte(`["0.70.0"]`))
		case "/kubernetes/operator.victoriametrics.com/0.70.0/manifest.json":
			_, _ = w.Write([]byte(`{"version":1,"kinds":{"VMCluster":{"integrity":"` + integrityOf(schemaBody) + `","size":` + sizeStr(schemaBody) + `}}}`))
		case "/kubernetes/operator.victoriametrics.com/0.70.0/VMCluster.json":
			_, _ = w.Write([]byte(schemaBody))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	tmp := t.TempDir()
	rc := newCDNResolver(registry.NewClient(srv.URL), 5*time.Minute, 24*time.Hour, 30*time.Second)
	rc.cacheDir = tmp // wired by test, prod uses ~/.schemalock/cache

	ctx := context.Background()
	res, err := rc.Resolve(ctx, "kubernetes", "operator.victoriametrics.com", "VMCluster")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.Version != "0.70.0" {
		t.Fatalf("got version %q", res.Version)
	}
	if string(res.SchemaBytes) != schemaBody {
		t.Fatalf("schema body mismatch")
	}

	// Second call must hit only the disk cache (no new CDN hits).
	startHits := atomic.LoadInt64(&hits)
	_, err = rc.Resolve(ctx, "kubernetes", "operator.victoriametrics.com", "VMCluster")
	if err != nil {
		t.Fatalf("Resolve (warm): %v", err)
	}
	if atomic.LoadInt64(&hits) != startHits {
		t.Fatalf("warm Resolve should not call CDN")
	}
}

// TestSafeCachePath verifies that safeCachePath rejects traversal segments and
// accepts clean segments.
func TestSafeCachePath(t *testing.T) {
	dir := t.TempDir()

	bad := []struct {
		name                      string
		eco, group, version, kind string
	}{
		{"dotdot version", "k8s", "op.io", "../..", "Kind"},
		{"slash in kind", "k8s", "op.io", "1.0.0", "../../etc/passwd"},
		{"backslash", "k8s", "op.io", "1.0.0", "Kind\\evil"},
		{"null byte", "k8s", "op.io", "1.0\x00.0", "Kind"},
		{"empty version", "k8s", "op.io", "", "Kind"},
		{"dot-only", "k8s", "op.io", ".", "Kind"},
	}
	for _, tc := range bad {
		t.Run("reject/"+tc.name, func(t *testing.T) {
			_, err := safeCachePath(dir, tc.eco, tc.group, tc.version, tc.kind)
			if err == nil {
				t.Errorf("expected error for unsafe segment, got nil")
			}
		})
	}

	good := []struct {
		name                      string
		eco, group, version, kind string
	}{
		{"clean", "kubernetes", "operator.victoriametrics.com", "0.70.0", "VMCluster"},
		{"unicode lookalike ok", "k8s", "op.io", "1.0.0", "Kind∕name"},
	}
	for _, tc := range good {
		t.Run("accept/"+tc.name, func(t *testing.T) {
			p, err := safeCachePath(dir, tc.eco, tc.group, tc.version, tc.kind)
			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if p == "" {
				t.Error("expected non-empty path")
			}
		})
	}
}

func TestCDNResolver_AtomicWrite(t *testing.T) {
	schemaBody := `{"type":"object","title":"AtomicWriteTest"}`
	h := sha256.Sum256([]byte(schemaBody))
	integrity := "sha256-" + base64.StdEncoding.EncodeToString(h[:])

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/kubernetes/op.io/versions.json":
			_, _ = w.Write([]byte(`["1.0.0"]`))
		case "/kubernetes/op.io/1.0.0/manifest.json":
			body := `{"version":1,"kinds":{"Kind":{"integrity":"` + integrity + `","size":` + strconv.Itoa(len(schemaBody)) + `}}}`
			_, _ = w.Write([]byte(body))
		case "/kubernetes/op.io/1.0.0/Kind.json":
			_, _ = w.Write([]byte(schemaBody))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	cacheDir := t.TempDir()
	rc := newCDNResolver(registry.NewClient(srv.URL), 5*time.Minute, 24*time.Hour, 30*time.Second)
	rc.cacheDir = cacheDir

	result, err := rc.Resolve(context.Background(), "kubernetes", "op.io", "Kind")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if string(result.SchemaBytes) != schemaBody {
		t.Errorf("schema bytes mismatch")
	}

	// Second call must be a cache hit (FromCache=true) with complete bytes.
	result2, err := rc.Resolve(context.Background(), "kubernetes", "op.io", "Kind")
	if err != nil {
		t.Fatalf("Resolve (cached): %v", err)
	}
	if !result2.FromCache {
		t.Error("second Resolve should be a cache hit")
	}
	if string(result2.SchemaBytes) != schemaBody {
		t.Errorf("cached schema bytes mismatch")
	}

	// No temp file debris.
	matches, _ := filepath.Glob(filepath.Join(cacheDir, "*", "*", "*", ".schemalock-tmp-*"))
	if len(matches) > 0 {
		t.Errorf("temp debris found: %v", matches)
	}
}

// integrityOf returns "sha256-<base64-std(sha256(s))>" matching registry.ComputeIntegrity.
func integrityOf(s string) string {
	h := sha256.Sum256([]byte(s))
	return "sha256-" + base64.StdEncoding.EncodeToString(h[:])
}

func sizeStr(s string) string { return strconv.Itoa(len(s)) }

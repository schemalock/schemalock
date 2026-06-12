package registry_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/schemalock/schemalock/internal/registry"
)

// knownUserAgent is the default User-Agent the client must send on every request.
const knownUserAgent = "schemalock/0.1 (+https://schemalock.dev)"

// sampleManifest is the hand-written manifest.json used by happy-path tests.
// Shape must match the live cdn.schemalock.dev format.
var sampleManifest = registry.Manifest{
	Version: 1,
	Kinds: map[string]registry.KindEntry{
		"Foo": {
			Integrity: "sha256-ungWv48Bz+pBQUDeXa4iI7ADYaOWF3qctBD/YfIAFa0=",
			Size:      42,
		},
	},
}

// newTestServer builds an httptest.Server that routes GET requests.
// manifestPath is the URL path that serves sampleManifest.
// schemaPath is the URL path that serves schemaBody.
// Any other path returns 404.
func newTestServer(t *testing.T, assertUA bool) *httptest.Server {
	t.Helper()
	manifestBytes, err := json.Marshal(sampleManifest)
	if err != nil {
		t.Fatalf("marshaling sample manifest: %v", err)
	}
	schemaBody := []byte("schema body")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if assertUA {
			if got := r.Header.Get("User-Agent"); got != knownUserAgent {
				t.Errorf("User-Agent = %q, want %q", got, knownUserAgent)
			}
		}
		switch r.URL.Path {
		case "/kubernetes/operator.example.com/1.0.0/manifest.json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(manifestBytes)
		case "/kubernetes/operator.example.com/1.0.0/Foo.json":
			_, _ = w.Write(schemaBody)
		case "/kubernetes/operator.example.com/1.0.0/error500.json":
			http.Error(w, "internal error", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestFetchManifest_Happy verifies that FetchManifest decodes a well-formed
// manifest.json correctly.
func TestFetchManifest_Happy(t *testing.T) {
	srv := newTestServer(t, true)
	c := registry.NewClient(srv.URL)

	m, err := c.FetchManifest(context.Background(), "kubernetes", "operator.example.com", "1.0.0")
	if err != nil {
		t.Fatalf("FetchManifest: unexpected error: %v", err)
	}
	if m.Version != 1 {
		t.Errorf("Manifest.Version = %d, want 1", m.Version)
	}
	entry, ok := m.Kinds["Foo"]
	if !ok {
		t.Fatal("Manifest.Kinds missing key \"Foo\"")
	}
	if entry.Size != 42 {
		t.Errorf("Kinds[Foo].Size = %d, want 42", entry.Size)
	}
}

// TestFetchSchema_Happy verifies that FetchSchema returns bytes byte-identical
// to what the server sent.
func TestFetchSchema_Happy(t *testing.T) {
	srv := newTestServer(t, true)
	c := registry.NewClient(srv.URL)

	got, err := c.FetchSchema(context.Background(), "kubernetes", "operator.example.com", "1.0.0", "Foo")
	if err != nil {
		t.Fatalf("FetchSchema: unexpected error: %v", err)
	}
	want := []byte("schema body")
	if string(got) != string(want) {
		t.Errorf("FetchSchema body = %q, want %q", got, want)
	}
}

// TestFetchManifest_NotFound verifies that a 404 response wraps ErrNotFound.
func TestFetchManifest_NotFound(t *testing.T) {
	srv := newTestServer(t, false)
	c := registry.NewClient(srv.URL)

	_, err := c.FetchManifest(context.Background(), "kubernetes", "operator.example.com", "9.9.9")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, registry.ErrNotFound) {
		t.Errorf("errors.Is(err, ErrNotFound) = false; err = %v", err)
	}
}

// TestFetchSchema_NotFound verifies that a 404 for a schema wraps ErrNotFound.
func TestFetchSchema_NotFound(t *testing.T) {
	srv := newTestServer(t, false)
	c := registry.NewClient(srv.URL)

	_, err := c.FetchSchema(context.Background(), "kubernetes", "operator.example.com", "9.9.9", "Bar")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, registry.ErrNotFound) {
		t.Errorf("errors.Is(err, ErrNotFound) = false; err = %v", err)
	}
}

// TestFetchSchema_ServerError verifies that a 5xx response returns an error
// that does NOT wrap ErrNotFound (5xx bubbles as a plain error).
func TestFetchSchema_ServerError(t *testing.T) {
	srv := newTestServer(t, false)
	c := registry.NewClient(srv.URL)

	_, err := c.FetchSchema(context.Background(), "kubernetes", "operator.example.com", "1.0.0", "error500")
	if err == nil {
		t.Fatal("expected error for 5xx response, got nil")
	}
	if errors.Is(err, registry.ErrNotFound) {
		t.Errorf("5xx error should not wrap ErrNotFound, but errors.Is returned true")
	}
}

// TestFetchManifest_NetworkFailure verifies that a network-level failure
// (server closed) returns an error.
func TestFetchManifest_NetworkFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	// Close before the request.
	srv.Close()

	c := registry.NewClient(srv.URL)
	_, err := c.FetchManifest(context.Background(), "kubernetes", "operator.example.com", "1.0.0")
	if err == nil {
		t.Fatal("expected error on network failure, got nil")
	}
}

// TestFetchManifest_ContextCanceled verifies that context cancellation
// propagates as an error wrapping context.Canceled.
func TestFetchManifest_ContextCanceled(t *testing.T) {
	// Use a server that blocks long enough for us to cancel.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Block until the client context is done.
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	c := registry.NewClient(srv.URL)

	done := make(chan error, 1)
	go func() {
		_, err := c.FetchManifest(ctx, "kubernetes", "operator.example.com", "1.0.0")
		done <- err
	}()

	// Cancel after the goroutine has started the request.
	cancel()
	err := <-done
	if err == nil {
		t.Fatal("expected error on context cancellation, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("errors.Is(err, context.Canceled) = false; err = %v", err)
	}
}

// TestUserAgent verifies that the User-Agent header is set on FetchSchema too.
func TestUserAgent(t *testing.T) {
	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		_, _ = w.Write([]byte("data"))
	}))
	t.Cleanup(srv.Close)

	c := registry.NewClient(srv.URL)
	_, _ = c.FetchSchema(context.Background(), "k8s", "g", "v", "Kind")

	if gotUA != knownUserAgent {
		t.Errorf("User-Agent = %q, want %q", gotUA, knownUserAgent)
	}
}

// TestComputeIntegrity_AbcVector is the concrete cross-check with ingest/:
// sha256("abc") in base64-std is ungWv48Bz+pBQUDeXa4iI7ADYaOWF3qctBD/YfIAFa0=.
func TestComputeIntegrity_AbcVector(t *testing.T) {
	const want = "sha256-ungWv48Bz+pBQUDeXa4iI7ADYaOWF3qctBD/YfIAFa0="
	got := registry.ComputeIntegrity([]byte("abc"))
	if got != want {
		t.Errorf("ComputeIntegrity(\"abc\") = %q, want %q", got, want)
	}
}

// TestVerifyIntegrity covers matching, mismatching, empty-body, and malformed
// integrity strings.
func TestVerifyIntegrity(t *testing.T) {
	abcHash := "sha256-ungWv48Bz+pBQUDeXa4iI7ADYaOWF3qctBD/YfIAFa0="
	emptyHash := registry.ComputeIntegrity([]byte{})

	tests := []struct {
		name      string
		body      []byte
		integrity string
		wantErr   bool
		wantIs    error // nil means no specific sentinel check
	}{
		{
			name:      "matching pair returns nil",
			body:      []byte("abc"),
			integrity: abcHash,
			wantErr:   false,
		},
		{
			name:      "non-matching pair returns ErrIntegrityMismatch",
			body:      []byte("abc"),
			integrity: "sha256-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
			wantErr:   true,
			wantIs:    registry.ErrIntegrityMismatch,
		},
		{
			name:      "empty body with its true hash returns nil",
			body:      []byte{},
			integrity: emptyHash,
			wantErr:   false,
		},
		{
			name:      "malformed integrity string (wrong prefix) returns error",
			body:      []byte("abc"),
			integrity: "notbase64!!!",
			wantErr:   true,
			wantIs:    registry.ErrIntegrityMismatch,
		},
		{
			name:      "malformed integrity string (md5 prefix) returns error",
			body:      []byte("abc"),
			integrity: "md5-ungWv48Bz+pBQUDeXa4iI7ADYaOWF3qctBD/YfIAFa0=",
			wantErr:   true,
			wantIs:    registry.ErrIntegrityMismatch,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := registry.VerifyIntegrity(tt.body, tt.integrity)
			if tt.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantIs != nil && !errors.Is(err, tt.wantIs) {
				t.Errorf("errors.Is(err, %v) = false; err = %v", tt.wantIs, err)
			}
		})
	}
}

// TestGet_ResponseTooLarge verifies that a response exceeding MaxBodyBytes
// is rejected with an error rather than read into memory unbounded.
func TestGet_ResponseTooLarge(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		buf := make([]byte, registry.MaxBodyBytes+1)
		_, _ = w.Write(buf)
	}))
	t.Cleanup(srv.Close)

	c := registry.NewClient(srv.URL)
	_, err := c.FetchSchema(context.Background(), "k8s", "op", "1.0", "Kind")
	if err == nil {
		t.Fatal("expected error for oversized response, got nil")
	}
	if !strings.Contains(err.Error(), "too large") {
		t.Errorf("error should mention 'too large': %v", err)
	}
}

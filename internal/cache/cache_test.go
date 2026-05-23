package cache_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/schemalock/app/internal/cache"
	"github.com/schemalock/app/internal/registry"
)

// testArgs holds a fixed (ecosystem, group, version, kind) tuple reused across
// several tests.
var testArgs = struct {
	eco, group, version, kind string
}{
	eco:     "kubernetes",
	group:   "operator.victoriametrics.com",
	version: "0.70.0",
	kind:    "VMCluster",
}

// computeIntegrity is a thin helper so tests can produce the correct SRI
// string for a body without duplicating the hashing logic.
func computeIntegrity(body []byte) string {
	return registry.ComputeIntegrity(body)
}

// TestRoundTrip writes a body with its correct integrity and reads it back.
func TestRoundTrip(t *testing.T) {
	root := t.TempDir()
	c := cache.New(root)

	body := []byte(`{"kind":"VMCluster"}`)
	integrity := computeIntegrity(body)

	if err := c.WriteSchema(testArgs.eco, testArgs.group, testArgs.version, testArgs.kind, integrity, body); err != nil {
		t.Fatalf("WriteSchema: %v", err)
	}

	got, err := c.ReadSchema(testArgs.eco, testArgs.group, testArgs.version, testArgs.kind)
	if err != nil {
		t.Fatalf("ReadSchema: %v", err)
	}

	if string(got) != string(body) {
		t.Errorf("round-trip mismatch:\n got  %q\n want %q", got, body)
	}
}

// TestMismatchNoFile asserts that WriteSchema with a wrong integrity string
// returns an error wrapping registry.ErrIntegrityMismatch and does NOT create
// the cache file.
func TestMismatchNoFile(t *testing.T) {
	root := t.TempDir()
	c := cache.New(root)

	body := []byte(`{"kind":"VMCluster"}`)
	wrongIntegrity := computeIntegrity([]byte("other bytes")) // integrity of different content

	err := c.WriteSchema(testArgs.eco, testArgs.group, testArgs.version, testArgs.kind, wrongIntegrity, body)
	if err == nil {
		t.Fatal("expected error from WriteSchema with wrong integrity, got nil")
	}
	if !errors.Is(err, registry.ErrIntegrityMismatch) {
		t.Errorf("error does not wrap registry.ErrIntegrityMismatch: %v", err)
	}

	// The cache file must not exist.
	path := c.SchemaPath(testArgs.eco, testArgs.group, testArgs.version, testArgs.kind)
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Errorf("expected file to not exist after integrity mismatch, but os.Stat returned: %v", statErr)
	}
}

// TestMissingReadReturnsErrNotFound asserts that reading a key that was never
// written returns an error wrapping cache.ErrNotFound.
func TestMissingReadReturnsErrNotFound(t *testing.T) {
	root := t.TempDir()
	c := cache.New(root)

	_, err := c.ReadSchema(testArgs.eco, testArgs.group, testArgs.version, testArgs.kind)
	if err == nil {
		t.Fatal("expected error for missing schema, got nil")
	}
	if !errors.Is(err, cache.ErrNotFound) {
		t.Errorf("error does not wrap cache.ErrNotFound: %v", err)
	}
}

// TestConcurrentWrites launches 50 goroutines that all write the same key with
// identical bytes. After all goroutines complete:
//  1. The file exists and contains the expected bytes.
//  2. No .schemalock-tmp-* debris remains in any subdirectory.
func TestConcurrentWrites(t *testing.T) {
	root := t.TempDir()
	c := cache.New(root)

	body := []byte(`{"kind":"VMCluster","concurrent":true}`)
	integrity := computeIntegrity(body)

	const workers = 50
	errs := make([]error, workers)
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := range workers {
		go func(idx int) {
			defer wg.Done()
			errs[idx] = c.WriteSchema(testArgs.eco, testArgs.group, testArgs.version, testArgs.kind, integrity, body)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d: WriteSchema error: %v", i, err)
		}
	}

	// File must exist with the expected content.
	got, err := c.ReadSchema(testArgs.eco, testArgs.group, testArgs.version, testArgs.kind)
	if err != nil {
		t.Fatalf("ReadSchema after concurrent writes: %v", err)
	}
	if string(got) != string(body) {
		t.Errorf("content mismatch after concurrent writes:\n got  %q\n want %q", got, body)
	}

	// No .schemalock-tmp-* debris anywhere under root.
	matches, err := filepath.Glob(filepath.Join(root, "*", "*", "*", ".schemalock-tmp-*"))
	if err != nil {
		t.Fatalf("Glob error: %v", err)
	}
	if len(matches) > 0 {
		t.Errorf("temp file debris found after concurrent writes: %v", matches)
	}
}

// TestLayoutMatchesCDN locks in that SchemaPath produces the exact on-disk
// layout that mirrors the CDN URL path. Future refactors must not drift this.
func TestLayoutMatchesCDN(t *testing.T) {
	root := t.TempDir()
	c := cache.New(root)

	got := c.SchemaPath("kubernetes", "operator.victoriametrics.com", "0.70.0", "VMCluster")
	want := filepath.Join(root, "kubernetes", "operator.victoriametrics.com", "0.70.0", "VMCluster.json")

	if got != want {
		t.Errorf("SchemaPath mismatch:\n got  %q\n want %q", got, want)
	}
}

// TestDefaultRootSmoke checks that DefaultRoot returns a path whose suffix
// matches the canonical cache location (/.schemalock/cache or the OS-specific
// equivalent) without asserting a specific home directory.
func TestDefaultRootSmoke(t *testing.T) {
	got, err := cache.DefaultRoot()
	if err != nil {
		t.Fatalf("DefaultRoot: %v", err)
	}

	wantSuffix := filepath.Join(".schemalock", "cache")
	if !strings.HasSuffix(got, wantSuffix) {
		t.Errorf("DefaultRoot() = %q, want suffix %q", got, wantSuffix)
	}
}

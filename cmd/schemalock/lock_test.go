package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/schemalock/app/internal/lockfile"
	"github.com/schemalock/app/internal/registry"
)

// fakeManifest builds a JSON manifest response with the given kinds.
func fakeManifest(kinds map[string]registry.KindEntry) []byte {
	m := registry.Manifest{
		Version: 1,
		Kinds:   kinds,
	}
	b, err := json.Marshal(m)
	if err != nil {
		panic(err)
	}
	return b
}

// TestRunLock_HappyPath tests the end-to-end happy path: an httptest server
// serves a fake manifest, runLock fetches it and writes a valid lockfile.
func TestRunLock_HappyPath(t *testing.T) {
	const (
		eco     = "kubernetes"
		group   = "operator.victoriametrics.com"
		version = "0.70.0"
	)

	kinds := map[string]registry.KindEntry{
		"VMCluster": {Integrity: "sha256-AAAA", Size: 1234},
		"VMSingle":  {Integrity: "sha256-BBBB", Size: 5678},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		want := "/" + eco + "/" + group + "/" + version + "/manifest.json"
		if r.URL.Path != want {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(fakeManifest(kinds))
	}))
	defer srv.Close()

	dir := t.TempDir()
	intentPath := filepath.Join(dir, "schemalock.yaml")
	lockPath := filepath.Join(dir, "schemalock.lock")

	intentContent := "version: 1\necosystems:\n  kubernetes:\n    - operator.victoriametrics.com@0.70.0\n"
	if err := os.WriteFile(intentPath, []byte(intentContent), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	err := runLock(context.Background(),
		[]string{
			"--file=" + intentPath,
			"--lockfile=" + lockPath,
			"--registry=" + srv.URL,
		},
		&stdout, &stderr,
	)
	if err != nil {
		t.Fatalf("runLock returned error: %v", err)
	}

	// Check stdout message.
	if !strings.Contains(stdout.String(), lockPath) {
		t.Errorf("stdout %q should contain lockfile path", stdout.String())
	}
	if !strings.Contains(stdout.String(), "1 entries") {
		t.Errorf("stdout %q should mention entry count", stdout.String())
	}

	// Read and decode the written lockfile.
	rawLock, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatalf("reading written lockfile: %v", err)
	}
	lf, err := lockfile.DecodeLock(rawLock)
	if err != nil {
		t.Fatalf("DecodeLock: %v", err)
	}

	if lf.Version != 1 {
		t.Errorf("Version = %d, want 1", lf.Version)
	}
	if lf.Registry != srv.URL {
		t.Errorf("Registry = %q, want %q", lf.Registry, srv.URL)
	}
	if len(lf.Entries) != 1 {
		t.Fatalf("len(Entries) = %d, want 1", len(lf.Entries))
	}

	entry := lf.Entries[0]
	wantID := eco + "/" + group + "@" + version
	if entry.ID != wantID {
		t.Errorf("entry.ID = %q, want %q", entry.ID, wantID)
	}
	wantBase := eco + "/" + group + "/" + version + "/"
	if entry.Base != wantBase {
		t.Errorf("entry.Base = %q, want %q", entry.Base, wantBase)
	}
	if len(entry.Schemas) != 2 {
		t.Errorf("len(Schemas) = %d, want 2", len(entry.Schemas))
	}
	for kind, ke := range kinds {
		got, ok := entry.Schemas[kind]
		if !ok {
			t.Errorf("Schemas missing Kind %q", kind)
			continue
		}
		if got.Integrity != ke.Integrity {
			t.Errorf("Schemas[%q].Integrity = %q, want %q", kind, got.Integrity, ke.Integrity)
		}
		if got.Size != ke.Size {
			t.Errorf("Schemas[%q].Size = %d, want %d", kind, got.Size, ke.Size)
		}
	}
}

// TestRunLock_404FromRegistry verifies that a 404 from the registry causes
// runLock to return an error wrapping ErrIO (exit code 3).
func TestRunLock_404FromRegistry(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	dir := t.TempDir()
	intentPath := filepath.Join(dir, "schemalock.yaml")
	lockPath := filepath.Join(dir, "schemalock.lock")

	intentContent := "version: 1\necosystems:\n  kubernetes:\n    - operator.victoriametrics.com@0.70.0\n"
	if err := os.WriteFile(intentPath, []byte(intentContent), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	err := runLock(context.Background(),
		[]string{
			"--file=" + intentPath,
			"--lockfile=" + lockPath,
			"--registry=" + srv.URL,
		},
		&stdout, &stderr,
	)
	if err == nil {
		t.Fatal("expected error for 404, got nil")
	}
	if !errors.Is(err, ErrIO) {
		t.Errorf("expected ErrIO, got: %v", err)
	}
	if exitCodeFor(err) != 3 {
		t.Errorf("exitCodeFor = %d, want 3", exitCodeFor(err))
	}
}

// TestRunLock_MissingIntentFile verifies that a missing intent file returns
// ErrUsage (exit code 1).
func TestRunLock_MissingIntentFile(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	err := runLock(context.Background(),
		[]string{"--file=/does/not/exist/schemalock.yaml", "--lockfile=" + filepath.Join(dir, "out.lock")},
		&stdout, &stderr,
	)
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
	if !errors.Is(err, ErrUsage) {
		t.Errorf("expected ErrUsage, got: %v", err)
	}
	if exitCodeFor(err) != 1 {
		t.Errorf("exitCodeFor = %d, want 1", exitCodeFor(err))
	}
}

// TestRunLock_MalformedIntent verifies that a malformed intent file returns
// ErrUsage (exit code 1).
func TestRunLock_MalformedIntent(t *testing.T) {
	dir := t.TempDir()
	intentPath := filepath.Join(dir, "schemalock.yaml")
	if err := os.WriteFile(intentPath, []byte(":::garbage:::\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	err := runLock(context.Background(),
		[]string{"--file=" + intentPath, "--lockfile=" + filepath.Join(dir, "out.lock")},
		&stdout, &stderr,
	)
	if err == nil {
		t.Fatal("expected error for malformed intent, got nil")
	}
	if !errors.Is(err, ErrUsage) {
		t.Errorf("expected ErrUsage, got: %v", err)
	}
	if exitCodeFor(err) != 1 {
		t.Errorf("exitCodeFor = %d, want 1", exitCodeFor(err))
	}
}

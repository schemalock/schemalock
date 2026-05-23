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

// buildTestLockfile writes a schemalock.lock to dir/schemalock.lock with
// a single entry for kubernetes/operator.victoriametrics.com@0.70.0 and the
// provided schemas.
func buildTestLockfile(t *testing.T, dir string, schemas map[string]lockfile.SchemaEntry, registryURL string) string {
	t.Helper()
	lf := lockfile.LockFile{
		Version:     1,
		Registry:    registryURL,
		GeneratedAt: "2026-05-18T00:00:00Z",
		Entries: []lockfile.LockEntry{
			{
				ID:      "kubernetes/operator.victoriametrics.com@0.70.0",
				Base:    "kubernetes/operator.victoriametrics.com/0.70.0/",
				Schemas: schemas,
			},
		},
	}
	encoded, err := lockfile.EncodeLock(lf)
	if err != nil {
		t.Fatalf("EncodeLock: %v", err)
	}
	lockPath := filepath.Join(dir, "schemalock.lock")
	if err := os.WriteFile(lockPath, encoded, 0o644); err != nil {
		t.Fatalf("write lockfile: %v", err)
	}
	return lockPath
}

// buildTestIntent writes a schemalock.yaml to dir/schemalock.yaml.
func buildTestIntent(t *testing.T, dir string, entries []string) string {
	t.Helper()
	var sb strings.Builder
	sb.WriteString("version: 1\necosystems:\n  kubernetes:\n")
	for _, e := range entries {
		sb.WriteString("    - " + e + "\n")
	}
	intentPath := filepath.Join(dir, "schemalock.yaml")
	if err := os.WriteFile(intentPath, []byte(sb.String()), 0o644); err != nil {
		t.Fatalf("write intent: %v", err)
	}
	return intentPath
}

// serveManifest returns an httptest server that serves the given kinds at the
// standard kubernetes/operator.victoriametrics.com/0.70.0/manifest.json path.
func serveManifest(t *testing.T, kinds map[string]registry.KindEntry) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		const wantPath = "/kubernetes/operator.victoriametrics.com/0.70.0/manifest.json"
		if r.URL.Path != wantPath {
			http.NotFound(w, r)
			return
		}
		m := registry.Manifest{Version: 1, Kinds: kinds}
		b, err := json.Marshal(m)
		if err != nil {
			http.Error(w, "marshal error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(b)
	}))
	return srv
}

// TestRunVerify_MissingLockfile verifies that a missing schemalock.lock returns
// ErrUsage (not ErrDrift) with a helpful message guiding the user.
func TestRunVerify_MissingLockfile(t *testing.T) {
	dir := t.TempDir()
	intentPath := buildTestIntent(t, dir, []string{"operator.victoriametrics.com@0.70.0"})
	lockPath := filepath.Join(dir, "schemalock.lock") // does not exist

	var stdout, stderr bytes.Buffer
	err := runVerify(context.Background(),
		[]string{
			"--file=" + intentPath,
			"--lockfile=" + lockPath,
			"--registry=https://cdn.schemalock.dev",
		},
		&stdout, &stderr,
	)
	if err == nil {
		t.Fatal("expected error for missing lockfile, got nil")
	}
	if !errors.Is(err, ErrUsage) {
		t.Errorf("expected ErrUsage, got: %v", err)
	}
	if !strings.Contains(err.Error(), lockPath) {
		t.Errorf("error message %q should contain lockfile path %s", err.Error(), lockPath)
	}
}

// TestRunVerify_MultipleDrifts verifies that two simultaneous integrity drifts
// in the same lockfile entry are both reported (collect-all, no fail-fast).
func TestRunVerify_MultipleDrifts(t *testing.T) {
	// Live manifest has different integrity for both VMCluster and VMSingle.
	liveKinds := map[string]registry.KindEntry{
		"VMCluster": {Integrity: "sha256-CHANGED1", Size: 1000},
		"VMSingle":  {Integrity: "sha256-CHANGED2", Size: 2000},
	}
	srv := serveManifest(t, liveKinds)
	defer srv.Close()

	dir := t.TempDir()
	// Lockfile has original integrity for both kinds.
	schemas := map[string]lockfile.SchemaEntry{
		"VMCluster": {Integrity: "sha256-AAAA", Size: 1000},
		"VMSingle":  {Integrity: "sha256-BBBB", Size: 2000},
	}
	lockPath := buildTestLockfile(t, dir, schemas, srv.URL)
	intentPath := buildTestIntent(t, dir, []string{"operator.victoriametrics.com@0.70.0"})

	var stdout, stderr bytes.Buffer
	err := runVerify(context.Background(),
		[]string{
			"--file=" + intentPath,
			"--lockfile=" + lockPath,
			"--registry=" + srv.URL,
		},
		&stdout, &stderr,
	)
	if err == nil {
		t.Fatal("expected error for drift, got nil")
	}
	if !errors.Is(err, ErrDrift) {
		t.Errorf("expected ErrDrift, got: %v", err)
	}
	if count := strings.Count(stdout.String(), "drift:"); count < 2 {
		t.Errorf("stdout should contain at least 2 'drift:' lines, got %d\nstdout: %s", count, stdout.String())
	}
}

// TestRunVerify_CleanState verifies exit 0 and "ok:" message when intent,
// lockfile, and live manifest are all consistent.
func TestRunVerify_CleanState(t *testing.T) {
	kinds := map[string]registry.KindEntry{
		"VMCluster": {Integrity: "sha256-AAAA", Size: 1000},
		"VMSingle":  {Integrity: "sha256-BBBB", Size: 2000},
	}
	srv := serveManifest(t, kinds)
	defer srv.Close()

	dir := t.TempDir()
	schemas := map[string]lockfile.SchemaEntry{
		"VMCluster": {Integrity: "sha256-AAAA", Size: 1000},
		"VMSingle":  {Integrity: "sha256-BBBB", Size: 2000},
	}
	lockPath := buildTestLockfile(t, dir, schemas, srv.URL)
	intentPath := buildTestIntent(t, dir, []string{"operator.victoriametrics.com@0.70.0"})

	var stdout, stderr bytes.Buffer
	err := runVerify(context.Background(),
		[]string{
			"--file=" + intentPath,
			"--lockfile=" + lockPath,
			"--registry=" + srv.URL,
		},
		&stdout, &stderr,
	)
	if err != nil {
		t.Fatalf("runVerify returned error: %v\nstdout: %s\nstderr: %s", err, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "ok:") {
		t.Errorf("stdout %q should contain 'ok:'", stdout.String())
	}
}

// TestRunVerify_IntegrityDrift verifies exit 2 and a "drift:" line when the
// live manifest has a different integrity for one Kind.
func TestRunVerify_IntegrityDrift(t *testing.T) {
	// Live manifest has a different integrity for VMCluster.
	liveKinds := map[string]registry.KindEntry{
		"VMCluster": {Integrity: "sha256-CHANGED", Size: 1000},
		"VMSingle":  {Integrity: "sha256-BBBB", Size: 2000},
	}
	srv := serveManifest(t, liveKinds)
	defer srv.Close()

	dir := t.TempDir()
	// Lockfile has original integrity.
	schemas := map[string]lockfile.SchemaEntry{
		"VMCluster": {Integrity: "sha256-AAAA", Size: 1000},
		"VMSingle":  {Integrity: "sha256-BBBB", Size: 2000},
	}
	lockPath := buildTestLockfile(t, dir, schemas, srv.URL)
	intentPath := buildTestIntent(t, dir, []string{"operator.victoriametrics.com@0.70.0"})

	var stdout, stderr bytes.Buffer
	err := runVerify(context.Background(),
		[]string{
			"--file=" + intentPath,
			"--lockfile=" + lockPath,
			"--registry=" + srv.URL,
		},
		&stdout, &stderr,
	)
	if err == nil {
		t.Fatal("expected error for drift, got nil")
	}
	if !errors.Is(err, ErrDrift) {
		t.Errorf("expected ErrDrift, got: %v", err)
	}
	if exitCodeFor(err) != 2 {
		t.Errorf("exitCodeFor = %d, want 2", exitCodeFor(err))
	}
	if !strings.Contains(stdout.String(), "drift:") {
		t.Errorf("stdout %q should contain 'drift:'", stdout.String())
	}
	if !strings.Contains(stdout.String(), "VMCluster") {
		t.Errorf("stdout %q should name the drifted Kind VMCluster", stdout.String())
	}
}

// TestRunVerify_NewIntentEntryNotInLockfile verifies exit 2 when a new entry
// exists in schemalock.yaml but is absent from schemalock.lock.
func TestRunVerify_NewIntentEntryNotInLockfile(t *testing.T) {
	kinds := map[string]registry.KindEntry{
		"VMCluster": {Integrity: "sha256-AAAA", Size: 1000},
	}
	srv := serveManifest(t, kinds)
	defer srv.Close()

	dir := t.TempDir()
	// Lockfile only has one entry.
	schemas := map[string]lockfile.SchemaEntry{
		"VMCluster": {Integrity: "sha256-AAAA", Size: 1000},
	}
	lockPath := buildTestLockfile(t, dir, schemas, srv.URL)
	// Intent has an extra entry not in the lockfile.
	intentPath := buildTestIntent(t, dir, []string{
		"operator.victoriametrics.com@0.70.0",
		"another.group.io@1.0.0",
	})

	var stdout, stderr bytes.Buffer
	err := runVerify(context.Background(),
		[]string{
			"--file=" + intentPath,
			"--lockfile=" + lockPath,
			"--registry=" + srv.URL,
		},
		&stdout, &stderr,
	)
	if err == nil {
		t.Fatal("expected error for extra intent entry, got nil")
	}
	if !errors.Is(err, ErrDrift) {
		t.Errorf("expected ErrDrift, got: %v", err)
	}
	if exitCodeFor(err) != 2 {
		t.Errorf("exitCodeFor = %d, want 2", exitCodeFor(err))
	}
	if !strings.Contains(stdout.String(), "drift:") {
		t.Errorf("stdout %q should contain 'drift:'", stdout.String())
	}
}

// TestRunVerify_StaleLockfileEntryNotInIntent verifies exit 2 when the
// lockfile has an entry that is no longer in schemalock.yaml.
func TestRunVerify_StaleLockfileEntryNotInIntent(t *testing.T) {
	kinds := map[string]registry.KindEntry{
		"VMCluster": {Integrity: "sha256-AAAA", Size: 1000},
	}
	srv := serveManifest(t, kinds)
	defer srv.Close()

	dir := t.TempDir()
	// Lockfile has an extra entry not in the intent.
	lf := lockfile.LockFile{
		Version:     1,
		Registry:    srv.URL,
		GeneratedAt: "2026-05-18T00:00:00Z",
		Entries: []lockfile.LockEntry{
			{
				ID:   "kubernetes/operator.victoriametrics.com@0.70.0",
				Base: "kubernetes/operator.victoriametrics.com/0.70.0/",
				Schemas: map[string]lockfile.SchemaEntry{
					"VMCluster": {Integrity: "sha256-AAAA", Size: 1000},
				},
			},
			{
				ID:   "kubernetes/stale.group.io@9.9.9",
				Base: "kubernetes/stale.group.io/9.9.9/",
				Schemas: map[string]lockfile.SchemaEntry{
					"StaleKind": {Integrity: "sha256-CCCC", Size: 500},
				},
			},
		},
	}
	encoded, err := lockfile.EncodeLock(lf)
	if err != nil {
		t.Fatalf("EncodeLock: %v", err)
	}
	lockPath := filepath.Join(dir, "schemalock.lock")
	if err := os.WriteFile(lockPath, encoded, 0o644); err != nil {
		t.Fatalf("write lockfile: %v", err)
	}
	// Intent only declares the first entry.
	intentPath := buildTestIntent(t, dir, []string{"operator.victoriametrics.com@0.70.0"})

	var stdout, stderr bytes.Buffer
	err = runVerify(context.Background(),
		[]string{
			"--file=" + intentPath,
			"--lockfile=" + lockPath,
			"--registry=" + srv.URL,
		},
		&stdout, &stderr,
	)
	if err == nil {
		t.Fatal("expected error for stale lockfile entry, got nil")
	}
	if !errors.Is(err, ErrDrift) {
		t.Errorf("expected ErrDrift, got: %v", err)
	}
	if exitCodeFor(err) != 2 {
		t.Errorf("exitCodeFor = %d, want 2", exitCodeFor(err))
	}
	if !strings.Contains(stdout.String(), "drift:") {
		t.Errorf("stdout %q should contain 'drift:'", stdout.String())
	}
}

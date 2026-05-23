package lockfile_test

import (
	"errors"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/schemalock/app/internal/lockfile"
)

// update is a test flag. When -update is passed, golden files are regenerated
// from the current EncodeLock output. Bump intentionally on format changes.
var update = flag.Bool("update", false, "rewrite golden files")

// fixedLockFile is the canonical LockFile used for golden-file tests. The
// generated_at is pinned so golden comparison is stable across runs.
func fixedLockFile() lockfile.LockFile {
	return lockfile.LockFile{
		Version:     1,
		Registry:    "https://cdn.schemalock.dev",
		GeneratedAt: "2026-05-18T00:00:00Z",
		Entries: []lockfile.LockEntry{
			{
				ID:   "kubernetes/operator.victoriametrics.com@0.70.0",
				Base: "kubernetes/operator.victoriametrics.com/0.70.0/",
				Schemas: map[string]lockfile.SchemaEntry{
					// Deliberately un-sorted to test sort stability.
					"VMSingle": {
						Integrity: "sha256-BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB=",
						Size:      2048,
					},
					"VMCluster": {
						Integrity: "sha256-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
						Size:      4096,
					},
				},
			},
		},
	}
}

// --- Test 1: DecodeIntent round-trip ---

func TestDecodeIntent_RoundTrip(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "intent_minimal.yaml"))
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	f, err := lockfile.DecodeIntent(b)
	if err != nil {
		t.Fatalf("DecodeIntent: %v", err)
	}
	if f.Version != 1 {
		t.Errorf("Version = %d, want 1", f.Version)
	}
	entries, ok := f.Ecosystems["kubernetes"]
	if !ok {
		t.Fatal("expected kubernetes ecosystem")
	}
	if len(entries) != 1 || entries[0] != "operator.victoriametrics.com@0.70.0" {
		t.Errorf("kubernetes entries = %v, want [operator.victoriametrics.com@0.70.0]", entries)
	}
}

// --- Test 2: DecodeIntent rejects missing version ---

func TestDecodeIntent_MissingVersion(t *testing.T) {
	input := []byte("ecosystems:\n  kubernetes:\n    - foo@1.0\n")
	_, err := lockfile.DecodeIntent(input)
	if err == nil {
		t.Fatal("expected error for missing version, got nil")
	}
	if !errors.Is(err, lockfile.ErrMalformedIntent) {
		t.Errorf("expected ErrMalformedIntent, got: %v", err)
	}
}

// --- Test 3: DecodeIntent accepts unknown ecosystem keys (forward-compat) ---

func TestDecodeIntent_UnknownEcosystem(t *testing.T) {
	input := []byte("version: 1\necosystems:\n  futuristic:\n    - somegroup@1.0\n")
	f, err := lockfile.DecodeIntent(input)
	if err != nil {
		t.Fatalf("expected no error for unknown ecosystem, got: %v", err)
	}
	if _, ok := f.Ecosystems["futuristic"]; !ok {
		t.Error("expected futuristic ecosystem to be present")
	}
}

// --- Test 4: ResolveID happy path ---

func TestResolveID_HappyPath(t *testing.T) {
	eco, grp, ver, err := lockfile.ResolveID("kubernetes/operator.victoriametrics.com@0.70.0")
	if err != nil {
		t.Fatalf("ResolveID: %v", err)
	}
	if eco != "kubernetes" {
		t.Errorf("ecosystem = %q, want %q", eco, "kubernetes")
	}
	if grp != "operator.victoriametrics.com" {
		t.Errorf("group = %q, want %q", grp, "operator.victoriametrics.com")
	}
	if ver != "0.70.0" {
		t.Errorf("version = %q, want %q", ver, "0.70.0")
	}
}

// --- Test 5: ResolveID rejects missing @ ---

func TestResolveID_MissingAt(t *testing.T) {
	_, _, _, err := lockfile.ResolveID("kubernetes/operator.victoriametrics.com")
	if err == nil {
		t.Fatal("expected error for missing @, got nil")
	}
	if !errors.Is(err, lockfile.ErrMalformedID) {
		t.Errorf("expected ErrMalformedID, got: %v", err)
	}
}

// --- Test 6: ResolveID rejects various empty components ---

func TestResolveID_EmptyComponents(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"missing slash", "kubernetes@1.0"},
		{"empty ecosystem", "/group@1.0"},
		{"empty group", "kubernetes/@1.0"},
		{"empty version", "kubernetes/group@"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, _, err := lockfile.ResolveID(tc.input)
			if err == nil {
				t.Fatalf("expected ErrMalformedID for %q, got nil", tc.input)
			}
			if !errors.Is(err, lockfile.ErrMalformedID) {
				t.Errorf("expected ErrMalformedID, got: %v", err)
			}
		})
	}
}

// --- Test 7: EncodeLock golden file ---

func TestEncodeLock_GoldenFile(t *testing.T) {
	got, err := lockfile.EncodeLock(fixedLockFile())
	if err != nil {
		t.Fatalf("EncodeLock: %v", err)
	}

	goldenPath := filepath.Join("testdata", "lockfile_golden.yaml")

	if *update {
		if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
			t.Fatalf("writing golden file: %v", err)
		}
		t.Log("golden file updated")
		return
	}

	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("reading golden file (run with -update to create): %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("EncodeLock output does not match golden file.\ngot:\n%s\nwant:\n%s", got, want)
	}
}

// --- Test 8: EncodeLock → DecodeLock round-trip ---

func TestEncodeLock_DecodeLock_RoundTrip(t *testing.T) {
	lf := fixedLockFile()
	b, err := lockfile.EncodeLock(lf)
	if err != nil {
		t.Fatalf("EncodeLock: %v", err)
	}

	decoded, err := lockfile.DecodeLock(b)
	if err != nil {
		t.Fatalf("DecodeLock: %v", err)
	}

	if decoded.Version != lf.Version {
		t.Errorf("Version: got %d, want %d", decoded.Version, lf.Version)
	}
	if decoded.Registry != lf.Registry {
		t.Errorf("Registry: got %q, want %q", decoded.Registry, lf.Registry)
	}
	if decoded.GeneratedAt != lf.GeneratedAt {
		t.Errorf("GeneratedAt: got %q, want %q", decoded.GeneratedAt, lf.GeneratedAt)
	}
	if len(decoded.Entries) != len(lf.Entries) {
		t.Fatalf("len(Entries): got %d, want %d", len(decoded.Entries), len(lf.Entries))
	}
	entry := decoded.Entries[0]
	if entry.ID != lf.Entries[0].ID {
		t.Errorf("Entry[0].ID: got %q, want %q", entry.ID, lf.Entries[0].ID)
	}
	if entry.Schemas["VMCluster"].Integrity != lf.Entries[0].Schemas["VMCluster"].Integrity {
		t.Errorf("VMCluster integrity mismatch")
	}
	if entry.Schemas["VMSingle"].Size != lf.Entries[0].Schemas["VMSingle"].Size {
		t.Errorf("VMSingle size mismatch")
	}
}

// --- Test 9: EncodeLock determinism (100 iterations) ---

func TestEncodeLock_Determinism(t *testing.T) {
	lf := fixedLockFile()
	first, err := lockfile.EncodeLock(lf)
	if err != nil {
		t.Fatalf("EncodeLock (first): %v", err)
	}

	for i := 1; i < 100; i++ {
		got, err := lockfile.EncodeLock(lf)
		if err != nil {
			t.Fatalf("EncodeLock (iter %d): %v", i, err)
		}
		if string(got) != string(first) {
			t.Fatalf("EncodeLock is not deterministic (iter %d).\nfirst:\n%s\ngot:\n%s", i, first, got)
		}
	}
}

// --- BuildID round-trip ---

func TestBuildID_ResolveID_RoundTrip(t *testing.T) {
	eco, grp, ver := "kubernetes", "operator.victoriametrics.com", "0.70.0"
	id := lockfile.BuildID(eco, grp, ver)
	gotEco, gotGrp, gotVer, err := lockfile.ResolveID(id)
	if err != nil {
		t.Fatalf("ResolveID(%q): %v", id, err)
	}
	if gotEco != eco || gotGrp != grp || gotVer != ver {
		t.Errorf("round-trip mismatch: got (%q, %q, %q), want (%q, %q, %q)",
			gotEco, gotGrp, gotVer, eco, grp, ver)
	}
}

// --- DecodeLock malformed YAML ---

func TestDecodeLock_MalformedYAML(t *testing.T) {
	_, err := lockfile.DecodeLock([]byte("{\nbad yaml ["))
	if err == nil {
		t.Fatal("expected error for malformed YAML, got nil")
	}
	if !errors.Is(err, lockfile.ErrMalformedLock) {
		t.Errorf("expected ErrMalformedLock, got: %v", err)
	}
}

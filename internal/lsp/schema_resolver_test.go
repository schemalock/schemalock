package lsp_test

import (
	"errors"
	"sync"
	"testing"

	"github.com/schemalock/app/internal/lockfile"
	"github.com/schemalock/app/internal/lsp"
)

// minimalLockFile builds a LockFile with the given entries for tests.
func minimalLockFile(entries []lockfile.LockEntry) lockfile.LockFile {
	return lockfile.LockFile{
		Version:     1,
		Registry:    "https://cdn.schemalock.dev",
		GeneratedAt: "2026-01-01T00:00:00Z",
		Entries:     entries,
	}
}

// TestNewSchemaResolver_AmbiguousKind asserts that construction returns
// ErrAmbiguousKind when two entries share the same (group, kind) at different
// release versions (e.g., two operator releases both publishing VMCluster).
func TestNewSchemaResolver_AmbiguousKind(t *testing.T) {
	lf := minimalLockFile([]lockfile.LockEntry{
		{
			ID:   "kubernetes/operator.victoriametrics.com@0.70.0",
			Base: "kubernetes/operator.victoriametrics.com/0.70.0/",
			Schemas: map[string]lockfile.SchemaEntry{
				"VMCluster": {Integrity: "sha256-aaa", Size: 100},
			},
		},
		{
			ID:   "kubernetes/operator.victoriametrics.com@0.71.0",
			Base: "kubernetes/operator.victoriametrics.com/0.71.0/",
			Schemas: map[string]lockfile.SchemaEntry{
				// Same group+kind as the entry above — this should be rejected.
				"VMCluster": {Integrity: "sha256-bbb", Size: 200},
			},
		},
	})

	_, err := lsp.NewSchemaResolver(lf)
	if err == nil {
		t.Fatal("expected ErrAmbiguousKind, got nil")
	}
	if !errors.Is(err, lsp.ErrAmbiguousKind) {
		t.Errorf("expected errors.Is(err, ErrAmbiguousKind), got: %v", err)
	}
}

// TestNewSchemaResolver_Clean asserts that a well-formed lockfile with no
// duplicates constructs cleanly and Resolve returns the expected entry for a
// known (apiVersion, kind) pair.
func TestNewSchemaResolver_Clean(t *testing.T) {
	lf := minimalLockFile([]lockfile.LockEntry{
		{
			ID:   "kubernetes/operator.victoriametrics.com@0.70.0",
			Base: "kubernetes/operator.victoriametrics.com/0.70.0/",
			Schemas: map[string]lockfile.SchemaEntry{
				"VMCluster": {Integrity: "sha256-aaa", Size: 100},
			},
		},
	})

	r, err := lsp.NewSchemaResolver(lf)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	entry, err := r.Resolve("operator.victoriametrics.com/v1beta1", "VMCluster")
	if err != nil {
		t.Fatalf("Resolve returned unexpected error: %v", err)
	}
	if entry.ReleaseVersion != "0.70.0" {
		t.Errorf("ReleaseVersion = %q, want %q", entry.ReleaseVersion, "0.70.0")
	}
	if entry.Kind != "VMCluster" {
		t.Errorf("Kind = %q, want %q", entry.Kind, "VMCluster")
	}
	if entry.Integrity != "sha256-aaa" {
		t.Errorf("Integrity = %q, want %q", entry.Integrity, "sha256-aaa")
	}
}

// TestKindsForGroup covers the KindsForGroup method across a variety of
// resolver states and query inputs.
func TestKindsForGroup(t *testing.T) {
	// Helper: build a two-kind entry for the given group.
	twoKindEntry := func(group, kindA, kindB string) lockfile.LockEntry {
		return lockfile.LockEntry{
			ID:   "kubernetes/" + group + "@0.1.0",
			Base: "kubernetes/" + group + "/0.1.0/",
			Schemas: map[string]lockfile.SchemaEntry{
				kindA: {Integrity: "sha256-aaa", Size: 100},
				kindB: {Integrity: "sha256-bbb", Size: 200},
			},
		}
	}

	tests := []struct {
		name    string
		entries []lockfile.LockEntry
		query   string
		want    []string // nil means expect nil; non-nil means exact sorted slice
	}{
		{
			name:    "EmptyResolver",
			entries: nil,
			query:   "anything",
			want:    []string{},
		},
		{
			name: "EmptyGroupString",
			entries: []lockfile.LockEntry{
				twoKindEntry("operator.victoriametrics.com", "VMAgent", "VMCluster"),
			},
			query: "",
			want:  nil,
		},
		{
			name: "SingleGroupTwoKinds",
			entries: []lockfile.LockEntry{
				twoKindEntry("operator.victoriametrics.com", "VMCluster", "VMAgent"),
			},
			query: "operator.victoriametrics.com",
			want:  []string{"VMAgent", "VMCluster"},
		},
		{
			name: "ThreeGroupsEachTwoKinds_GroupA",
			entries: []lockfile.LockEntry{
				twoKindEntry("a.example.com", "AlphaOne", "AlphaTwo"),
				twoKindEntry("b.example.com", "BetaOne", "BetaTwo"),
				twoKindEntry("c.example.com", "GammaOne", "GammaTwo"),
			},
			query: "a.example.com",
			want:  []string{"AlphaOne", "AlphaTwo"},
		},
		{
			name: "ThreeGroupsEachTwoKinds_GroupB",
			entries: []lockfile.LockEntry{
				twoKindEntry("a.example.com", "AlphaOne", "AlphaTwo"),
				twoKindEntry("b.example.com", "BetaOne", "BetaTwo"),
				twoKindEntry("c.example.com", "GammaOne", "GammaTwo"),
			},
			query: "b.example.com",
			want:  []string{"BetaOne", "BetaTwo"},
		},
		{
			name: "ThreeGroupsEachTwoKinds_GroupC",
			entries: []lockfile.LockEntry{
				twoKindEntry("a.example.com", "AlphaOne", "AlphaTwo"),
				twoKindEntry("b.example.com", "BetaOne", "BetaTwo"),
				twoKindEntry("c.example.com", "GammaOne", "GammaTwo"),
			},
			query: "c.example.com",
			want:  []string{"GammaOne", "GammaTwo"},
		},
		{
			name: "UnknownGroup",
			entries: []lockfile.LockEntry{
				twoKindEntry("a.example.com", "AlphaOne", "AlphaTwo"),
			},
			query: "missing.example.com",
			want:  []string{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var entries []lockfile.LockEntry
			if tc.entries != nil {
				entries = tc.entries
			}
			r, err := lsp.NewSchemaResolver(minimalLockFile(entries))
			if err != nil {
				t.Fatalf("NewSchemaResolver: %v", err)
			}

			got := r.KindsForGroup(tc.query)

			if tc.want == nil {
				if got != nil {
					t.Errorf("KindsForGroup(%q) = %v, want nil", tc.query, got)
				}
				return
			}

			if len(got) != len(tc.want) {
				t.Fatalf("KindsForGroup(%q) = %v (len %d), want %v (len %d)",
					tc.query, got, len(got), tc.want, len(tc.want))
			}
			for i, k := range tc.want {
				if got[i] != k {
					t.Errorf("KindsForGroup(%q)[%d] = %q, want %q", tc.query, i, got[i], k)
				}
			}
		})
	}
}

// TestKindsForGroup_ConcurrentReadReload is a smoke test that spawns multiple
// goroutines calling KindsForGroup concurrently with one goroutine calling
// Reload. Run under -race.
func TestKindsForGroup_ConcurrentReadReload(t *testing.T) {
	entry := lockfile.LockEntry{
		ID:   "kubernetes/operator.victoriametrics.com@0.70.0",
		Base: "kubernetes/operator.victoriametrics.com/0.70.0/",
		Schemas: map[string]lockfile.SchemaEntry{
			"VMCluster": {Integrity: "sha256-aaa", Size: 100},
			"VMAgent":   {Integrity: "sha256-bbb", Size: 200},
		},
	}
	lf := minimalLockFile([]lockfile.LockEntry{entry})

	r, err := lsp.NewSchemaResolver(lf)
	if err != nil {
		t.Fatalf("NewSchemaResolver: %v", err)
	}

	const readers = 8
	const readsPerGoroutine = 100
	const reloads = 50

	var wg sync.WaitGroup

	// Spawn reader goroutines.
	for range readers {
		wg.Go(func() {
			for range readsPerGoroutine {
				kinds := r.KindsForGroup("operator.victoriametrics.com")
				// Result must always be one of: nil/empty, ["VMAgent","VMCluster"].
				if len(kinds) != 0 && len(kinds) != 2 {
					t.Errorf("unexpected kinds length %d: %v", len(kinds), kinds)
				}
				if len(kinds) == 2 {
					if kinds[0] != "VMAgent" || kinds[1] != "VMCluster" {
						t.Errorf("unexpected kind ordering: %v", kinds)
					}
				}
			}
		})
	}

	// Spawn one reloader goroutine.
	wg.Go(func() {
		for range reloads {
			if err := r.Reload(lf); err != nil {
				t.Errorf("Reload: %v", err)
			}
		}
	})

	wg.Wait()
}

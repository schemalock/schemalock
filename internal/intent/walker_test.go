package intent

import (
	"path/filepath"
	"testing"
)

func TestWalkUp_rootOnly_returnsSingleEntry(t *testing.T) {
	start := absFixture(t, "testdata/repo/teamB/k8s")
	chain, err := WalkUp(start)
	if err != nil {
		t.Fatalf("WalkUp: %v", err)
	}
	if len(chain) != 1 {
		t.Fatalf("len(chain) = %d, want 1; chain=%v", len(chain), pathsOf(chain))
	}
	rootPath := absFixture(t, "testdata/repo/schemalock.yaml")
	if chain[0].Path != rootPath {
		t.Errorf("chain[0].Path = %s, want %s", chain[0].Path, rootPath)
	}
	if chain[0].File.Version != 1 {
		t.Errorf("root file Version = %d, want 1", chain[0].File.Version)
	}
}

func TestWalkUp_rootPlusOverlay_returnsRootFirst(t *testing.T) {
	start := absFixture(t, "testdata/repo/teamA/k8s")
	chain, err := WalkUp(start)
	if err != nil {
		t.Fatalf("WalkUp: %v", err)
	}
	if len(chain) != 2 {
		t.Fatalf("len(chain) = %d, want 2; chain=%v", len(chain), pathsOf(chain))
	}
	rootPath := absFixture(t, "testdata/repo/schemalock.yaml")
	overlayPath := absFixture(t, "testdata/repo/teamA/schemalock.yaml")
	if chain[0].Path != rootPath {
		t.Errorf("chain[0] = %s, want %s (root first)", chain[0].Path, rootPath)
	}
	if chain[1].Path != overlayPath {
		t.Errorf("chain[1] = %s, want %s", chain[1].Path, overlayPath)
	}
}

func TestWalkUp_rootTrueHalts(t *testing.T) {
	// sub-project sits at testdata/sub-project; the parent dir testdata/
	// does not contain a schemalock.yaml, but even if it did, root: true
	// must halt the walk.
	start := absFixture(t, "testdata/sub-project/deep")
	chain, err := WalkUp(start)
	if err != nil {
		t.Fatalf("WalkUp: %v", err)
	}
	if len(chain) != 1 {
		t.Fatalf("len(chain) = %d, want 1 (root: true must halt)", len(chain))
	}
	wantPath := absFixture(t, "testdata/sub-project/schemalock.yaml")
	if chain[0].Path != wantPath {
		t.Errorf("chain[0] = %s, want %s", chain[0].Path, wantPath)
	}
	if !chain[0].File.Root {
		t.Errorf("chain[0].File.Root = false, want true")
	}
}

func TestWalkUp_noFiles_returnsEmpty(t *testing.T) {
	dir := t.TempDir()
	chain, err := WalkUp(dir)
	if err != nil {
		t.Fatalf("WalkUp: %v", err)
	}
	if len(chain) != 0 {
		t.Errorf("len(chain) = %d, want 0", len(chain))
	}
}

func absFixture(t *testing.T, rel string) string {
	t.Helper()
	p, err := filepath.Abs(rel)
	if err != nil {
		t.Fatalf("filepath.Abs: %v", err)
	}
	return p
}

func pathsOf(chain []LoadedFile) []string {
	out := make([]string, len(chain))
	for i, c := range chain {
		out[i] = c.Path
	}
	return out
}

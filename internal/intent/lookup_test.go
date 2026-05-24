package intent

import (
	"sync"
	"testing"
)

func TestLookup_pinForFromRootOnly(t *testing.T) {
	l := NewLookup()
	dir := absFixture(t, "testdata/repo/teamB/k8s")
	version, ok, err := l.PinFor(dir, "kubernetes", "cert-manager.io")
	if err != nil {
		t.Fatalf("PinFor: %v", err)
	}
	if !ok || version != "v1.16.1" {
		t.Errorf("PinFor cert-manager.io = (%q, %v), want (v1.16.1, true)", version, ok)
	}
}

func TestLookup_pinForFromOverlayWins(t *testing.T) {
	l := NewLookup()
	dir := absFixture(t, "testdata/repo/teamA/k8s")
	version, ok, err := l.PinFor(dir, "kubernetes", "operator.victoriametrics.com")
	if err != nil {
		t.Fatalf("PinFor: %v", err)
	}
	if !ok || version != "0.69.0" {
		t.Errorf("PinFor vm = (%q, %v), want (0.69.0, true)", version, ok)
	}
}

func TestLookup_pinForUnknownNameReturnsNotOk(t *testing.T) {
	l := NewLookup()
	dir := absFixture(t, "testdata/repo/teamB/k8s")
	version, ok, err := l.PinFor(dir, "kubernetes", "not-pinned.io")
	if err != nil {
		t.Fatalf("PinFor: %v", err)
	}
	if ok {
		t.Errorf("PinFor unknown = (%q, true), want not ok", version)
	}
}

func TestLookup_cacheReuse(t *testing.T) {
	l := NewLookup()
	dir := absFixture(t, "testdata/repo/teamA/k8s")
	if _, _, err := l.PinFor(dir, "kubernetes", "cert-manager.io"); err != nil {
		t.Fatalf("PinFor: %v", err)
	}
	if l.entries() == 0 {
		t.Errorf("entries = 0 after first PinFor, want >= 1")
	}
}

func TestLookup_invalidate(t *testing.T) {
	l := NewLookup()
	dir := absFixture(t, "testdata/repo/teamA/k8s")
	if _, _, err := l.PinFor(dir, "kubernetes", "cert-manager.io"); err != nil {
		t.Fatalf("PinFor: %v", err)
	}
	l.Invalidate()
	if l.entries() != 0 {
		t.Errorf("entries after Invalidate = %d, want 0", l.entries())
	}
}

func TestLookup_concurrent(t *testing.T) {
	l := NewLookup()
	dirs := []string{
		absFixture(t, "testdata/repo/teamA/k8s"),
		absFixture(t, "testdata/repo/teamB/k8s"),
	}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(d string) {
			defer wg.Done()
			if _, _, err := l.PinFor(d, "kubernetes", "cert-manager.io"); err != nil {
				t.Errorf("PinFor: %v", err)
			}
		}(dirs[i%len(dirs)])
	}
	wg.Wait()
}

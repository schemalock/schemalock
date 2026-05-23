package lsp

import (
	"sync"
	"testing"
)

func TestOverrideStore_SetGetClear(t *testing.T) {
	s := NewOverrideStore()

	if _, ok := s.Get("file:///a.yaml"); ok {
		t.Fatal("empty store should not return entries")
	}

	s.Set("file:///a.yaml", "operator.victoriametrics.com", "0.68.4")
	got, ok := s.Get("file:///a.yaml")
	if !ok {
		t.Fatal("expected entry after Set")
	}
	if got.Group != "operator.victoriametrics.com" || got.Version != "0.68.4" {
		t.Fatalf("got %+v", got)
	}

	s.Clear("file:///a.yaml")
	if _, ok := s.Get("file:///a.yaml"); ok {
		t.Fatal("expected no entry after Clear")
	}
}

func TestOverrideStore_Concurrent(t *testing.T) {
	s := NewOverrideStore()
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.Set("file:///a.yaml", "g", "v")
			s.Get("file:///a.yaml")
			s.Clear("file:///a.yaml")
		}()
	}
	wg.Wait()
}

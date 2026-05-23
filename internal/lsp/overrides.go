package lsp

import "sync"

// Override is a per-document version pin set by the user (via the editor
// picker) that is not persisted to disk and resets on server restart.
type Override struct {
	Group   string
	Version string
}

// OverrideStore is a concurrency-safe URI -> Override map. It carries the
// "preview this version" decision the user made through the status-bar picker.
// Entries are cleared on didClose for the URI; the entire store resets on
// server restart.
type OverrideStore struct {
	mu sync.RWMutex
	m  map[string]Override
}

// NewOverrideStore returns an empty OverrideStore ready for concurrent use.
func NewOverrideStore() *OverrideStore {
	return &OverrideStore{m: make(map[string]Override)}
}

// Set records an override for uri.
func (s *OverrideStore) Set(uri, group, version string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[uri] = Override{Group: group, Version: version}
}

// Get returns the override for uri, if any.
func (s *OverrideStore) Get(uri string) (Override, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	o, ok := s.m[uri]
	return o, ok
}

// Clear removes the override for uri. No-op when uri has no override.
func (s *OverrideStore) Clear(uri string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, uri)
}

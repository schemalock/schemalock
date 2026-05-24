package intent

import (
	"fmt"
	"sync"
)

// Lookup wraps WalkUp + Merge with a per-directory cache so the LSP and CLI
// can answer "what version of <group> is pinned for files in <dir>?" without
// repeatedly walking the filesystem.
//
// The cache is keyed by absolute directory path. Invalidate clears all
// entries; granular invalidation (per directory) is not yet needed and can
// be added when the LSP grows file-watcher integration.
//
// Lookup is safe for concurrent use.
type Lookup struct {
	mu    sync.RWMutex
	cache map[string]IntentSet // absolute dir → effective set
}

// NewLookup constructs an empty Lookup with an initialised cache.
func NewLookup() *Lookup {
	return &Lookup{cache: make(map[string]IntentSet)}
}

// PinFor returns the pinned version for (ecosystem, name) effective in dir.
// Returns ("", false, nil) if no pin matches; ("", false, err) if the
// hierarchy walk or merge surfaces an error.
func (l *Lookup) PinFor(dir, ecosystem, name string) (version string, ok bool, err error) {
	set, err := l.setFor(dir)
	if err != nil {
		return "", false, err
	}
	if eco, found := set[ecosystem]; found {
		if v, vok := eco[name]; vok {
			return v, true, nil
		}
	}
	return "", false, nil
}

// SetFor returns the effective IntentSet for dir, going through the cache.
// Exposed for callers (e.g. verify) that need to iterate all pins.
func (l *Lookup) SetFor(dir string) (IntentSet, error) {
	return l.setFor(dir)
}

func (l *Lookup) setFor(dir string) (IntentSet, error) {
	l.mu.RLock()
	if set, ok := l.cache[dir]; ok {
		l.mu.RUnlock()
		return set, nil
	}
	l.mu.RUnlock()

	chain, err := WalkUp(dir)
	if err != nil {
		return nil, fmt.Errorf("walking intent hierarchy from %s: %w", dir, err)
	}
	set, err := Merge(chain)
	if err != nil {
		return nil, fmt.Errorf("merging intent chain for %s: %w", dir, err)
	}

	// Two goroutines may reach here concurrently; last writer wins, no corruption.
	l.mu.Lock()
	l.cache[dir] = set
	l.mu.Unlock()
	return set, nil
}

// Invalidate clears the entire per-directory cache. Intended for LSP
// reload-on-edit triggers.
func (l *Lookup) Invalidate() {
	l.mu.Lock()
	l.cache = make(map[string]IntentSet)
	l.mu.Unlock()
}

// entries is a test-only accessor for cache size assertions.
func (l *Lookup) entries() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return len(l.cache)
}

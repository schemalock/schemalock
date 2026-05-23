package lsp

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/schemalock/app/internal/lockfile"
)

// resolvedEntry holds the fully-qualified information needed to fetch and
// verify one schema from the cache or registry.
type resolvedEntry struct {
	// Ecosystem is the ecosystem name (e.g. "kubernetes").
	Ecosystem string
	// Group is the API group (e.g. "operator.victoriametrics.com").
	Group string
	// ReleaseVersion is the operator release version (e.g. "0.70.0").
	ReleaseVersion string
	// Kind is the CRD Kind name (e.g. "VMCluster").
	Kind string
	// Integrity is the SRI hash of the schema file.
	Integrity string
	// Size is the byte size of the schema file.
	Size int
}

// groupKindKey is the lazy-index key. It captures the API group (from
// apiVersion's left side of "/") and the Kind name.
// Two lock entries with the same group+kind are ambiguous (ErrAmbiguousKind).
type groupKindKey struct {
	Group string
	Kind  string
}

// SchemaResolver maps (apiVersion, kind) pairs to resolved lock entries using
// the lazy-index strategy recorded in .claude/memory/decisions.md:
//
//  1. Build a (group, kind) → resolvedEntry index at construction time, using
//     only the information already present in the lock file (no network I/O).
//  2. On Resolve(apiVersion, kind): split apiVersion on "/" to extract group;
//     look up (group, kind) in the index.
//  3. Cache the (apiVersion, kind) → resolvedEntry mapping after the first
//     successful resolution so subsequent calls are O(1).
//
// SchemaResolver is safe for concurrent use.
type SchemaResolver struct {
	mu    sync.RWMutex
	index map[groupKindKey]resolvedEntry // (group, kind) → entry; built from lock file
	cache map[string]resolvedEntry       // "apiVersion\x00kind" → entry; populated lazily
}

// NewSchemaResolver builds a [SchemaResolver] from a parsed lock file.
// It returns [ErrAmbiguousKind] if two entries share the same (group, kind)
// at different release versions.
func NewSchemaResolver(lf lockfile.LockFile) (*SchemaResolver, error) {
	index := make(map[groupKindKey]resolvedEntry)

	for _, entry := range lf.Entries {
		ecosystem, group, releaseVersion, err := lockfile.ResolveID(entry.ID)
		if err != nil {
			return nil, fmt.Errorf("schema resolver: malformed lock entry ID %q: %w", entry.ID, err)
		}

		for kind, schema := range entry.Schemas {
			key := groupKindKey{Group: group, Kind: kind}
			if existing, conflict := index[key]; conflict {
				return nil, fmt.Errorf("%w: (%s, %s) found in both %s and %s",
					ErrAmbiguousKind, group, kind, existing.ReleaseVersion, releaseVersion)
			}
			index[key] = resolvedEntry{
				Ecosystem:      ecosystem,
				Group:          group,
				ReleaseVersion: releaseVersion,
				Kind:           kind,
				Integrity:      schema.Integrity,
				Size:           schema.Size,
			}
		}
	}

	return &SchemaResolver{
		index: index,
		cache: make(map[string]resolvedEntry),
	}, nil
}

// Reload atomically swaps the lock file and rebuilds the lazy index.
// It returns [ErrAmbiguousKind] under the same conditions as [NewSchemaResolver].
// After a successful Reload, the resolution cache is cleared so that stale
// (apiVersion, kind) mappings do not survive a lock-file update.
func (r *SchemaResolver) Reload(lf lockfile.LockFile) error {
	nr, err := NewSchemaResolver(lf)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.index = nr.index
	r.cache = make(map[string]resolvedEntry)
	return nil
}

// Resolve maps an apiVersion + kind pair to the lock entry that should be used
// to fetch and validate the document.
//
// apiVersion must have the form "<group>/<version>" (e.g.
// "operator.victoriametrics.com/v1beta1"). Core Kubernetes types without a
// group prefix (e.g. bare "v1") return [ErrNoMatch] because they are out of
// scope for the PoC.
//
// Ambiguity (two entries sharing the same group+kind) is detected at
// construction time by [NewSchemaResolver] and causes that call to fail with
// [ErrAmbiguousKind]. Resolve itself only returns [ErrNoMatch] (no match) or
// nil (success).
func (r *SchemaResolver) Resolve(apiVersion, kind string) (resolvedEntry, error) {
	cacheKey := apiVersion + "\x00" + kind

	r.mu.RLock()
	if entry, ok := r.cache[cacheKey]; ok {
		r.mu.RUnlock()
		return entry, nil
	}
	r.mu.RUnlock()

	// Split apiVersion into group and version suffix.
	group, _, hasSlash := strings.Cut(apiVersion, "/")
	if !hasSlash {
		// Core Kubernetes types (bare "v1") — not in scope for the PoC.
		return resolvedEntry{}, fmt.Errorf("%w: %q/%s (core types not supported)", ErrNoMatch, apiVersion, kind)
	}

	key := groupKindKey{Group: group, Kind: kind}

	r.mu.Lock()
	defer r.mu.Unlock()

	entry, ok := r.index[key]
	if !ok {
		return resolvedEntry{}, fmt.Errorf("%w: apiVersion=%q kind=%q", ErrNoMatch, apiVersion, kind)
	}

	// Cache for subsequent calls.
	r.cache[cacheKey] = entry
	return entry, nil
}

// KindsForGroup returns the sorted list of Kind names available in the lock
// file for the given API group (e.g. "operator.victoriametrics.com"). Returns
// nil when group is empty. Returns an empty (non-nil) slice when the group is
// present in the index but matched no entries.
//
// Safe for concurrent use.
func (r *SchemaResolver) KindsForGroup(group string) []string {
	if group == "" {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0)
	for key := range r.index {
		if key.Group == group {
			out = append(out, key.Kind)
		}
	}
	sort.Strings(out)
	return out
}

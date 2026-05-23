package lsp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/schemalock/app/internal/registry"
	"github.com/schemalock/app/internal/yamldoc"
)

// State is the resolution outcome surfaced to the editor.
type State int

const (
	// StateUnindexable means the document has no apiVersion+kind, or the group
	// is not indexed by the CDN or lockfile.
	StateUnindexable State = iota
	// StatePinned means the schema came from a lockfile-pinned entry and was
	// integrity-verified.
	StatePinned
	// StateUnpinned means the schema came from the CDN's latest version with no
	// lockfile pin.
	StateUnpinned
	// StatePreview means the schema came from a per-document override version.
	StatePreview
	// StateError means a resolution step failed (integrity mismatch, network
	// error, etc.). ErrMsg carries the details.
	StateError
)

// ResolveResult is the full per-URI resolution returned by the chain.
// Schema is nil for StateUnindexable and StateError.
type ResolveResult struct {
	State     State
	Group     string
	Kind      string
	Version   string
	Source    string // "lockfile", "override", "cdn-latest", "cdn-error", ""
	Schema    []byte
	Integrity string
	ErrMsg    string // non-empty when State == StateError; surfaced in tooltip
}

// resolverChain composes the three sub-resolvers: lockfile-pinned,
// per-document override, and CDN fallback.
type resolverChain struct {
	lockResolver    *SchemaResolver
	overrides       *OverrideStore
	cdn             *cdnResolver
	fallbackEnabled bool
	cacheDir        string // for pinned schema disk reads (matches cdn.cacheDir)
}

// newResolverChain constructs the chain. lock, ov, and cdn may be nil; the
// corresponding resolution step is skipped. fallbackEnabled guards the CDN
// fallback path (step 3).
func newResolverChain(lock *SchemaResolver, ov *OverrideStore, cdn *cdnResolver, fallbackEnabled bool, cacheDir string) *resolverChain {
	return &resolverChain{
		lockResolver:    lock,
		overrides:       ov,
		cdn:             cdn,
		fallbackEnabled: fallbackEnabled,
		cacheDir:        cacheDir,
	}
}

// Resolve runs the chain for a single (uri, text) pair.
//
// Resolution order (most-specific user intent wins):
//  1. Per-document override — explicit picker selection, highest priority so
//     the user can preview a non-pinned version even when the lockfile has an
//     entry for this (group, kind).
//  2. Lockfile — integrity-verified pinned schema from disk (or CDN re-fetch).
//  3. CDN fallback — latest version fetched from CDN.
func (c *resolverChain) Resolve(ctx context.Context, uri, text string) ResolveResult {
	// Parse the document to extract apiVersion and kind.
	docs, err := yamldoc.Parse([]byte(text))
	if err != nil || len(docs) == 0 {
		return ResolveResult{State: StateUnindexable}
	}
	doc := docs[0]
	apiVersion := doc.APIVersion
	kind := doc.Kind
	if apiVersion == "" || kind == "" {
		return ResolveResult{State: StateUnindexable}
	}
	group := groupFromAPIVersion(apiVersion)
	if group == "" {
		return ResolveResult{State: StateUnindexable}
	}

	// 1. Per-document override (preview). Explicit session intent: this beats
	// the lockfile so the user can audit a different version against a
	// committed pin without removing the pin.
	if c.overrides != nil && c.cdn != nil {
		if o, ok := c.overrides.Get(uri); ok && o.Group == group {
			res, err := c.cdn.ResolveAt(ctx, "kubernetes", group, kind, o.Version)
			if err == nil {
				return ResolveResult{
					State:     StatePreview,
					Group:     group,
					Kind:      kind,
					Version:   o.Version,
					Source:    "override",
					Schema:    res.SchemaBytes,
					Integrity: res.Integrity,
				}
			}
			return ResolveResult{
				State:   StateError,
				Group:   group,
				Kind:    kind,
				Version: o.Version,
				Source:  "cdn-error",
				ErrMsg:  err.Error(),
			}
		}
	}

	// 2. Lockfile.
	if c.lockResolver != nil {
		if entry, err := c.lockResolver.Resolve(apiVersion, kind); err == nil {
			// Read schema bytes from disk; verify integrity against lockfile hash.
			path := filepath.Join(c.cacheDir, entry.Ecosystem, entry.Group, entry.ReleaseVersion, entry.Kind+".json")
			if body, err := os.ReadFile(path); err == nil {
				if vErr := registry.VerifyIntegrity(body, entry.Integrity); vErr == nil {
					return ResolveResult{
						State:     StatePinned,
						Group:     entry.Group,
						Kind:      entry.Kind,
						Version:   entry.ReleaseVersion,
						Source:    "lockfile",
						Schema:    body,
						Integrity: entry.Integrity,
					}
				}
				// Integrity mismatch — surface as Error.
				return ResolveResult{
					State:   StateError,
					Group:   entry.Group,
					Kind:    entry.Kind,
					Version: entry.ReleaseVersion,
					Source:  "lockfile",
					ErrMsg:  "integrity mismatch on cached schema",
				}
			}
			// Disk miss — try to fetch via CDN's ResolveAt (which writes back to cache).
			if c.cdn != nil {
				res, err := c.cdn.ResolveAt(ctx, entry.Ecosystem, entry.Group, entry.Kind, entry.ReleaseVersion)
				if err == nil {
					return ResolveResult{
						State:     StatePinned,
						Group:     entry.Group,
						Kind:      entry.Kind,
						Version:   entry.ReleaseVersion,
						Source:    "lockfile",
						Schema:    res.SchemaBytes,
						Integrity: res.Integrity,
					}
				}
				return ResolveResult{
					State:   StateError,
					Group:   entry.Group,
					Kind:    entry.Kind,
					Version: entry.ReleaseVersion,
					Source:  "lockfile",
					ErrMsg:  err.Error(),
				}
			}
			return ResolveResult{
				State:   StateError,
				Group:   entry.Group,
				Kind:    entry.Kind,
				Version: entry.ReleaseVersion,
				Source:  "lockfile",
				ErrMsg:  "schema not in disk cache and no CDN configured",
			}
		}
	}

	// 3. CDN fallback.
	if c.fallbackEnabled && c.cdn != nil {
		res, err := c.cdn.Resolve(ctx, "kubernetes", group, kind)
		if err == nil {
			return ResolveResult{
				State:     StateUnpinned,
				Group:     group,
				Kind:      kind,
				Version:   res.Version,
				Source:    "cdn-latest",
				Schema:    res.SchemaBytes,
				Integrity: res.Integrity,
			}
		}
		if errors.Is(err, registry.ErrNotFound) {
			return ResolveResult{State: StateUnindexable, Group: group, Kind: kind}
		}
		return ResolveResult{State: StateError, Group: group, Kind: kind, Source: "cdn-error", ErrMsg: err.Error()}
	}

	return ResolveResult{State: StateUnindexable, Group: group, Kind: kind}
}

// groupFromAPIVersion returns the group portion of apiVersion (everything
// before "/"). For core Kubernetes resources like "v1" (no slash) returns "".
func groupFromAPIVersion(apiVersion string) string {
	group, _, ok := strings.Cut(apiVersion, "/")
	if !ok {
		return ""
	}
	return group
}

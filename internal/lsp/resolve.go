package lsp

import (
	"context"
	"errors"
	"path/filepath"
	"strings"

	"github.com/schemalock/app/internal/intent"
	"github.com/schemalock/app/internal/registry"
	"github.com/schemalock/app/internal/yamldoc"
)

// State is the resolution outcome surfaced to the editor.
type State int

const (
	// StateUnindexable means the document has no apiVersion+kind, or the group
	// is not indexed by the CDN or intent file.
	StateUnindexable State = iota
	// StatePinned means the schema came from an intent-pinned entry.
	StatePinned
	// StateUnpinned means the schema came from the CDN's latest version with no
	// intent pin.
	StateUnpinned
	// StatePreview means the schema came from a per-document override version.
	StatePreview
	// StateError means a resolution step failed (network error, etc.).
	// ErrMsg carries the details.
	StateError
)

// ResolveResult is the full per-URI resolution returned by the chain.
// Schema is nil for StateUnindexable and StateError.
type ResolveResult struct {
	State     State
	Group     string
	Kind      string
	Version   string
	Source    string // "intent", "override", "cdn-latest", "cdn-error", ""
	Schema    []byte
	Integrity string
	ErrMsg    string // non-empty when State == StateError; surfaced in tooltip
}

// resolverChain composes the three sub-resolvers: intent-pinned,
// per-document override, and CDN fallback.
type resolverChain struct {
	intentLookup    *intent.Lookup
	overrides       *OverrideStore
	cdn             *cdnResolver
	fallbackEnabled bool
}

// newResolverChain constructs the chain. il, ov, and cdn may be nil; the
// corresponding resolution step is skipped. fallbackEnabled guards the CDN
// fallback path (step 3).
func newResolverChain(il *intent.Lookup, ov *OverrideStore, cdn *cdnResolver, fallbackEnabled bool) *resolverChain {
	return &resolverChain{
		intentLookup:    il,
		overrides:       ov,
		cdn:             cdn,
		fallbackEnabled: fallbackEnabled,
	}
}

// Resolve runs the chain for a single (uri, text) pair.
//
// Resolution order (most-specific user intent wins):
//  1. Per-document override — explicit picker selection, highest priority so
//     the user can preview a non-pinned version even when the intent has an
//     entry for this group.
//  2. Intent-pinned — version pinned by the nearest schemalock.yaml hierarchy
//     for the document's directory. Fetched via cdn.ResolveAt.
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
	// the intent pin so the user can audit a different version against a
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

	// 2. Intent-pinned: look up the pinned version for this document's directory.
	if c.intentLookup != nil && c.cdn != nil {
		dir := dirFromURI(uri)
		if dir != "" {
			version, ok, lookupErr := c.intentLookup.PinFor(dir, "kubernetes", group)
			if lookupErr == nil && ok {
				res, err := c.cdn.ResolveAt(ctx, "kubernetes", group, kind, version)
				if err == nil {
					return ResolveResult{
						State:     StatePinned,
						Group:     group,
						Kind:      kind,
						Version:   version,
						Source:    "intent",
						Schema:    res.SchemaBytes,
						Integrity: res.Integrity,
					}
				}
				return ResolveResult{
					State:   StateError,
					Group:   group,
					Kind:    kind,
					Version: version,
					Source:  "cdn-error",
					ErrMsg:  err.Error(),
				}
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

// dirFromURI converts a file:// document URI to its containing directory.
// Returns "" for non-file URIs.
func dirFromURI(docURI string) string {
	const prefix = "file://"
	if !strings.HasPrefix(docURI, prefix) {
		return ""
	}
	path := docURI[len(prefix):]
	if path == "" {
		return ""
	}
	return filepath.Dir(path)
}

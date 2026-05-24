// Package lsp — CLI-facing adapter over the internal resolverChain.
//
// LSP consumers use newResolverChain directly (file-private). CLI consumers
// (currently `schemalock verify`) construct a [Resolver] through
// NewCLIResolver, which wires the chain with an intent.Lookup, a CDN client,
// no override store, and CDN fallback enabled.
package lsp

import (
	"context"
	"time"

	"github.com/schemalock/app/internal/cache"
	"github.com/schemalock/app/internal/intent"
	"github.com/schemalock/app/internal/registry"
)

// Resolver is the exported facade over resolverChain for one-shot CLI
// invocations. The underlying chain is stateless across documents — pin
// lookups go through the embedded *intent.Lookup, which caches per
// directory.
type Resolver struct {
	chain *resolverChain
}

// NewCLIResolver constructs a Resolver wired with intent.Lookup, a CDN
// client at registryURL, no override store, and fallback enabled. The
// on-disk schema cache lives at ~/.schemalock/cache (via cache.DefaultRoot).
func NewCLIResolver(registryURL string) (*Resolver, error) {
	root, err := cache.DefaultRoot()
	if err != nil {
		return nil, err
	}
	return NewCLIResolverWithCacheDir(registryURL, root), nil
}

// NewCLIResolverWithCacheDir is the test-friendly variant that lets callers
// supply an explicit cache directory instead of ~/.schemalock/cache.
func NewCLIResolverWithCacheDir(registryURL, cacheDir string) *Resolver {
	cli := registry.NewClient(registryURL)
	cdn := newCDNResolver(cli, 5*time.Minute, 24*time.Hour, 30*time.Second)
	cdn.cacheDir = cacheDir
	chain := newResolverChain(intent.NewLookup(), nil, cdn, true)
	return &Resolver{chain: chain}
}

// Resolve runs the chain for a single (uri, text) pair. Mirrors the LSP
// resolverChain semantics: per-doc override → intent-pinned → CDN-latest.
func (r *Resolver) Resolve(ctx context.Context, uri, text string) ResolveResult {
	return r.chain.Resolve(ctx, uri, text)
}

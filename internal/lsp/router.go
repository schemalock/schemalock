package lsp

import (
	"context"
	"sync"
)

// Router decides per-URI resolution by delegating to a resolverChain. It
// caches the ResolveResult per URI so repeated hover/completion requests
// within an edit cycle don't re-parse the document. Cache is invalidated
// on didChange (per-URI) or lockfile reload (all URIs).
//
// Router is safe for concurrent use.
type Router struct {
	chain *resolverChain
	mu    sync.Mutex
	cache map[string]ResolveResult
}

// NewRouter returns a Router backed by the given resolver chain. chain may be
// nil; in that case Resolve always returns StateUnindexable.
func NewRouter(chain *resolverChain) *Router {
	return &Router{chain: chain, cache: make(map[string]ResolveResult)}
}

// Resolve returns the cached ResolveResult for (uri, text), populating the
// cache on first call.
func (r *Router) Resolve(ctx context.Context, uri, text string) ResolveResult {
	if r == nil || r.chain == nil {
		return ResolveResult{State: StateUnindexable}
	}
	r.mu.Lock()
	if got, ok := r.cache[uri]; ok {
		r.mu.Unlock()
		return got
	}
	r.mu.Unlock()

	got := r.chain.Resolve(ctx, uri, text)

	r.mu.Lock()
	r.cache[uri] = got
	r.mu.Unlock()
	return got
}

// IsOwned reports whether schemalock claims this URI for direct serving.
// True for Pinned, Unpinned, Preview; false for Unindexable and Error.
func (r *Router) IsOwned(ctx context.Context, uri, text string) bool {
	s := r.Resolve(ctx, uri, text).State
	return s == StatePinned || s == StateUnpinned || s == StatePreview
}

// Invalidate removes the cache entry for uri.
func (r *Router) Invalidate(uri string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	delete(r.cache, uri)
	r.mu.Unlock()
}

// InvalidateAll drops every cache entry.
func (r *Router) InvalidateAll() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.cache = make(map[string]ResolveResult)
	r.mu.Unlock()
}

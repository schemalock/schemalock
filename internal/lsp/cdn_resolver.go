package lsp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/schemalock/app/internal/registry"
	"golang.org/x/sync/singleflight"
)

// versionsEntry caches one /<ecosystem>/<group>/versions.json response.
// status == versionsIndexed means versions is populated; status == versionsNotIndexed
// means the CDN returned 404 (no schemas for this group).
type versionsStatus int

const (
	versionsIndexed versionsStatus = iota
	versionsNotIndexed
)

type versionsEntry struct {
	versions  []string
	status    versionsStatus
	expiresAt time.Time
}

// cdnResolver is the CDN side of the resolution chain. It owns the in-memory
// caches for versions.json and manifest.json, the short-lived errorCache for
// transient failures, and singleflight groups that coalesce concurrent CDN
// fetches for the same key.
type cdnResolver struct {
	client *registry.Client

	posTTL time.Duration
	negTTL time.Duration
	errTTL time.Duration

	cacheDir string // on-disk schema cache path, typically ~/.schemalock/cache

	vMu     sync.RWMutex
	vCache  map[string]versionsEntry // key: ecosystem + "/" + group

	vFlight singleflight.Group

	mMu    sync.RWMutex
	mCache map[manifestKey]manifestEntry

	mFlight singleflight.Group

	eMu    sync.Mutex
	eCache map[string]time.Time // key: ecosystem + "/" + group, value: expiry
}

func newCDNResolver(client *registry.Client, posTTL, negTTL, errTTL time.Duration) *cdnResolver {
	return &cdnResolver{
		client: client,
		posTTL: posTTL,
		negTTL: negTTL,
		errTTL: errTTL,
		vCache: make(map[string]versionsEntry),
		mCache: make(map[manifestKey]manifestEntry),
		eCache: make(map[string]time.Time),
	}
}

// versions returns the cached versions list for (ecosystem, group), fetching
// from the CDN on miss. registry.ErrNotFound is returned when the group is not
// indexed by the CDN; the negative result is cached for negTTL.
func (r *cdnResolver) versions(ctx context.Context, ecosystem, group string) ([]string, error) {
	key := ecosystem + "/" + group

	if r.IsTransientError(ecosystem, group) {
		return nil, fmt.Errorf("cdn: in back-off for %s/%s", ecosystem, group)
	}

	r.vMu.RLock()
	if e, ok := r.vCache[key]; ok && time.Now().Before(e.expiresAt) {
		r.vMu.RUnlock()
		switch e.status {
		case versionsIndexed:
			return append([]string(nil), e.versions...), nil
		case versionsNotIndexed:
			return nil, registry.ErrNotFound
		}
	}
	r.vMu.RUnlock()

	v, err, _ := r.vFlight.Do(key, func() (any, error) {
		return r.client.FetchVersions(ctx, ecosystem, group)
	})

	now := time.Now()
	if err != nil {
		if errors.Is(err, registry.ErrNotFound) {
			r.vMu.Lock()
			r.vCache[key] = versionsEntry{status: versionsNotIndexed, expiresAt: now.Add(r.negTTL)}
			r.vMu.Unlock()
		} else {
			r.noteTransientError(ecosystem, group)
		}
		// transient errors are NOT cached; errorCache (added in Task 1.6) handles those.
		return nil, err
	}

	vs := v.([]string)
	r.vMu.Lock()
	r.vCache[key] = versionsEntry{versions: append([]string(nil), vs...), status: versionsIndexed, expiresAt: now.Add(r.posTTL)}
	r.vMu.Unlock()
	return vs, nil
}

// manifestKey identifies one /<ecosystem>/<group>/<version>/manifest.json file.
type manifestKey struct{ ecosystem, group, version string }

// manifestEntry holds a cached manifest.json response with expiration.
type manifestEntry struct {
	manifest  registry.Manifest
	found     bool // false = CDN returned 404
	expiresAt time.Time
}

// manifest returns the cached manifest for (ecosystem, group, version),
// fetching from the CDN on miss. registry.ErrNotFound is returned when the
// CDN responds 404; the negative result is cached for negTTL.
func (r *cdnResolver) manifest(ctx context.Context, ecosystem, group, version string) (registry.Manifest, error) {
	key := manifestKey{ecosystem, group, version}

	if r.IsTransientError(ecosystem, group) {
		return registry.Manifest{}, fmt.Errorf("cdn: in back-off for %s/%s", ecosystem, group)
	}

	r.mMu.RLock()
	if e, ok := r.mCache[key]; ok && time.Now().Before(e.expiresAt) {
		r.mMu.RUnlock()
		if e.found {
			return e.manifest, nil
		}
		return registry.Manifest{}, registry.ErrNotFound
	}
	r.mMu.RUnlock()

	flightKey := ecosystem + "/" + group + "/" + version
	m, err, _ := r.mFlight.Do(flightKey, func() (any, error) {
		return r.client.FetchManifest(ctx, ecosystem, group, version)
	})

	now := time.Now()
	if err != nil {
		if errors.Is(err, registry.ErrNotFound) {
			r.mMu.Lock()
			r.mCache[key] = manifestEntry{found: false, expiresAt: now.Add(r.negTTL)}
			r.mMu.Unlock()
		} else {
			r.noteTransientError(ecosystem, group)
		}
		return registry.Manifest{}, err
	}

	manifest := m.(registry.Manifest)
	r.mMu.Lock()
	r.mCache[key] = manifestEntry{manifest: manifest, found: true, expiresAt: now.Add(r.posTTL)}
	r.mMu.Unlock()
	return manifest, nil
}

// noteTransientError records that a CDN call for (ecosystem, group) failed
// with a transient error. Further calls within errTTL are short-circuited.
func (r *cdnResolver) noteTransientError(ecosystem, group string) {
	key := ecosystem + "/" + group
	r.eMu.Lock()
	r.eCache[key] = time.Now().Add(r.errTTL)
	r.eMu.Unlock()
}

// IsTransientError returns true if the back-off window for (ecosystem, group)
// is still active.
func (r *cdnResolver) IsTransientError(ecosystem, group string) bool {
	key := ecosystem + "/" + group
	r.eMu.Lock()
	defer r.eMu.Unlock()
	exp, ok := r.eCache[key]
	if !ok {
		return false
	}
	if time.Now().After(exp) {
		delete(r.eCache, key)
		return false
	}
	return true
}

// ClearTransientErrors removes the back-off entry for (ecosystem, group),
// allowing the next call to hit the CDN again.
func (r *cdnResolver) ClearTransientErrors(ecosystem, group string) {
	key := ecosystem + "/" + group
	r.eMu.Lock()
	delete(r.eCache, key)
	r.eMu.Unlock()
}

// CDNResolveResult is what cdnResolver.Resolve returns to its caller (the
// resolver chain in resolve.go). SchemaBytes is the byte-identical schema as
// fetched from CDN and integrity-verified.
type CDNResolveResult struct {
	Version     string
	Integrity   string
	SchemaBytes []byte
	FromCache   bool // true if served from disk cache without a CDN fetch
}

// Resolve returns the schema bytes for (ecosystem, group, kind), picking the
// latest indexed version. Cache-first: ~/.schemalock/cache/.../<version>/<Kind>.json
// is checked before any CDN call. Failures map to the error states in resolve.go:
//   - registry.ErrNotFound  => "Unindexable" (group not indexed, or kind not in manifest)
//   - other errors          => "Error" (transient, back-off via noteTransientError)
func (r *cdnResolver) Resolve(ctx context.Context, ecosystem, group, kind string) (CDNResolveResult, error) {
	versions, err := r.versions(ctx, ecosystem, group)
	if err != nil {
		return CDNResolveResult{}, err
	}
	if len(versions) == 0 {
		return CDNResolveResult{}, registry.ErrNotFound
	}
	latest := versions[0]

	manifest, err := r.manifest(ctx, ecosystem, group, latest)
	if err != nil {
		return CDNResolveResult{}, err
	}
	entry, ok := manifest.Kinds[kind]
	if !ok {
		return CDNResolveResult{}, registry.ErrNotFound
	}

	// Disk cache first.
	cachePath := filepath.Join(r.cacheDir, ecosystem, group, latest, kind+".json")
	if b, err := os.ReadFile(cachePath); err == nil {
		// Disk cache trusted for unpinned schemas (verified at write time).
		return CDNResolveResult{Version: latest, Integrity: entry.Integrity, SchemaBytes: b, FromCache: true}, nil
	}

	// Fetch from CDN.
	body, err := r.client.FetchSchema(ctx, ecosystem, group, latest, kind)
	if err != nil {
		return CDNResolveResult{}, err
	}
	// Verify against manifest integrity.
	wantIntegrity := entry.Integrity
	gotIntegrity := registry.ComputeIntegrity(body)
	if gotIntegrity != wantIntegrity {
		return CDNResolveResult{}, fmt.Errorf("cdn: integrity mismatch for %s/%s/%s/%s.json: want %s got %s",
			ecosystem, group, latest, kind, wantIntegrity, gotIntegrity)
	}
	// Write to disk cache.
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err == nil {
		_ = os.WriteFile(cachePath, body, 0o644)
	}
	return CDNResolveResult{Version: latest, Integrity: wantIntegrity, SchemaBytes: body}, nil
}

// snapshot returns a JSON-safe view of every in-memory cache for debugging.
func (r *cdnResolver) snapshot() map[string]any {
	out := map[string]any{}

	r.vMu.RLock()
	vs := make(map[string]any, len(r.vCache))
	for k, e := range r.vCache {
		vs[k] = map[string]any{
			"status":    e.status,
			"expiresAt": e.expiresAt,
			"versions":  e.versions,
		}
	}
	r.vMu.RUnlock()
	out["versions"] = vs

	r.mMu.RLock()
	ms := make(map[string]any, len(r.mCache))
	for k, e := range r.mCache {
		ms[k.ecosystem+"/"+k.group+"/"+k.version] = map[string]any{
			"found":     e.found,
			"expiresAt": e.expiresAt,
		}
	}
	r.mMu.RUnlock()
	out["manifests"] = ms

	r.eMu.Lock()
	es := make(map[string]any, len(r.eCache))
	for k, t := range r.eCache {
		es[k] = t
	}
	r.eMu.Unlock()
	out["errors"] = es

	return out
}

// ResolveAt is like Resolve but uses the caller-supplied version instead of
// picking the latest from versions.json. The manifest is still consulted to
// confirm the version exists; this guards against typos in the override store
// and supports the lockfile-pinned + cache-miss path.
func (r *cdnResolver) ResolveAt(ctx context.Context, ecosystem, group, kind, version string) (CDNResolveResult, error) {
	if r.IsTransientError(ecosystem, group) {
		return CDNResolveResult{}, fmt.Errorf("cdn: in back-off for %s/%s", ecosystem, group)
	}
	manifest, err := r.manifest(ctx, ecosystem, group, version)
	if err != nil {
		return CDNResolveResult{}, err
	}
	entry, ok := manifest.Kinds[kind]
	if !ok {
		return CDNResolveResult{}, registry.ErrNotFound
	}
	cachePath := filepath.Join(r.cacheDir, ecosystem, group, version, kind+".json")
	if b, err := os.ReadFile(cachePath); err == nil {
		return CDNResolveResult{Version: version, Integrity: entry.Integrity, SchemaBytes: b, FromCache: true}, nil
	}
	body, err := r.client.FetchSchema(ctx, ecosystem, group, version, kind)
	if err != nil {
		return CDNResolveResult{}, err
	}
	if got := registry.ComputeIntegrity(body); got != entry.Integrity {
		return CDNResolveResult{}, fmt.Errorf("cdn: integrity mismatch for %s/%s/%s/%s.json", ecosystem, group, version, kind)
	}
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err == nil {
		_ = os.WriteFile(cachePath, body, 0o644)
	}
	return CDNResolveResult{Version: version, Integrity: entry.Integrity, SchemaBytes: body}, nil
}

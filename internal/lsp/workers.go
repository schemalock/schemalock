package lsp

import (
	"context"
	"errors"
	"fmt"
	"sync"

	lsp "go.lsp.dev/protocol"

	"github.com/schemalock/app/internal/cache"
	"github.com/schemalock/app/internal/registry"
	"github.com/schemalock/app/internal/validator"
	"github.com/schemalock/app/internal/yamldoc"
)

// job is one unit of work submitted to the [WorkerPool]: validate one version
// of one document and publish the resulting diagnostics.
// The resolver is captured at submission time so workers never read a shared
// mutable field on the pool itself, avoiding data races when the resolver is
// updated after initialize.
//
// When the chain resolved via the CDN (StateUnpinned or StatePreview),
// schemaBytes and integrity carry the integrity-verified schema bytes from the
// ResolveResult. The worker uses these directly when the lockfile resolver
// returns ErrNoMatch, so CDN-fallback documents receive diagnostics even when
// they are not recorded in schemalock.lock.
type job struct {
	ctx         context.Context
	uri         string
	version     uint32
	text        string
	resolver    *SchemaResolver // captured at Submit time; may be nil (skip validation)
	schemaBytes []byte          // non-nil for CDN-resolved (Unpinned/Preview) docs
	integrity   string          // SRI hash matching schemaBytes; used as compiler cache key
}

// WorkerDeps bundles the static dependencies injected into each worker
// goroutine. The [SchemaResolver] is not included here; it is captured per-job
// in [job.resolver] so that resolver updates after initialize are race-free.
type WorkerDeps struct {
	// Cache is the local schema mirror.
	Cache *cache.Cache
	// Registry is the CDN client used to fetch missing schemas.
	Registry *registry.Client
	// Compiler is the compile-once JSON Schema cache.
	Compiler *validator.Compiler
	// Publish is called by each worker after validation is complete.
	// ctx is the job context, uri is the document URI, version is the document
	// version (int32 to match lsp.PublishDiagnosticsParams.Version), and
	// diagnostics is the validated result list (empty slice = clear).
	Publish func(ctx context.Context, uri string, version uint32, diagnostics []lsp.Diagnostic)
	// StrictFn is called once per job to determine whether strict-mode typo
	// detection is enabled. nil means strict-off. Using a closure (rather than
	// a snapshot bool) keeps a future didChangeConfiguration path race-free.
	StrictFn func() bool
}

// WorkerPool manages a bounded set of goroutines that process validation jobs.
// Jobs are submitted via [WorkerPool.Submit] and processed concurrently.
// Call [WorkerPool.Stop] to drain in-flight work and shut down all goroutines.
type WorkerPool struct {
	jobs chan job
	wg   sync.WaitGroup
	deps WorkerDeps
}

// NewWorkerPool starts size worker goroutines and returns a ready pool.
// size must be >= 1.
func NewWorkerPool(size int, deps WorkerDeps) *WorkerPool {
	if size < 1 {
		size = 1
	}
	p := &WorkerPool{
		jobs: make(chan job, size*4), // small buffer so Submit rarely blocks
		deps: deps,
	}
	for i := 0; i < size; i++ {
		p.wg.Add(1)
		go p.run()
	}
	return p
}

// Submit enqueues a validation job. It is safe to call Submit from any
// goroutine. If the pool has been stopped, Submit panics (callers must not
// submit after Stop).
func (p *WorkerPool) Submit(j job) {
	p.jobs <- j
}

// Stop closes the job channel and waits for all workers to finish their
// current jobs. After Stop returns, no more jobs will be processed.
func (p *WorkerPool) Stop() {
	close(p.jobs)
	p.wg.Wait()
}

// run is the worker goroutine body. It pulls jobs from the channel until it
// is closed.
func (p *WorkerPool) run() {
	defer p.wg.Done()
	for j := range p.jobs {
		p.process(j)
	}
}

// process validates one document version and publishes diagnostics.
// Errors that are normal (no schema match, context cancelled) are silently
// handled. Unexpected errors are surfaced via the Publish callback as a
// single error diagnostic.
func (p *WorkerPool) process(j job) {
	// Respect cancellation immediately.
	if j.ctx.Err() != nil {
		return
	}

	// If neither a lockfile resolver nor CDN schema bytes are available, we
	// cannot validate. CDN-resolved jobs (Unpinned/Preview) may carry non-nil
	// schemaBytes even when j.resolver is nil (no lockfile at initialize time).
	if j.resolver == nil && len(j.schemaBytes) == 0 {
		p.deps.Publish(j.ctx, j.uri, j.version, []lsp.Diagnostic{})
		return
	}

	docs, err := yamldoc.Parse([]byte(j.text))
	if err != nil {
		// If we can't parse the document at all, emit a diagnostic at line 1.
		p.deps.Publish(j.ctx, j.uri, j.version, []lsp.Diagnostic{{
			Range: lsp.Range{
				Start: lsp.Position{Line: 0, Character: 0},
				End:   lsp.Position{Line: 0, Character: 0},
			},
			Severity: lsp.DiagnosticSeverityError,
			Source:   "schemalock",
			Message:  fmt.Sprintf("YAML parse error: %s", err),
		}})
		return
	}

	var allDiags []lsp.Diagnostic

	for _, doc := range docs {
		if j.ctx.Err() != nil {
			return
		}

		if doc.APIVersion == "" || doc.Kind == "" {
			// Not a typed Kubernetes resource; skip silently.
			continue
		}

		// Attempt lockfile resolution first. When there is no lockfile resolver
		// (e.g. initialize found no schemalock.lock), treat as ErrNoMatch so the
		// CDN fallback bytes can take over.
		var resolveErr error = ErrNoMatch
		var entry resolvedEntry
		if j.resolver != nil {
			entry, resolveErr = j.resolver.Resolve(doc.APIVersion, doc.Kind)
		}
		useFallbackBytes := errors.Is(resolveErr, ErrNoMatch) && len(j.schemaBytes) > 0

		if resolveErr != nil && !useFallbackBytes {
			// ErrNoMatch with no fallback bytes, or some other resolver error:
			// skip this document silently.
			continue
		}

		if j.ctx.Err() != nil {
			return
		}

		var schemaBytes []byte
		var compileKey string

		if useFallbackBytes {
			// CDN-resolved document (Unpinned/Preview): use the pre-fetched,
			// integrity-verified bytes supplied by the caller. Verify integrity
			// before compilation as a defence-in-depth check (the chain already
			// verified them, but the job may have been queued slightly before the
			// chain result was written).
			if vErr := registry.VerifyIntegrity(j.schemaBytes, j.integrity); vErr != nil {
				// Integrity mismatch on the bytes we were given — skip and emit
				// no diagnostics rather than validating against a suspect schema.
				continue
			}
			schemaBytes = j.schemaBytes
			compileKey = j.integrity
		} else {
			// Lockfile-pinned document: read from cache or fetch from CDN.
			var err error
			schemaBytes, err = p.deps.Cache.ReadSchema(entry.Ecosystem, entry.Group, entry.ReleaseVersion, entry.Kind)
			if errors.Is(err, cache.ErrNotFound) {
				schemaBytes, err = p.deps.Registry.FetchSchema(j.ctx, entry.Ecosystem, entry.Group, entry.ReleaseVersion, entry.Kind)
				if err != nil {
					if j.ctx.Err() != nil {
						return
					}
					p.deps.Publish(j.ctx, j.uri, j.version, []lsp.Diagnostic{{
						Range: lsp.Range{
							Start: lsp.Position{Line: 0, Character: 0},
							End:   lsp.Position{Line: 0, Character: 0},
						},
						Severity: lsp.DiagnosticSeverityWarning,
						Source:   "schemalock",
						Message:  fmt.Sprintf("could not fetch schema for %s/%s: %s", doc.APIVersion, doc.Kind, err),
					}})
					return
				}
				// Write to cache (integrity-verified inside WriteSchema).
				if werr := p.deps.Cache.WriteSchema(entry.Ecosystem, entry.Group, entry.ReleaseVersion, entry.Kind, entry.Integrity, schemaBytes); werr != nil {
					// Log but don't abort — schemaBytes is still valid, we just
					// couldn't persist it.
					_ = werr
				}
			} else if err != nil {
				// Unexpected read error; skip this document.
				continue
			}
			compileKey = entry.Integrity
		}

		if j.ctx.Err() != nil {
			return
		}

		// Apply strict-mode schema rewrite when enabled. The rewrite injects
		// additionalProperties:false at every object node that has properties
		// but no existing additionalProperties declaration. On rewrite failure
		// we fall through with the original bytes so baseline validation is
		// unaffected. The compile key gets a ":strict" suffix so the strict and
		// non-strict compiled forms can coexist in the Compiler cache without
		// colliding. Note: "#" must not be used here as a suffix character
		// because the Compiler uses the key as a synthetic URL path component,
		// and "#" would be treated as a URL fragment delimiter by the
		// jsonschema library.
		if p.deps.StrictFn != nil && p.deps.StrictFn() {
			if wrapped, werr := validator.WrapStrict(schemaBytes); werr == nil {
				schemaBytes = wrapped
				compileKey = compileKey + ":strict"
			}
			// else: silent fall-through; strict-mode is best-effort.
		}

		compiled, err := p.deps.Compiler.Compile(compileKey, schemaBytes)
		if err != nil {
			p.deps.Publish(j.ctx, j.uri, j.version, []lsp.Diagnostic{{
				Range: lsp.Range{
					Start: lsp.Position{Line: 0, Character: 0},
					End:   lsp.Position{Line: 0, Character: 0},
				},
				Severity: lsp.DiagnosticSeverityError,
				Source:   "schemalock",
				Message:  fmt.Sprintf("could not compile schema for %s/%s: %s", doc.APIVersion, doc.Kind, err),
			}})
			return
		}

		vdiags := validator.Validate(compiled, doc)
		for _, d := range vdiags {
			allDiags = append(allDiags, toLSPDiagnostic(d))
		}
	}

	if j.ctx.Err() != nil {
		return
	}

	if allDiags == nil {
		allDiags = []lsp.Diagnostic{}
	}

	p.deps.Publish(j.ctx, j.uri, j.version, allDiags)
}

// toLSPDiagnostic converts a validator.Diagnostic (1-based positions) to a
// lsp.Diagnostic (0-based LSP positions with uint32 coordinates).
// Negative values are clamped to 0 before the int→uint32 cast to prevent
// wraparound — the clamping must happen before the cast, not after.
func toLSPDiagnostic(d validator.Diagnostic) lsp.Diagnostic {
	startLine := max(d.Range.Start.Line-1, 0)
	startChar := max(d.Range.Start.Column-1, 0)
	endLine := max(d.Range.End.Line-1, 0)
	endChar := max(d.Range.End.Column-1, 0)
	return lsp.Diagnostic{
		Range: lsp.Range{
			Start: lsp.Position{Line: uint32(startLine), Character: uint32(startChar)},
			End:   lsp.Position{Line: uint32(endLine), Character: uint32(endChar)},
		},
		Severity: lsp.DiagnosticSeverity(d.Severity),
		Source:   d.Source,
		Message:  d.Message,
	}
}

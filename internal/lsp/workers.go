package lsp

import (
	"context"
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
// mutable field on the pool itself.
//
// schemaBytes and integrity carry the integrity-verified schema bytes from
// the resolver chain's ResolveResult (StatePinned/StateUnpinned/StatePreview).
// Workers validate against these bytes directly — no lockfile-cache path
// remains in the new intent-based design.
type job struct {
	ctx         context.Context
	uri         string
	version     uint32
	text        string
	schemaBytes []byte // schema bytes from the resolver chain
	integrity   string // SRI hash matching schemaBytes; used as compiler cache key
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
// Call [WorkerPool.Stop] to shut down all goroutines. Jobs that are buffered
// but not yet started when Stop is called may be dropped; this is intentional
// for an LSP server where pending diagnostics are irrelevant after shutdown.
type WorkerPool struct {
	jobs chan job
	done chan struct{} // closed by Stop; Submit selects on this to drop new jobs without panicking
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
		done: make(chan struct{}),
		deps: deps,
	}
	for i := 0; i < size; i++ {
		p.wg.Add(1)
		go p.run()
	}
	return p
}

// Submit enqueues a validation job. Safe to call from any goroutine.
// If the pool has been stopped, the job is silently discarded.
func (p *WorkerPool) Submit(j job) {
	select {
	case p.jobs <- j:
	case <-p.done:
	}
}

// Stop signals all workers to exit and waits for them to finish.
// Any Submit calls that race with or follow Stop are silently dropped.
// Buffered jobs that have not yet started processing may also be dropped.
func (p *WorkerPool) Stop() {
	close(p.done)
	p.wg.Wait()
}

// run is the worker goroutine body. It pulls jobs from the channel until
// p.done is closed.
func (p *WorkerPool) run() {
	defer p.wg.Done()
	for {
		select {
		case j := <-p.jobs:
			p.process(j)
		case <-p.done:
			return
		}
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

	// If no schema bytes are available (StateUnindexable / StateError) there
	// is nothing to validate against. Emit an explicit empty list so the
	// editor clears any stale diagnostics for this version.
	if len(j.schemaBytes) == 0 {
		p.deps.Publish(j.ctx, j.uri, j.version, []lsp.Diagnostic{})
		return
	}

	docs, parseErr := yamldoc.Parse([]byte(j.text))
	if parseErr != nil && len(docs) == 0 {
		// Complete parse failure: no usable documents. Emit a parse error diagnostic.
		p.deps.Publish(j.ctx, j.uri, j.version, []lsp.Diagnostic{{
			Range: lsp.Range{
				Start: lsp.Position{Line: 0, Character: 0},
				End:   lsp.Position{Line: 0, Character: 0},
			},
			Severity: lsp.DiagnosticSeverityError,
			Source:   "schemalock",
			Message:  fmt.Sprintf("YAML parse error: %s", parseErr),
		}})
		return
	}
	// Partial parse errors (some docs failed) are intentionally ignored here;
	// valid documents are validated below.

	var allDiags []lsp.Diagnostic

	for _, doc := range docs {
		if j.ctx.Err() != nil {
			return
		}

		if doc.APIVersion == "" || doc.Kind == "" {
			// Not a typed Kubernetes resource; skip silently.
			continue
		}

		// Validate using the schema bytes supplied by the resolver chain.
		// Integrity is verified as defence-in-depth (the chain already
		// verified against the CDN manifest hash).
		if vErr := registry.VerifyIntegrity(j.schemaBytes, j.integrity); vErr != nil {
			continue
		}
		schemaBytes := j.schemaBytes
		compileKey := j.integrity

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

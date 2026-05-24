package lsp

import (
	"context"
	"io"
	"log"
	"runtime"
	"sync"

	"go.lsp.dev/jsonrpc2"
	lspprot "go.lsp.dev/protocol"
	"go.uber.org/zap"

	"github.com/schemalock/app/internal/cache"
	"github.com/schemalock/app/internal/intent"
	"github.com/schemalock/app/internal/lsp/adapter/yamlls"
	"github.com/schemalock/app/internal/lsp/protocol"
	"github.com/schemalock/app/internal/registry"
	"github.com/schemalock/app/internal/validator"
)

// Config holds the external dependencies injected into a [Server].
type Config struct {
	// Cache is the local schema mirror. Required.
	Cache *cache.Cache
	// Registry is the CDN client. Required.
	Registry *registry.Client
	// Logger is the stderr logger. Required; must not write to stdout.
	Logger *log.Logger
}

// Server implements the SchemaLock LSP server. It owns the reader loop and
// coordinates the worker pool and document store.
// Construct with [NewServer]; run with [Server.Run].
type Server struct {
	log      *log.Logger
	docs     *DocumentStore
	cacheDir *cache.Cache
	reg      *registry.Client
	compiler     *validator.Compiler
	intentLookup *intent.Lookup // nil until initialize succeeds
	workers      *WorkerPool

	// mu guards intentLookup for concurrent access.
	// Handlers run in separate goroutines on the jsonrpc2 read path.
	mu sync.RWMutex

	// cancelMu guards cancelFuncs for concurrent access.
	// DidOpen/DidChange/DidClose/DidChangeWatchedFiles can run concurrently
	// on the jsonrpc2 read path.
	cancelMu sync.Mutex

	// cancelFuncs tracks per-URI cancellation for in-flight validation jobs.
	// All reads and writes must be protected by cancelMu.
	cancelFuncs map[string]context.CancelFunc

	// conn is the jsonrpc2 connection.
	conn jsonrpc2.Conn

	// client is the typed LSP client dispatcher for outgoing notifications.
	client lspprot.Client

	// zapLogger is the no-op zap logger passed to jsonrpc2/protocol constructors.
	zapLogger *zap.Logger

	// exitCalled is set to true by the Exit handler before calling conn.Close()
	// so that Run can distinguish a clean exit-notification shutdown from an
	// unexpected stream error.
	exitCalled bool

	// yamlls is the proxy adapter for the yaml-language-server subprocess. It
	// is constructed during initialize and shut down in Run's defer. Every
	// Call* method is a no-op when yamlls is nil or the connector is empty
	// (yaml-ls absent or version-probe rejected).
	yamlls *yamlls.Connector

	// router decides ownership (owned/unowned) for each document URI. Owned
	// documents are served by the schemalock native path; unowned documents
	// are proxied to yaml-ls. nil until initialize constructs a resolver.
	router *Router

	// cdnResolver is the CDN side of the resolver chain. Stored so that
	// DidChangeWatchedFiles and future override handlers can rebuild the chain
	// after initialize without losing the in-memory CDN cache.
	cdnResolver *cdnResolver

	// overrides is the per-document version override store. Stored alongside
	// cdnResolver so DidClose can call Clear(uri) before invalidating the router.
	overrides *OverrideStore

	// fallback records whether CDN fallback was enabled in initializationOptions.
	// Used if the chain is ever rebuilt after initialize.
	fallback bool

	// strict records whether strict-mode typo detection was enabled in
	// initializationOptions. Set during initialize from the "strict" key;
	// absent or wrong-type → true (default ON). Read via a StrictFn closure
	// by the worker pool.
	strict bool

	// notifyClient sends a server-push notification to the LSP client. It is
	// set in Run once the jsonrpc2 connection is established. Nil in tests that
	// don't wire a connection (all notification calls are gated on nil-check).
	notifyClient func(ctx context.Context, method string, params any) error
}

// NewServer constructs a [Server] from the given configuration. It does not
// start any goroutines; call [Server.Run] to begin serving.
func NewServer(cfg Config) *Server {
	return &Server{
		log:         cfg.Logger,
		docs:        newDocumentStore(),
		cacheDir:    cfg.Cache,
		reg:         cfg.Registry,
		compiler:    validator.NewCompiler(),
		cancelFuncs: make(map[string]context.CancelFunc),
		zapLogger:   zap.NewNop(),
	}
}

// Run starts the LSP server using the go.lsp.dev/jsonrpc2-based transport. It
// reads and writes framed JSON-RPC messages over rwc and blocks until:
//   - the client sends an "exit" notification (conn.Close is called → returns nil), or
//   - the context is cancelled (conn.Close is called → returns nil), or
//   - the underlying stream fails (returns the stream error).
//
// All standard LSP handlers and custom schemalock/* methods are dispatched
// through the typed protocol.Server + customHandler chain.
func (s *Server) Run(ctx context.Context, rwc io.ReadWriteCloser) error {
	stream := jsonrpc2.NewStream(rwc)
	s.conn = jsonrpc2.NewConn(stream)
	s.client = lspprot.ClientDispatcher(s.conn, s.zapLogger)

	// notifyCh is a bounded channel used to fan out server-push notifications
	// through a dedicated sender goroutine. The handler path enqueues without
	// blocking; the goroutine drains and calls conn.Notify serially. This
	// decouples the synchronous handler read-path from the conn write mutex so
	// that handlers never deadlock when the worker pool is concurrently writing
	// publishDiagnostics notifications.
	type notification struct {
		ctx    context.Context
		method string
		params any
	}
	notifyCh := make(chan notification, 32)

	// Wire the server-push notification helper. Enqueues into the channel;
	// drops the notification if the channel is full to avoid blocking.
	s.notifyClient = func(ctx context.Context, method string, params any) error {
		select {
		case notifyCh <- notification{ctx: ctx, method: method, params: params}:
		default:
			// Channel full: drop the notification. This is acceptable for
			// best-effort state-change signals; the client will re-query on demand.
		}
		return nil
	}

	// Start the dedicated notification sender goroutine. It drains notifyCh
	// and stops when the channel is closed or the conn is done.
	notifyDone := make(chan struct{})
	go func() {
		defer close(notifyDone)
		for {
			select {
			case <-s.conn.Done():
				// Connection closed; drain the channel without sending.
				for range notifyCh {
				}
				return
			case n, ok := <-notifyCh:
				if !ok {
					return
				}
				// Use Background context: the per-request context may have been
				// cancelled by the time the goroutine runs.
				_ = s.conn.Notify(context.Background(), n.method, n.params)
			}
		}
	}()

	// Start the worker pool for background validation.
	workerSize := min(runtime.NumCPU(), 4)
	s.workers = NewWorkerPool(workerSize, WorkerDeps{
		Cache:    s.cacheDir,
		Registry: s.reg,
		Compiler: s.compiler,
		Publish: func(ctx context.Context, uri string, version uint32, diags []lspprot.Diagnostic) {
			s.publishDiagnostics(ctx, uri, version, diags)
		},
		// StrictFn reads s.strict at job time (always post-initialize). The
		// closure pattern keeps future didChangeConfiguration support race-free.
		StrictFn: func() bool { return s.strict },
	})

	defer func() {
		// Cancel all in-flight jobs before stopping workers.
		s.cancelMu.Lock()
		for _, cancel := range s.cancelFuncs {
			cancel()
		}
		s.cancelMu.Unlock()
		// Shut down the yaml-ls subprocess (idempotent; no-op when connector is empty).
		_ = s.yamlls.Shutdown(context.Background())
		s.workers.Stop()
		// Close the notification channel and wait for the sender goroutine to
		// drain and exit before returning. This ensures no notifications are
		// sent after Run returns.
		close(notifyCh)
		<-notifyDone
	}()

	// Build the handler chain: custom schemalock/* methods in front of the
	// typed protocol.Server dispatcher, with standard MethodNotFoundHandler
	// as the terminal fallback.
	typed := lspprot.ServerHandler(s, jsonrpc2.MethodNotFoundHandler)
	custom := s.customHandler(typed)

	// Wrap with CancelHandler only (not AsyncHandler) to keep request dispatch
	// synchronous on the read goroutine. AsyncHandler's goroutine-per-request
	// model causes a race where the Exit handler can close the connection while
	// a prior response (e.g. shutdown) is still being written. Serial dispatch
	// is safe for all handlers we currently implement because our handlers do
	// not block on external I/O (the worker pool absorbs validation).
	handler := lspprot.CancelHandler(custom)

	// Start the read loop; conn.Go returns immediately.
	s.conn.Go(ctx, handler)

	// Wait for the connection to close (either by Exit handler calling
	// conn.Close, or by context cancellation).
	select {
	case <-s.conn.Done():
		// Clean shutdown via exit notification or stream EOF.
	case <-ctx.Done():
		_ = s.conn.Close()
		<-s.conn.Done()
	}

	// If the Exit handler triggered the shutdown, return nil (clean shutdown).
	// conn.Err() would otherwise contain the "closed pipe" error from closing
	// the stream, which is not an actual failure.
	if s.exitCalled {
		return nil
	}
	return s.conn.Err()
}

// customHandler returns a jsonrpc2.Handler that intercepts schemalock/* custom
// methods before delegating to the typed protocol.ServerHandler chain.
//
// This is the committed approach described in the active plan's §"Handler
// migration shape > Custom methods". It keeps the custom-error-code mapping
// (CodeNoMatch, CodeAmbiguousKind, CodeFetchFailed) trivial and leaves the
// typed Server.Request catch-all free to return ErrMethodNotFound for genuinely
// unknown methods.
func (s *Server) customHandler(next jsonrpc2.Handler) jsonrpc2.Handler {
	return func(ctx context.Context, reply jsonrpc2.Replier, req jsonrpc2.Request) error {
		switch req.Method() {
		case protocol.MethodResolveSchema:
			return s.handleResolveSchemaNew(ctx, reply, req)
		case protocol.MethodGetDocumentState:
			return handleTyped(ctx, reply, req, s.handleGetDocumentState)
		case protocol.MethodListVersionsForGroup:
			return handleTyped(ctx, reply, req, s.handleListVersionsForGroup)
		case protocol.MethodSetDocumentVersionOverride:
			return handleTyped(ctx, reply, req, s.handleSetDocumentVersionOverride)
		case protocol.MethodRetryResolve:
			return handleTyped(ctx, reply, req, s.handleRetryResolve)
		case protocol.MethodCacheDebug:
			return handleTyped(ctx, reply, req, s.handleCacheDebug)
		default:
			return next(ctx, reply, req)
		}
	}
}

// publishDiagnostics sends a textDocument/publishDiagnostics notification to
// the client via the typed LSP client dispatcher. It is called by worker
// goroutines and is safe for concurrent use because jsonrpc2.Conn serialises
// concurrent writes internally.
//
// ctx is the job context passed down from WorkerPool.process. version is
// uint32 to match lsp.PublishDiagnosticsParams.Version.
func (s *Server) publishDiagnostics(ctx context.Context, uri string, version uint32, diagnostics []lspprot.Diagnostic) {
	if s.client == nil {
		// Defensive: handlers can be invoked outside of Run (notably from
		// concurrency_race_test.go which exercises cancelMu directly). Skip
		// the publish in that case rather than panic on nil client.
		return
	}

	if err := s.client.PublishDiagnostics(ctx, &lspprot.PublishDiagnosticsParams{
		URI:         lspprot.DocumentURI(uri),
		Version:     version,
		Diagnostics: diagnostics,
	}); err != nil {
		s.log.Printf("publishDiagnostics: %v", err)
	}
}

// enqueueValidation cancels any in-flight job for uri, then submits a new one.
// The current resolver snapshot is captured into the job so that workers never
// read a shared mutable field. cancelMu protects cancelFuncs so callers can
// invoke this from any goroutine.
//
// res is the ResolveResult from the router for this document. When res.Schema
// is non-nil (CDN-resolved Unpinned or Preview state), the schema bytes and
// integrity are threaded into the job so the worker can validate even when the
// lockfile resolver returns ErrNoMatch for this document.
func (s *Server) enqueueValidation(uri, text string, version uint32, res ResolveResult) {
	s.cancelMu.Lock()
	if cancel, ok := s.cancelFuncs[uri]; ok {
		cancel()
	}
	jobCtx, cancel := context.WithCancel(context.Background())
	s.cancelFuncs[uri] = cancel
	s.cancelMu.Unlock()

	s.workers.Submit(job{
		ctx:         jobCtx,
		uri:         uri,
		version:     version,
		text:        text,
		resolver:    nil, // lockfile resolver removed; CDN schema bytes used instead
		schemaBytes: res.Schema,
		integrity:   res.Integrity,
	})
}

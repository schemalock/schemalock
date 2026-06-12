// Package lsp contains the typed-server method set for the SchemaLock LSP
// server. This file declares the ten methods that SchemaLock actively handles
// (Initialize, Initialized, Shutdown, Exit, DidOpen, DidChange, DidClose,
// Completion, Hover, DidChangeWatchedFiles). All handlers are fully
// implemented as of Chunk D. The remaining ~50 protocol.Server no-op methods
// live in server_stubs.go.
package lsp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
	"go.lsp.dev/jsonrpc2"
	lsp "go.lsp.dev/protocol"

	"github.com/schemalock/schemalock/internal/intent"
	"github.com/schemalock/schemalock/internal/lsp/adapter/yamlls"
	"github.com/schemalock/schemalock/internal/lsp/protocol"
	"github.com/schemalock/schemalock/internal/registry"
	"github.com/schemalock/schemalock/internal/yamldoc"
)

// Initialize handles the LSP initialize request via the new jsonrpc2-based Run
// path. It performs lockfile discovery, resolver construction, connector + router
// wiring, and capability advertisement.
//
// The core logic lives in the private initialize helper so the wire_smoke_test
// can call it directly with typed Go parameters without constructing a
// jsonrpc2.Request/Replier pair.
func (s *Server) Initialize(ctx context.Context, params *lsp.InitializeParams) (*lsp.InitializeResult, error) {
	return s.initialize(ctx, params)
}

// initialize is the pure helper containing all Initialize business logic.
// Called by the typed Initialize method (new Run path).
func (s *Server) initialize(ctx context.Context, params *lsp.InitializeParams) (*lsp.InitializeResult, error) {
	// Prefer WorkspaceFolders (LSP 3.6+); fall back to RootURI for legacy
	// clients that don't send WorkspaceFolders. RootURI is deprecated by the
	// spec but still emitted by older editor clients we want to support.
	var rootURI string
	if len(params.WorkspaceFolders) > 0 {
		rootURI = params.WorkspaceFolders[0].URI
	} else {
		rootURI = string(params.RootURI) //nolint:staticcheck // SA1019: RootURI fallback for clients pre-dating WorkspaceFolders
	}
	rootPath := uriToPath(rootURI)

	// Build an intent lookup for walking schemalock.yaml hierarchies.
	// NewLookup is cheap (empty cache) and resolves lazily on first PinFor call.
	intentLookup := intent.NewLookup()
	s.log.Printf("initialize: workspace root %s", rootPath)

	// Parse fallback.enabled from initializationOptions. Default is true so
	// the CDN fallback is active unless explicitly disabled by the client.
	fallbackEnabled := true
	if raw, ok := params.InitializationOptions.(map[string]any); ok {
		if fbRaw, ok := raw["fallback"].(map[string]any); ok {
			if enabled, ok := fbRaw["enabled"].(bool); ok {
				fallbackEnabled = enabled
			}
		}
	}

	// Parse strict from initializationOptions. Default is true (strict-mode ON).
	// Absent field or wrong type → true.
	strictEnabled := true
	if raw, ok := params.InitializationOptions.(map[string]any); ok {
		if v, ok := raw["strict"].(bool); ok {
			strictEnabled = v
		}
	}

	// Build router now that we have the resolver. The CDN resolver and override
	// store are stored on the server so they can be reused if the chain is
	// rebuilt (e.g. after a lockfile reload or version override).
	cdn := newCDNResolver(s.reg, 5*time.Minute, 24*time.Hour, 30*time.Second)
	cdn.cacheDir = s.cacheDir.Root()
	ov := NewOverrideStore()
	chain := newResolverChain(intentLookup, ov, cdn, fallbackEnabled)
	router := NewRouter(chain)

	// Resolve yaml-ls path from initializationOptions. The shape is:
	//   { "yamlLanguageServer": { "path": "/abs/path/to/yaml-language-server" } }
	// Unknown fields (including the old { "mode": "..." } payload from the
	// extension pre-Chunk-5) are silently ignored.
	yamllsPath := resolveYamllsPathFromInterface(params.InitializationOptions, s.log)
	resolvedPath := yamlls.Resolve(yamllsPath)

	// Build and initialise the yaml-ls connector. NewConnector returns an
	// empty (no-op) Connector when resolvedPath is "".
	connector := yamlls.NewConnector(ctx, yamlls.Config{
		Path:             resolvedPath,
		Logger:           s.log,
		EditorClient:     s.client,
		WorkspaceFolders: params.WorkspaceFolders,
		RootURI:          lsp.DocumentURI(rootURI), // already prefers WorkspaceFolders; deprecated-field access localized to line 44
	})
	// CallInitialize sends initialize to yaml-ls and applies the version probe.
	// On reject / absent, the connector silently degrades to empty (no-op).
	if err := connector.CallInitialize(ctx, params); err != nil {
		s.log.Printf("initialize: yaml-ls CallInitialize: %v", err)
	}

	// Wire the schema provider: given a document URI, return the cached schema
	// file:// URI for owned documents so yaml-ls can apply the correct schema.
	// The intent-based path does not maintain a kind→path index, so this
	// provider always returns "" for now (yaml-ls falls back to its own lookup).
	connector.SetSchemaProvider(func(_ context.Context, _ string) string {
		return ""
	})

	// Wire the ownership checker: PublishDiagnostics uses this to drop
	// yaml-ls diagnostics for owned URIs (invariant #4 — schemalock publishes
	// its own diagnostics for owned files via the worker).
	// router.IsOwned is O(1) on a cache hit, which is the common case for any
	// open document that has already been visited by DidOpen/DidChange.
	connector.SetOwnershipChecker(func(docURI string) bool {
		text, _, ok := s.docs.Get(docURI)
		if !ok {
			return false
		}
		return s.router.IsOwned(context.Background(), docURI, text)
	})

	// Wire intentLookup under the write lock.
	s.mu.Lock()
	s.intentLookup = intentLookup
	s.mu.Unlock()
	// router, yamlls, cdnResolver, overrides, fallback, and strict are not
	// guarded by mu: they are written once here and only read afterward. The
	// happens-before relationship is established by the jsonrpc2 read loop
	// serialising handler calls.
	s.router = router
	s.yamlls = connector
	s.cdnResolver = cdn
	s.overrides = ov
	s.fallback = fallbackEnabled
	s.strict.Store(strictEnabled)
	s.log.Printf("initialize: strict mode = %v", strictEnabled)

	// Build the InitializeResult. TextDocumentSync must be the integer form
	// (lsp.TextDocumentSyncKindFull = 1.0 as float64, but marshals as 1) to
	// match the current wire bytes exactly. Verified by wire_diff_test.go.
	caps := lsp.ServerCapabilities{
		TextDocumentSync: lsp.TextDocumentSyncKindFull,
		CompletionProvider: &lsp.CompletionOptions{
			TriggerCharacters: []string{":"},
		},
		HoverProvider:          true,
		DefinitionProvider:     true,
		ReferencesProvider:     true,
		DocumentSymbolProvider: true,
	}

	return &lsp.InitializeResult{
		Capabilities: caps,
		ServerInfo: &lsp.ServerInfo{
			Name:    "schemalock",
			Version: "0.1.0",
		},
	}, nil
}

// resolveYamllsPathFromInterface extracts the yaml-language-server binary path
// from initializationOptions. The expected shape is:
//
//	{ "yamlLanguageServer": { "path": "/abs/path/to/yaml-language-server" } }
//
// Unknown fields (including the pre-Chunk-5 { "mode": "..." } payload) are
// silently ignored. Returns "" when opts is nil or the path field is absent.
func resolveYamllsPathFromInterface(opts any, logger interface{ Printf(string, ...any) }) string {
	if opts == nil {
		return ""
	}
	raw, err := json.Marshal(opts)
	if err != nil {
		logger.Printf("initialize: cannot re-marshal initializationOptions: %v", err)
		return ""
	}
	var parsed struct {
		YamlLanguageServer struct {
			Path string `json:"path"`
		} `json:"yamlLanguageServer"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		// Graceful degradation: any JSON structure that does not match the
		// expected shape is silently ignored (old { "mode": "..." } payloads
		// will simply parse with an empty YamlLanguageServer.Path).
		return ""
	}
	return parsed.YamlLanguageServer.Path
}

// Initialized handles the initialized notification. No-op — the LSP spec
// says clients send this to indicate initialization is complete; servers
// acknowledge it by doing nothing (capability negotiation already done in
// Initialize).
func (s *Server) Initialized(_ context.Context, _ *lsp.InitializedParams) error {
	return nil
}

// Shutdown handles the shutdown request. Initiates a graceful shutdown of the
// yaml-ls subprocess (if running) and returns nil. Per LSP spec, shutdown
// returns a null result; the client follows with exit.
//
// The defer in Run also calls s.yamlls.Shutdown as a safety net for unclean
// exits. Connector.Shutdown is idempotent so the double-call is harmless.
func (s *Server) Shutdown(ctx context.Context) error {
	s.log.Printf("shutdown: received, waiting for exit")
	_ = s.yamlls.Shutdown(ctx)
	return nil
}

// Exit handles the exit notification. On the new Run path, closing the
// connection triggers <-conn.Done() in Run, ending the server loop.
// We set exitCalled before closing so Run can return nil instead of the
// closed-pipe error from the stream.
func (s *Server) Exit(_ context.Context) error {
	if s.conn != nil {
		s.exitCalled = true
		_ = s.conn.Close()
	}
	return nil
}

// DidOpen handles the textDocument/didOpen notification. Records the document
// in the store, enqueues a validation job (for owned documents), fans the
// notification out to yaml-ls (for unowned documents or schema contribution),
// and publishes baseline parse diagnostics when yaml-ls is absent and the
// document is unowned.
func (s *Server) DidOpen(ctx context.Context, params *lsp.DidOpenTextDocumentParams) error {
	td := params.TextDocument
	uri := string(td.URI)
	version := uint32(td.Version)
	s.docs.Open(uri, version, td.Text)

	// Invalidate any cached resolution before resolving with the real document
	// text. A status-bar getDocumentState poll can reach the server before this
	// didOpen and cache a StateUnindexable result (resolved from empty text)
	// keyed by this URI; without this invalidation the Resolve below would hit
	// that stale entry and suppress the documentStateChanged notification.
	s.router.Invalidate(uri)
	res := s.router.Resolve(ctx, uri, td.Text)
	switch res.State {
	case StatePinned, StateUnpinned, StatePreview:
		// Schemalock-owned: enqueue schema validation using res.Schema (already
		// integrity-verified by the chain — no second fetch needed). Pass res so
		// the worker can use res.Schema directly for CDN-resolved documents.
		s.enqueueValidation(uri, td.Text, version, res)
	default:
		// StateError or StateUnindexable: forward to yaml-ls path.
		// Run baseline parse-error diagnostics when yaml-ls is absent.
		if !s.yamlls.ShouldRun() {
			diags := BaselineDiagnostics(td.Text)
			s.publishDiagnostics(ctx, uri, version, diags)
		}
	}

	// Fan-out to yaml-ls regardless of ownership so yaml-ls has complete
	// document state and can be queried later.
	s.yamlls.DocumentDidOpen(params)

	// Notify client of the document's resolved state. StateUnindexable is
	// not notified — it is the default "not a schemalock document" state
	// and produces no actionable information for the client.
	if res.State != StateUnindexable {
		s.notifyDocumentStateChanged(ctx, uri, res)
	}
	return nil
}

// DidChange handles the textDocument/didChange notification. Full-text sync
// only: uses the first ContentChange.Text. Invalidates the router cache for
// the URI and fans the notification out to yaml-ls.
func (s *Server) DidChange(ctx context.Context, params *lsp.DidChangeTextDocumentParams) error {
	if len(params.ContentChanges) == 0 {
		return nil
	}
	td := params.TextDocument
	uri := string(td.URI)
	version := uint32(td.Version)
	text := params.ContentChanges[0].Text
	s.docs.Change(uri, version, text)

	// Invalidate BEFORE resolving so the new text is re-evaluated by the chain.
	s.router.Invalidate(uri)

	res := s.router.Resolve(ctx, uri, text)
	switch res.State {
	case StatePinned, StateUnpinned, StatePreview:
		// Schemalock-owned: enqueue schema validation. Pass res so the worker
		// can use res.Schema directly for CDN-resolved documents.
		s.enqueueValidation(uri, text, version, res)
	default:
		// StateError or StateUnindexable: forward to yaml-ls path.
		if !s.yamlls.ShouldRun() {
			diags := BaselineDiagnostics(text)
			s.publishDiagnostics(ctx, uri, version, diags)
		}
	}

	// Fan-out to yaml-ls regardless of ownership.
	s.yamlls.DocumentDidChange(params)

	// Notify client of the document's resolved state. StateUnindexable is
	// not notified — it is the default "not a schemalock document" state
	// and produces no actionable information for the client.
	if res.State != StateUnindexable {
		s.notifyDocumentStateChanged(ctx, uri, res)
	}
	return nil
}

// DidClose handles the textDocument/didClose notification. Cancels any
// in-flight validation, removes the document from the store, clears its
// diagnostics, invalidates the router cache, and fans the notification to
// yaml-ls.
func (s *Server) DidClose(ctx context.Context, params *lsp.DidCloseTextDocumentParams) error {
	uri := string(params.TextDocument.URI)

	s.cancelMu.Lock()
	if cancel, ok := s.cancelFuncs[uri]; ok {
		cancel()
		delete(s.cancelFuncs, uri)
	}
	s.cancelMu.Unlock()

	s.docs.Close(uri)

	// Clear any per-document version override BEFORE invalidating the router so
	// the next Resolve call (for a re-open) starts clean.
	if s.overrides != nil {
		s.overrides.Clear(uri)
	}
	s.router.Invalidate(uri)

	// Clear diagnostics for the closed document. version=0 is used since we
	// have no version for a closed document; vscode-languageclient tolerates this.
	s.publishDiagnostics(ctx, uri, 0, []lsp.Diagnostic{})

	// Fan-out to yaml-ls so it releases its internal document state.
	s.yamlls.DocumentDidClose(params)
	return nil
}

// Completion handles the textDocument/completion request. Always tries the
// schemalock native path first (which handles M7/M8 bootstrap and schema-based
// completion for owned documents). If the native path returns an empty/nil
// result and the document is unowned (StateError/StateUnindexable), the
// request is proxied to yaml-ls.
func (s *Server) Completion(ctx context.Context, params *lsp.CompletionParams) (*lsp.CompletionList, error) {
	uri := string(params.TextDocument.URI)
	text, _, _ := s.docs.Get(uri)

	res := s.router.Resolve(ctx, uri, text)

	switch res.State {
	case StatePinned, StateUnpinned, StatePreview:
		// Schemalock-owned: run native completion path with res.Schema available.
		// Pass res so ownedCompletion can use CDN schema bytes when the lockfile
		// resolver returns ErrNoMatch.
		result, err := s.ownedCompletion(ctx, params, text, res)
		if err != nil {
			return nil, err
		}
		if result != nil && (result.IsIncomplete || len(result.Items) > 0) {
			return result, nil
		}
		// Native path returned no items; return empty sentinel.
		if result == nil {
			result = &lsp.CompletionList{IsIncomplete: false, Items: []lsp.CompletionItem{}}
		}
		return result, nil
	default:
		// StateError or StateUnindexable: try native bootstrap first (it handles
		// the apiVersion+kind bootstrap even for unresolved documents), then
		// proxy to yaml-ls.
		result, err := s.ownedCompletion(ctx, params, text, res)
		if err != nil {
			return nil, err
		}
		if result != nil && (result.IsIncomplete || len(result.Items) > 0) {
			return result, nil
		}
		// No native items — proxy to yaml-ls.
		yamllsResult, err2 := s.yamlls.CallCompletion(ctx, params)
		if err2 != nil {
			return nil, err2
		}
		if yamllsResult != nil {
			return yamllsResult, nil
		}
		// yaml-ls absent or returned nil: return empty sentinel.
		if result == nil {
			result = &lsp.CompletionList{IsIncomplete: false, Items: []lsp.CompletionItem{}}
		}
		return result, nil
	}
}

// ownedCompletion runs the native schemalock completion path (M7/M8 bootstrap).
// Called when router.IsOwned is true (or for bootstrap completions on
// StateError/StateUnindexable documents).
//
// res is the ResolveResult from the router. When the lockfile resolver returns
// ErrNoMatch but res.Schema is non-nil (CDN-resolved Unpinned/Preview state),
// ownedCompletion compiles the schema from res.Schema directly so that
// completion works for documents not recorded in schemalock.lock.
func (s *Server) ownedCompletion(ctx context.Context, params *lsp.CompletionParams, text string, res ResolveResult) (*lsp.CompletionList, error) {
	empty := &lsp.CompletionList{IsIncomplete: false, Items: []lsp.CompletionItem{}}

	docs, err := yamldoc.Parse([]byte(text))
	if err != nil || len(docs) == 0 {
		return empty, nil
	}
	doc := docs[0]

	cursorCtx := positionAt(lsp.Position{
		Line:      params.Position.Line,
		Character: params.Position.Character,
	}, doc)

	wordRange := wordRangeAt(text, lsp.Position{
		Line:      params.Position.Line,
		Character: params.Position.Character,
	})

	// Bootstrap kind completion: when apiVersion is set but kind is empty,
	// return one item per kind from the operator manifest.
	s.mu.RLock()
	intentLookup := s.intentLookup
	s.mu.RUnlock()

	uri := string(params.TextDocument.URI)
	if items := bootstrapKindCompletions(ctx, doc, cursorCtx, uri, intentLookup, s.cdnResolver, wordRange); items != nil {
		return &lsp.CompletionList{IsIncomplete: false, Items: items}, nil
	}

	if doc.APIVersion == "" || doc.Kind == "" {
		return empty, nil
	}

	// Compile the schema from the chain-resolved bytes (CDN-resolved or pinned).
	var compiled *jsonschema.Schema
	if len(res.Schema) > 0 {
		compiled, err = s.compiler.Compile(res.Integrity, res.Schema)
		if err != nil {
			return empty, nil
		}
	}
	if compiled == nil {
		return empty, nil
	}

	if cursorCtx.IsKeyPosition {
		parentSchema := schemaPropertiesAt(compiled, cursorCtx.ParentPointer)
		items := completionItemsForProperties(parentSchema, cursorCtx.ExistingKeys, wordRange)
		if items == nil {
			items = []lsp.CompletionItem{}
		}
		return &lsp.CompletionList{IsIncomplete: false, Items: items}, nil
	}

	fieldSchema := schemaAtPointer(compiled, cursorCtx.Pointer)
	if fieldSchema != nil && fieldSchema.Enum != nil {
		items := completionItemsForEnum(fieldSchema, schemaDetail(fieldSchema), wordRange)
		if items == nil {
			items = []lsp.CompletionItem{}
		}
		return &lsp.CompletionList{IsIncomplete: false, Items: items}, nil
	}

	return empty, nil
}

// Hover handles the textDocument/hover request. Routes via resolved state:
// owned documents (Pinned/Unpinned/Preview) use the schemalock native path
// (M7 hover); Error/Unindexable documents are proxied to yaml-ls.
func (s *Server) Hover(ctx context.Context, params *lsp.HoverParams) (*lsp.Hover, error) {
	uri := string(params.TextDocument.URI)
	text, _, _ := s.docs.Get(uri)

	res := s.router.Resolve(ctx, uri, text)
	switch res.State {
	case StatePinned, StateUnpinned, StatePreview:
		// Pass res so ownedHover can use CDN schema bytes when the lockfile
		// resolver returns ErrNoMatch.
		return s.ownedHover(params, text, res)
	default:
		return s.yamlls.CallHover(ctx, params)
	}
}

// ownedHover runs the native schemalock hover path (M7 hover).
// Called when router.IsOwned is true.
//
// res is the ResolveResult from the router. When the lockfile resolver returns
// ErrNoMatch but res.Schema is non-nil (CDN-resolved Unpinned/Preview state),
// ownedHover compiles the schema from res.Schema directly so that hover works
// for documents not recorded in schemalock.lock.
func (s *Server) ownedHover(params *lsp.HoverParams, text string, res ResolveResult) (*lsp.Hover, error) {
	docs, err := yamldoc.Parse([]byte(text))
	if err != nil || len(docs) == 0 {
		return nil, nil
	}
	doc := docs[0]

	if doc.APIVersion == "" || doc.Kind == "" {
		return nil, nil
	}

	// Compile the schema from the chain-resolved bytes (CDN-resolved or pinned).
	var compiled *jsonschema.Schema
	if len(res.Schema) > 0 {
		compiled, err = s.compiler.Compile(res.Integrity, res.Schema)
		if err != nil {
			return nil, nil
		}
	}
	if compiled == nil {
		return nil, nil
	}

	cursorCtx := positionAt(lsp.Position{
		Line:      params.Position.Line,
		Character: params.Position.Character,
	}, doc)
	if cursorCtx.Pointer == "" {
		return nil, nil
	}

	sch := schemaAtPointer(compiled, cursorCtx.Pointer)
	if sch == nil {
		return nil, nil
	}

	fieldName := lastPointerSegment(cursorCtx.Pointer)
	parentRequired := requiredSet(schemaPropertiesAt(compiled, cursorCtx.ParentPointer))

	body := hoverMarkdown(sch, fieldName, parentRequired, doc.Kind, doc.APIVersion)
	if body == "" {
		return nil, nil
	}

	return &lsp.Hover{
		Contents: lsp.MarkupContent{
			Kind:  lsp.Markdown,
			Value: body,
		},
	}, nil
}

// DidChangeWatchedFiles handles the workspace/didChangeWatchedFiles
// notification. If any event is for a schemalock.lock or schemalock.yaml file,
// the intent lookup cache is invalidated and all open documents are re-queued.
func (s *Server) DidChangeWatchedFiles(_ context.Context, params *lsp.DidChangeWatchedFilesParams) error {
	for _, event := range params.Changes {
		uri := string(event.URI)
		if !isIntentOrLockfileEvent(uri) {
			continue
		}
		s.mu.RLock()
		il := s.intentLookup
		s.mu.RUnlock()
		if il != nil {
			il.Invalidate()
		}
		s.log.Printf("didChangeWatchedFiles: intent lookup invalidated for %s", uri)
		s.router.InvalidateAll()
		s.requeueAllOpenDocuments()
		break
	}
	return nil
}

// notifyDocumentStateChanged enqueues a schemalock/documentStateChanged
// server-push notification to the client. It is a no-op when notifyClient is
// nil (test setups that don't wire a full jsonrpc2 connection).
//
// The notification is enqueued into the notification channel wired in Run;
// a dedicated sender goroutine drains the channel and calls conn.Notify
// serially. This keeps the handler path non-blocking (no conn write-mutex
// contention with the worker pool's publishDiagnostics writes).
func (s *Server) notifyDocumentStateChanged(ctx context.Context, uri string, res ResolveResult) {
	if s.notifyClient == nil {
		return
	}
	_ = s.notifyClient(ctx, protocol.MethodDocumentStateChanged, protocol.DocumentStateChangedParams{
		URI:     uri,
		State:   protocol.State(res.State),
		Group:   res.Group,
		Kind:    res.Kind,
		Version: res.Version,
		Source:  res.Source,
		ErrMsg:  res.ErrMsg,
	})
}

// handleResolveSchemaNew is the jsonrpc2.Replier-style handler for
// schemalock/resolveSchema used in the new Run path. The core business logic
// lives in the pure helper resolveSchemaHelper so it can be unit-tested
// without constructing a jsonrpc2.Request/Replier pair.
func (s *Server) handleResolveSchemaNew(ctx context.Context, reply jsonrpc2.Replier, req jsonrpc2.Request) error {
	var params protocol.ResolveSchemaParams
	if err := json.Unmarshal(req.Params(), &params); err != nil {
		return reply(ctx, nil, fmt.Errorf("%w: resolveSchema: invalid params: %s", jsonrpc2.ErrInvalidParams, err))
	}
	result, rpcErr := s.resolveSchemaHelper(ctx, params)
	if rpcErr != nil {
		return reply(ctx, nil, rpcErr)
	}
	return reply(ctx, result, nil)
}

// handleTyped is a generic decode-and-reply wrapper for custom schemalock/*
// request handlers. It decodes req.Params() into P, calls fn, and replies with
// the result R. If P is struct{} (no params), Unmarshal is skipped gracefully.
func handleTyped[P any, R any](
	ctx context.Context,
	reply jsonrpc2.Replier,
	req jsonrpc2.Request,
	fn func(context.Context, P) (R, error),
) error {
	var params P
	if raw := req.Params(); len(raw) > 0 {
		if err := json.Unmarshal(raw, &params); err != nil {
			return reply(ctx, nil, fmt.Errorf("%w: invalid params: %s", jsonrpc2.ErrInvalidParams, err))
		}
	}
	result, err := fn(ctx, params)
	if err != nil {
		return reply(ctx, nil, err)
	}
	return reply(ctx, result, nil)
}

// handleGetDocumentState returns the current ResolveResult for a URI.
func (s *Server) handleGetDocumentState(ctx context.Context, p protocol.GetDocumentStateParams) (protocol.GetDocumentStateResult, error) {
	text, _, ok := s.docs.Get(p.URI)
	if !ok {
		// Document not open yet (e.g. a status-bar poll racing didOpen).
		// Resolving empty text here would cache StateUnindexable under this URI
		// and poison the later didOpen resolution. Return Unindexable without
		// touching the router cache; didOpen will resolve the real text.
		return protocol.GetDocumentStateResult{State: protocol.State(StateUnindexable)}, nil
	}
	res := s.router.Resolve(ctx, p.URI, text)
	return protocol.GetDocumentStateResult{
		State:   protocol.State(res.State),
		Group:   res.Group,
		Kind:    res.Kind,
		Version: res.Version,
		Source:  res.Source,
		ErrMsg:  res.ErrMsg,
	}, nil
}

// handleListVersionsForGroup returns the CDN versions list for a group.
func (s *Server) handleListVersionsForGroup(ctx context.Context, p protocol.ListVersionsForGroupParams) (protocol.ListVersionsForGroupResult, error) {
	if s.cdnResolver == nil {
		return protocol.ListVersionsForGroupResult{Indexed: false}, nil
	}
	vs, err := s.cdnResolver.versions(ctx, "kubernetes", p.Group)
	if err != nil {
		if errors.Is(err, registry.ErrNotFound) {
			return protocol.ListVersionsForGroupResult{Indexed: false}, nil
		}
		return protocol.ListVersionsForGroupResult{}, err
	}
	return protocol.ListVersionsForGroupResult{Versions: vs, Indexed: true}, nil
}

// handleSetDocumentVersionOverride sets or clears a per-doc version override
// and immediately re-resolves the URI, pushing a documentStateChanged
// notification with the new state.
func (s *Server) handleSetDocumentVersionOverride(ctx context.Context, p protocol.SetDocumentVersionOverrideParams) (protocol.SetDocumentVersionOverrideResult, error) {
	text, _, _ := s.docs.Get(p.URI)
	var group string
	if docs, err := yamldoc.Parse([]byte(text)); err == nil && len(docs) > 0 {
		group = groupFromAPIVersion(docs[0].APIVersion)
	}

	if p.Version == "" {
		s.overrides.Clear(p.URI)
	} else {
		s.overrides.Set(p.URI, group, p.Version)
	}
	s.router.Invalidate(p.URI)

	res := s.router.Resolve(ctx, p.URI, text)
	s.notifyDocumentStateChanged(ctx, p.URI, res)
	return protocol.SetDocumentVersionOverrideResult{OK: true}, nil
}

// handleRetryResolve clears transient-error back-off for the group and
// (optionally) re-resolves the URI, firing a notification.
func (s *Server) handleRetryResolve(ctx context.Context, p protocol.RetryResolveParams) (protocol.RetryResolveResult, error) {
	if s.cdnResolver != nil {
		s.cdnResolver.ClearTransientErrors("kubernetes", p.Group)
	}
	s.router.InvalidateAll()
	if p.URI != "" {
		text, _, _ := s.docs.Get(p.URI)
		res := s.router.Resolve(ctx, p.URI, text)
		s.notifyDocumentStateChanged(ctx, p.URI, res)
	}
	return protocol.RetryResolveResult{OK: true}, nil
}

// handleCacheDebug returns a JSON-safe snapshot of every in-memory cdnResolver cache.
func (s *Server) handleCacheDebug(_ context.Context, _ struct{}) (protocol.CacheDebugResult, error) {
	if s.cdnResolver == nil {
		return protocol.CacheDebugResult{Snapshot: map[string]any{}}, nil
	}
	return protocol.CacheDebugResult{Snapshot: s.cdnResolver.snapshot()}, nil
}

// compile-time assertion: *Server must satisfy protocol.Server.
var _ lsp.Server = (*Server)(nil)

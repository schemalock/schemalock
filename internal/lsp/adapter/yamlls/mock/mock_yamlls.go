// Package mock provides an in-process yaml-language-server stub for unit tests.
// It speaks the LSP jsonrpc2 protocol over an io.Pipe pair and responds to the
// initialize/initialized/shutdown/exit sequence with canned responses.
//
// The mock does NOT spawn a subprocess; it is wired via io.Pipe so tests
// remain hermetic and fast.
package mock

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"

	"go.lsp.dev/jsonrpc2"
	lsp "go.lsp.dev/protocol"
	"go.uber.org/zap"
)

// YamllsVersion is the ServerInfo.Version the mock advertises in its
// InitializeResult.
const YamllsVersion = "1.15.0"

// MockYamlls is an in-process yaml-ls stub. Use [New] to construct one and
// [MockYamlls.Connect] to get the io.ReadWriteCloser that a [yamlls.Connector]
// should connect to.
type MockYamlls struct {
	// Version is the version string returned in InitializeResult. Override
	// before calling Connect to simulate version-probe scenarios.
	Version string

	// CustomSchemaRequestURIs accumulates the lists of URIs received in each
	// custom/schema/request call. Guarded by mu.
	CustomSchemaRequestURIs [][]string

	// ReceivedMethods lists every jsonrpc2 method received, in order. Guarded by mu.
	ReceivedMethods []string

	// HoverResult is returned for every Hover request. Nil by default.
	HoverResult *lsp.Hover

	// CompletionResult is returned for every Completion request. Nil by default.
	CompletionResult *lsp.CompletionList

	// OnShutdown is called when the mock receives a shutdown request, before
	// sending the response. May be nil.
	OnShutdown func()

	mu   sync.Mutex
	conn jsonrpc2.Conn
}

// New creates a MockYamlls with the default version [YamllsVersion].
func New() *MockYamlls {
	return &MockYamlls{Version: YamllsVersion}
}

// pipeRWC wraps an io.PipeReader and io.PipeWriter into an io.ReadWriteCloser.
type pipeRWC struct {
	r *io.PipeReader
	w *io.PipeWriter
}

func (p pipeRWC) Read(b []byte) (int, error)  { return p.r.Read(b) }
func (p pipeRWC) Write(b []byte) (int, error) { return p.w.Write(b) }
func (p pipeRWC) Close() error {
	_ = p.r.Close()
	return p.w.Close()
}

// Connect returns the client-side ReadWriteCloser (the side that the Connector
// should connect to) and starts the mock's jsonrpc2 handler in a background
// goroutine. The mock shuts itself down cleanly when it receives an exit
// notification.
//
// Call [MockYamlls.Wait] to block until the mock has fully stopped.
func (m *MockYamlls) Connect(ctx context.Context) io.ReadWriteCloser {
	// Two io.Pipe pairs: one for each direction.
	// clientR reads what the mock writes; mockR reads what the client writes.
	clientR, mockW := io.Pipe()
	mockR, clientW := io.Pipe()

	clientSide := pipeRWC{r: clientR, w: clientW}
	mockSide := pipeRWC{r: mockR, w: mockW}

	stream := jsonrpc2.NewStream(mockSide)
	conn := jsonrpc2.NewConn(stream)
	m.mu.Lock()
	m.conn = conn
	m.mu.Unlock()

	zapNop := zap.NewNop()
	// The mock acts as a server, so we use ServerHandler to handle standard
	// LSP requests and fall back to our custom handler for the rest.
	// lsp.CancelHandler (from go.lsp.dev/protocol) returns a single Handler.
	handler := lsp.ServerHandler(m, jsonrpc2.MethodNotFoundHandler)
	customH := m.customHandler(handler)
	conn.Go(ctx, lsp.CancelHandler(customH))

	// Wire up the server dispatcher so we can call back to the client
	// (for custom/schema/request responses etc).
	_ = lsp.ClientDispatcher(conn, zapNop)

	return clientSide
}

// Wait blocks until the mock's connection is closed.
func (m *MockYamlls) Wait() {
	m.mu.Lock()
	conn := m.conn
	m.mu.Unlock()
	if conn != nil {
		<-conn.Done()
	}
}

// customHandler intercepts yaml/registerCustomSchemaRequest and custom/schema/request.
func (m *MockYamlls) customHandler(next jsonrpc2.Handler) jsonrpc2.Handler {
	return func(ctx context.Context, reply jsonrpc2.Replier, req jsonrpc2.Request) error {
		m.mu.Lock()
		m.ReceivedMethods = append(m.ReceivedMethods, req.Method())
		m.mu.Unlock()

		switch req.Method() {
		case "yaml/registerCustomSchemaRequest":
			// Acknowledgement; no body needed.
			return reply(ctx, nil, nil)
		case "custom/schema/request":
			var uris []string
			if err := json.Unmarshal(req.Params(), &uris); err != nil {
				return reply(ctx, nil, err)
			}
			m.mu.Lock()
			m.CustomSchemaRequestURIs = append(m.CustomSchemaRequestURIs, uris)
			m.mu.Unlock()
			// Return empty schema URIs (mock doesn't know any schemas).
			result := make([]string, len(uris))
			return reply(ctx, result, nil)
		}
		return next(ctx, reply, req)
	}
}

// --- lsp.Server implementation for the mock ---

// Initialize returns a canned InitializeResult with the configured Version.
func (m *MockYamlls) Initialize(_ context.Context, _ *lsp.InitializeParams) (*lsp.InitializeResult, error) {
	return &lsp.InitializeResult{
		Capabilities: lsp.ServerCapabilities{
			TextDocumentSync: lsp.TextDocumentSyncKindFull,
			HoverProvider:    true,
			CompletionProvider: &lsp.CompletionOptions{
				TriggerCharacters: []string{":"},
			},
		},
		ServerInfo: &lsp.ServerInfo{
			Name:    "yaml-language-server (mock)",
			Version: m.Version,
		},
	}, nil
}

// Initialized is a no-op in the mock.
func (m *MockYamlls) Initialized(_ context.Context, _ *lsp.InitializedParams) error { return nil }

// Shutdown sends the response and optionally calls OnShutdown.
func (m *MockYamlls) Shutdown(_ context.Context) error {
	m.mu.Lock()
	fn := m.OnShutdown
	m.mu.Unlock()
	if fn != nil {
		fn()
	}
	return nil
}

// Exit closes the connection.
func (m *MockYamlls) Exit(_ context.Context) error {
	m.mu.Lock()
	conn := m.conn
	m.mu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
	return nil
}

// DidOpen records receipt.
func (m *MockYamlls) DidOpen(_ context.Context, _ *lsp.DidOpenTextDocumentParams) error {
	return nil
}

// DidChange records receipt.
func (m *MockYamlls) DidChange(_ context.Context, _ *lsp.DidChangeTextDocumentParams) error {
	return nil
}

// DidClose records receipt.
func (m *MockYamlls) DidClose(_ context.Context, _ *lsp.DidCloseTextDocumentParams) error {
	return nil
}

// DidSave records receipt.
func (m *MockYamlls) DidSave(_ context.Context, _ *lsp.DidSaveTextDocumentParams) error {
	return nil
}

// Hover returns the configured HoverResult.
func (m *MockYamlls) Hover(_ context.Context, _ *lsp.HoverParams) (*lsp.Hover, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.HoverResult, nil
}

// Completion returns the configured CompletionResult.
func (m *MockYamlls) Completion(_ context.Context, _ *lsp.CompletionParams) (*lsp.CompletionList, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.CompletionResult, nil
}

// Definition returns empty results.
func (m *MockYamlls) Definition(_ context.Context, _ *lsp.DefinitionParams) ([]lsp.Location, error) {
	return nil, nil
}

// References returns empty results.
func (m *MockYamlls) References(_ context.Context, _ *lsp.ReferenceParams) ([]lsp.Location, error) {
	return nil, nil
}

// DocumentSymbol returns empty results.
func (m *MockYamlls) DocumentSymbol(_ context.Context, _ *lsp.DocumentSymbolParams) ([]any, error) {
	return nil, nil
}

// --- All remaining lsp.Server methods are no-ops ---

func (m *MockYamlls) DidChangeConfiguration(_ context.Context, _ *lsp.DidChangeConfigurationParams) error {
	return nil
}
func (m *MockYamlls) DidChangeWatchedFiles(_ context.Context, _ *lsp.DidChangeWatchedFilesParams) error {
	return nil
}
func (m *MockYamlls) DidChangeWorkspaceFolders(_ context.Context, _ *lsp.DidChangeWorkspaceFoldersParams) error {
	return nil
}
func (m *MockYamlls) WillSave(_ context.Context, _ *lsp.WillSaveTextDocumentParams) error {
	return nil
}
func (m *MockYamlls) WillSaveWaitUntil(_ context.Context, _ *lsp.WillSaveTextDocumentParams) ([]lsp.TextEdit, error) {
	return nil, nil
}
func (m *MockYamlls) CodeAction(_ context.Context, _ *lsp.CodeActionParams) ([]lsp.CodeAction, error) {
	return nil, nil
}
func (m *MockYamlls) CodeLens(_ context.Context, _ *lsp.CodeLensParams) ([]lsp.CodeLens, error) {
	return nil, nil
}
func (m *MockYamlls) CodeLensResolve(_ context.Context, _ *lsp.CodeLens) (*lsp.CodeLens, error) {
	return nil, nil
}
func (m *MockYamlls) ColorPresentation(_ context.Context, _ *lsp.ColorPresentationParams) ([]lsp.ColorPresentation, error) {
	return nil, nil
}
func (m *MockYamlls) CompletionResolve(_ context.Context, item *lsp.CompletionItem) (*lsp.CompletionItem, error) {
	return item, nil
}
func (m *MockYamlls) Declaration(_ context.Context, _ *lsp.DeclarationParams) ([]lsp.Location, error) {
	return nil, nil
}
func (m *MockYamlls) DocumentColor(_ context.Context, _ *lsp.DocumentColorParams) ([]lsp.ColorInformation, error) {
	return nil, nil
}
func (m *MockYamlls) DocumentHighlight(_ context.Context, _ *lsp.DocumentHighlightParams) ([]lsp.DocumentHighlight, error) {
	return nil, nil
}
func (m *MockYamlls) DocumentLink(_ context.Context, _ *lsp.DocumentLinkParams) ([]lsp.DocumentLink, error) {
	return nil, nil
}
func (m *MockYamlls) DocumentLinkResolve(_ context.Context, link *lsp.DocumentLink) (*lsp.DocumentLink, error) {
	return link, nil
}
func (m *MockYamlls) ExecuteCommand(_ context.Context, _ *lsp.ExecuteCommandParams) (any, error) {
	return nil, nil
}
func (m *MockYamlls) FoldingRanges(_ context.Context, _ *lsp.FoldingRangeParams) ([]lsp.FoldingRange, error) {
	return nil, nil
}
func (m *MockYamlls) Formatting(_ context.Context, _ *lsp.DocumentFormattingParams) ([]lsp.TextEdit, error) {
	return nil, nil
}
func (m *MockYamlls) Implementation(_ context.Context, _ *lsp.ImplementationParams) ([]lsp.Location, error) {
	return nil, nil
}
func (m *MockYamlls) OnTypeFormatting(_ context.Context, _ *lsp.DocumentOnTypeFormattingParams) ([]lsp.TextEdit, error) {
	return nil, nil
}
func (m *MockYamlls) PrepareRename(_ context.Context, _ *lsp.PrepareRenameParams) (*lsp.Range, error) {
	return nil, nil
}
func (m *MockYamlls) RangeFormatting(_ context.Context, _ *lsp.DocumentRangeFormattingParams) ([]lsp.TextEdit, error) {
	return nil, nil
}
func (m *MockYamlls) Rename(_ context.Context, _ *lsp.RenameParams) (*lsp.WorkspaceEdit, error) {
	return nil, nil
}
func (m *MockYamlls) Request(_ context.Context, _ string, _ any) (any, error) {
	return nil, fmt.Errorf("not implemented")
}
func (m *MockYamlls) SelectionRange(_ context.Context, _ *lsp.SelectionRangeParams) ([]lsp.SelectionRange, error) {
	return nil, nil
}
func (m *MockYamlls) SemanticTokensFull(_ context.Context, _ *lsp.SemanticTokensParams) (*lsp.SemanticTokens, error) {
	return nil, nil
}
func (m *MockYamlls) SemanticTokensFullDelta(_ context.Context, _ *lsp.SemanticTokensDeltaParams) (any, error) {
	return nil, nil
}
func (m *MockYamlls) SemanticTokensRange(_ context.Context, _ *lsp.SemanticTokensRangeParams) (*lsp.SemanticTokens, error) {
	return nil, nil
}
func (m *MockYamlls) SemanticTokensRefresh(_ context.Context) error { return nil }
func (m *MockYamlls) SignatureHelp(_ context.Context, _ *lsp.SignatureHelpParams) (*lsp.SignatureHelp, error) {
	return nil, nil
}
func (m *MockYamlls) Symbols(_ context.Context, _ *lsp.WorkspaceSymbolParams) ([]lsp.SymbolInformation, error) {
	return nil, nil
}
func (m *MockYamlls) TypeDefinition(_ context.Context, _ *lsp.TypeDefinitionParams) ([]lsp.Location, error) {
	return nil, nil
}
func (m *MockYamlls) WorkDoneProgressCancel(_ context.Context, _ *lsp.WorkDoneProgressCancelParams) error {
	return nil
}
func (m *MockYamlls) LogTrace(_ context.Context, _ *lsp.LogTraceParams) error { return nil }
func (m *MockYamlls) SetTrace(_ context.Context, _ *lsp.SetTraceParams) error { return nil }
func (m *MockYamlls) ShowDocument(_ context.Context, _ *lsp.ShowDocumentParams) (*lsp.ShowDocumentResult, error) {
	return nil, nil
}
func (m *MockYamlls) WillCreateFiles(_ context.Context, _ *lsp.CreateFilesParams) (*lsp.WorkspaceEdit, error) {
	return nil, nil
}
func (m *MockYamlls) DidCreateFiles(_ context.Context, _ *lsp.CreateFilesParams) error { return nil }
func (m *MockYamlls) WillRenameFiles(_ context.Context, _ *lsp.RenameFilesParams) (*lsp.WorkspaceEdit, error) {
	return nil, nil
}
func (m *MockYamlls) DidRenameFiles(_ context.Context, _ *lsp.RenameFilesParams) error { return nil }
func (m *MockYamlls) WillDeleteFiles(_ context.Context, _ *lsp.DeleteFilesParams) (*lsp.WorkspaceEdit, error) {
	return nil, nil
}
func (m *MockYamlls) DidDeleteFiles(_ context.Context, _ *lsp.DeleteFilesParams) error { return nil }
func (m *MockYamlls) CodeLensRefresh(_ context.Context) error                          { return nil }
func (m *MockYamlls) PrepareCallHierarchy(_ context.Context, _ *lsp.CallHierarchyPrepareParams) ([]lsp.CallHierarchyItem, error) {
	return nil, nil
}
func (m *MockYamlls) IncomingCalls(_ context.Context, _ *lsp.CallHierarchyIncomingCallsParams) ([]lsp.CallHierarchyIncomingCall, error) {
	return nil, nil
}
func (m *MockYamlls) OutgoingCalls(_ context.Context, _ *lsp.CallHierarchyOutgoingCallsParams) ([]lsp.CallHierarchyOutgoingCall, error) {
	return nil, nil
}
func (m *MockYamlls) LinkedEditingRange(_ context.Context, _ *lsp.LinkedEditingRangeParams) (*lsp.LinkedEditingRanges, error) {
	return nil, nil
}
func (m *MockYamlls) Moniker(_ context.Context, _ *lsp.MonikerParams) ([]lsp.Moniker, error) {
	return nil, nil
}

// compile-time assertion: MockYamlls must satisfy lsp.Server.
var _ lsp.Server = (*MockYamlls)(nil)

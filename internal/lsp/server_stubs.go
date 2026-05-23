// Package lsp contains proxy-or-noop stubs for the ~50 protocol.Server
// methods that SchemaLock does not fully implement. They are grouped by LSP
// spec section: lifecycle, workspace, text-document (requests),
// text-document (notifications), file operations, call hierarchy, semantic
// tokens, and miscellaneous.
//
// Three of the text-document request methods — Definition, References, and
// DocumentSymbol — are no longer pure no-ops: they unconditionally proxy to
// yaml-ls (via s.yamlls.Call*). All other stubs remain no-ops.
//
// No logging is done here intentionally: helm-ls debug-logs unimplemented
// methods, but SchemaLock keeps stderr clean during integration so that the
// validatingWriter contract is not violated by spurious output.
package lsp

import (
	"context"

	lsp "go.lsp.dev/protocol"
)

// --- Lifecycle / tracing ---

// WorkDoneProgressCancel is not implemented.
func (s *Server) WorkDoneProgressCancel(_ context.Context, _ *lsp.WorkDoneProgressCancelParams) error {
	return nil
}

// LogTrace is not implemented.
func (s *Server) LogTrace(_ context.Context, _ *lsp.LogTraceParams) error {
	return nil
}

// SetTrace is not implemented.
func (s *Server) SetTrace(_ context.Context, _ *lsp.SetTraceParams) error {
	return nil
}

// --- Workspace ---

// DidChangeConfiguration is not implemented.
func (s *Server) DidChangeConfiguration(_ context.Context, _ *lsp.DidChangeConfigurationParams) error {
	return nil
}

// DidChangeWorkspaceFolders is not implemented.
func (s *Server) DidChangeWorkspaceFolders(_ context.Context, _ *lsp.DidChangeWorkspaceFoldersParams) error {
	return nil
}

// ExecuteCommand is not implemented.
func (s *Server) ExecuteCommand(_ context.Context, _ *lsp.ExecuteCommandParams) (any, error) {
	return nil, nil
}

// Symbols is not implemented.
func (s *Server) Symbols(_ context.Context, _ *lsp.WorkspaceSymbolParams) ([]lsp.SymbolInformation, error) {
	return nil, nil
}

// --- File operations (workspace) ---

// WillCreateFiles is not implemented.
func (s *Server) WillCreateFiles(_ context.Context, _ *lsp.CreateFilesParams) (*lsp.WorkspaceEdit, error) {
	return nil, nil
}

// DidCreateFiles is not implemented.
func (s *Server) DidCreateFiles(_ context.Context, _ *lsp.CreateFilesParams) error {
	return nil
}

// WillRenameFiles is not implemented.
func (s *Server) WillRenameFiles(_ context.Context, _ *lsp.RenameFilesParams) (*lsp.WorkspaceEdit, error) {
	return nil, nil
}

// DidRenameFiles is not implemented.
func (s *Server) DidRenameFiles(_ context.Context, _ *lsp.RenameFilesParams) error {
	return nil
}

// WillDeleteFiles is not implemented.
func (s *Server) WillDeleteFiles(_ context.Context, _ *lsp.DeleteFilesParams) (*lsp.WorkspaceEdit, error) {
	return nil, nil
}

// DidDeleteFiles is not implemented.
func (s *Server) DidDeleteFiles(_ context.Context, _ *lsp.DeleteFilesParams) error {
	return nil
}

// --- Text document: queries (requests) ---

// CodeAction is not implemented.
func (s *Server) CodeAction(_ context.Context, _ *lsp.CodeActionParams) ([]lsp.CodeAction, error) {
	return nil, nil
}

// CodeLens is not implemented.
func (s *Server) CodeLens(_ context.Context, _ *lsp.CodeLensParams) ([]lsp.CodeLens, error) {
	return nil, nil
}

// CodeLensResolve is not implemented.
func (s *Server) CodeLensResolve(_ context.Context, _ *lsp.CodeLens) (*lsp.CodeLens, error) {
	return nil, nil
}

// CodeLensRefresh is not implemented.
func (s *Server) CodeLensRefresh(_ context.Context) error {
	return nil
}

// ColorPresentation is not implemented.
func (s *Server) ColorPresentation(_ context.Context, _ *lsp.ColorPresentationParams) ([]lsp.ColorPresentation, error) {
	return nil, nil
}

// CompletionResolve is not implemented.
func (s *Server) CompletionResolve(_ context.Context, _ *lsp.CompletionItem) (*lsp.CompletionItem, error) {
	return nil, nil
}

// Declaration is not implemented.
func (s *Server) Declaration(_ context.Context, _ *lsp.DeclarationParams) ([]lsp.Location, error) {
	return nil, nil
}

// Definition proxies textDocument/definition to yaml-ls. Returns (nil, nil)
// when yaml-ls is absent or inactive — the caller treats nil as "no results".
// No router check: Definition always forwards to yaml-ls regardless of
// document ownership (yaml-ls provides "go to schema" navigation for owned
// files when a schema URI is attached; we have nothing to add in v1).
func (s *Server) Definition(ctx context.Context, params *lsp.DefinitionParams) ([]lsp.Location, error) {
	return s.yamlls.CallDefinition(ctx, params)
}

// DocumentColor is not implemented.
func (s *Server) DocumentColor(_ context.Context, _ *lsp.DocumentColorParams) ([]lsp.ColorInformation, error) {
	return nil, nil
}

// DocumentHighlight is not implemented.
func (s *Server) DocumentHighlight(_ context.Context, _ *lsp.DocumentHighlightParams) ([]lsp.DocumentHighlight, error) {
	return nil, nil
}

// DocumentLink is not implemented.
func (s *Server) DocumentLink(_ context.Context, _ *lsp.DocumentLinkParams) ([]lsp.DocumentLink, error) {
	return nil, nil
}

// DocumentLinkResolve is not implemented.
func (s *Server) DocumentLinkResolve(_ context.Context, _ *lsp.DocumentLink) (*lsp.DocumentLink, error) {
	return nil, nil
}

// DocumentSymbol proxies textDocument/documentSymbol to yaml-ls. Returns
// (nil, nil) when yaml-ls is absent or inactive. No router check: always
// forwards regardless of document ownership.
func (s *Server) DocumentSymbol(ctx context.Context, params *lsp.DocumentSymbolParams) ([]any, error) {
	return s.yamlls.CallDocumentSymbol(ctx, params)
}

// FoldingRanges is not implemented.
func (s *Server) FoldingRanges(_ context.Context, _ *lsp.FoldingRangeParams) ([]lsp.FoldingRange, error) {
	return nil, nil
}

// Formatting is not implemented.
func (s *Server) Formatting(_ context.Context, _ *lsp.DocumentFormattingParams) ([]lsp.TextEdit, error) {
	return nil, nil
}

// Implementation is not implemented.
func (s *Server) Implementation(_ context.Context, _ *lsp.ImplementationParams) ([]lsp.Location, error) {
	return nil, nil
}

// OnTypeFormatting is not implemented.
func (s *Server) OnTypeFormatting(_ context.Context, _ *lsp.DocumentOnTypeFormattingParams) ([]lsp.TextEdit, error) {
	return nil, nil
}

// PrepareRename is not implemented.
func (s *Server) PrepareRename(_ context.Context, _ *lsp.PrepareRenameParams) (*lsp.Range, error) {
	return nil, nil
}

// RangeFormatting is not implemented.
func (s *Server) RangeFormatting(_ context.Context, _ *lsp.DocumentRangeFormattingParams) ([]lsp.TextEdit, error) {
	return nil, nil
}

// References proxies textDocument/references to yaml-ls. Returns (nil, nil)
// when yaml-ls is absent or inactive. No router check: always forwards
// regardless of document ownership.
func (s *Server) References(ctx context.Context, params *lsp.ReferenceParams) ([]lsp.Location, error) {
	return s.yamlls.CallReferences(ctx, params)
}

// Rename is not implemented.
func (s *Server) Rename(_ context.Context, _ *lsp.RenameParams) (*lsp.WorkspaceEdit, error) {
	return nil, nil
}

// SignatureHelp is not implemented.
func (s *Server) SignatureHelp(_ context.Context, _ *lsp.SignatureHelpParams) (*lsp.SignatureHelp, error) {
	return nil, nil
}

// TypeDefinition is not implemented.
func (s *Server) TypeDefinition(_ context.Context, _ *lsp.TypeDefinitionParams) ([]lsp.Location, error) {
	return nil, nil
}

// WillSaveWaitUntil is not implemented.
func (s *Server) WillSaveWaitUntil(_ context.Context, _ *lsp.WillSaveTextDocumentParams) ([]lsp.TextEdit, error) {
	return nil, nil
}

// ShowDocument is not implemented.
func (s *Server) ShowDocument(_ context.Context, _ *lsp.ShowDocumentParams) (*lsp.ShowDocumentResult, error) {
	return nil, nil
}

// --- Text document: notifications ---

// DidSave is not implemented.
func (s *Server) DidSave(_ context.Context, _ *lsp.DidSaveTextDocumentParams) error {
	return nil
}

// WillSave is not implemented.
func (s *Server) WillSave(_ context.Context, _ *lsp.WillSaveTextDocumentParams) error {
	return nil
}

// --- Call hierarchy ---

// PrepareCallHierarchy is not implemented.
func (s *Server) PrepareCallHierarchy(_ context.Context, _ *lsp.CallHierarchyPrepareParams) ([]lsp.CallHierarchyItem, error) {
	return nil, nil
}

// IncomingCalls is not implemented.
func (s *Server) IncomingCalls(_ context.Context, _ *lsp.CallHierarchyIncomingCallsParams) ([]lsp.CallHierarchyIncomingCall, error) {
	return nil, nil
}

// OutgoingCalls is not implemented.
func (s *Server) OutgoingCalls(_ context.Context, _ *lsp.CallHierarchyOutgoingCallsParams) ([]lsp.CallHierarchyOutgoingCall, error) {
	return nil, nil
}

// --- Semantic tokens ---

// SemanticTokensFull is not implemented.
func (s *Server) SemanticTokensFull(_ context.Context, _ *lsp.SemanticTokensParams) (*lsp.SemanticTokens, error) {
	return nil, nil
}

// SemanticTokensFullDelta is not implemented.
func (s *Server) SemanticTokensFullDelta(_ context.Context, _ *lsp.SemanticTokensDeltaParams) (any, error) {
	return nil, nil
}

// SemanticTokensRange is not implemented.
func (s *Server) SemanticTokensRange(_ context.Context, _ *lsp.SemanticTokensRangeParams) (*lsp.SemanticTokens, error) {
	return nil, nil
}

// SemanticTokensRefresh is not implemented.
func (s *Server) SemanticTokensRefresh(_ context.Context) error {
	return nil
}

// --- Miscellaneous ---

// LinkedEditingRange is not implemented.
func (s *Server) LinkedEditingRange(_ context.Context, _ *lsp.LinkedEditingRangeParams) (*lsp.LinkedEditingRanges, error) {
	return nil, nil
}

// Moniker is not implemented.
func (s *Server) Moniker(_ context.Context, _ *lsp.MonikerParams) ([]lsp.Moniker, error) {
	return nil, nil
}

// Request handles any non-standard LSP request not matched by the typed
// dispatcher. Stub body — wired in Chunk C/D. Final behavior: return
// jsonrpc2.ErrMethodNotFound for unknown methods; the custom-method chain
// in front of protocol.ServerHandler handles the schemalock/* methods
// before this fallback fires.
func (s *Server) Request(_ context.Context, _ string, _ any) (any, error) {
	return nil, nil
}

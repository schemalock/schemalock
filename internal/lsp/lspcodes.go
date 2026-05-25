package lsp

import lspprot "go.lsp.dev/protocol"

// VS Code's language client maps LSP CompletionItemKind to its TypeScript
// enum by subtracting 1 from the wire value, so LSP wire 7 ->
// vscode.CompletionItemKind.Class and LSP wire 13 ->
// vscode.CompletionItemKind.Enum. We deliberately send Class for object
// property completions (distinctive icon vs. Field) and Enum for enum
// values plus kind-bootstrap items.
const (
	// completionKindClass is what we send for object property completions.
	// Wire value: 7 (renders as Class in VS Code).
	completionKindClass lspprot.CompletionItemKind = 7

	// completionKindEnum is what we send for enum value completions and
	// kind-bootstrap items. Wire value: 13 (renders as Enum in VS Code).
	completionKindEnum lspprot.CompletionItemKind = 13
)

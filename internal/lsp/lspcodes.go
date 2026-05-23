package lsp

import lspprot "go.lsp.dev/protocol"

// completionKindField and completionKindEnumMember preserve the CompletionItemKind
// wire values that SchemaLock has historically sent to clients (7 and 13
// respectively). These differ from the go.lsp.dev/protocol constants
// (Field=5, EnumMember=20) because our internal/lsp/protocol/types.go was
// using non-standard values that VS Code's client has already been trained on.
// Keeping the same numeric values here ensures M10a introduces no wire-format
// change. This can be revisited in M11 with a client-side update.
const (
	// completionKindField is the CompletionItemKind value we send for object
	// property completions. Wire value: 7.
	completionKindField lspprot.CompletionItemKind = 7

	// completionKindEnumMember is the CompletionItemKind value we send for
	// enum value completions and kind-bootstrap items. Wire value: 13.
	completionKindEnumMember lspprot.CompletionItemKind = 13
)

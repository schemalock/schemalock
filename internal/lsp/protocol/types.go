// Package protocol defines SchemaLock-specific LSP parameter and result types.
// Standard LSP types (Position, Range, Diagnostic, CompletionItem, Hover, etc.)
// are provided by go.lsp.dev/protocol. The schemalock/* custom request,
// response, and notification payload types that have no equivalent in the spec
// live here.
package protocol

// ResolveSchemaParams are the parameters for schemalock/resolveSchema.
type ResolveSchemaParams struct {
	// TextDocumentURI identifies the YAML document being resolved.
	// Currently used only for logging; resolution itself is driven by
	// APIVersion and Kind.
	TextDocumentURI string `json:"textDocumentUri"`
	// APIVersion is the document's apiVersion (e.g.
	// "operator.victoriametrics.com/v1beta1").
	APIVersion string `json:"apiVersion"`
	// Kind is the document's kind (e.g. "VMCluster").
	Kind string `json:"kind"`
}

// ResolveSchemaResult is the success response for schemalock/resolveSchema.
type ResolveSchemaResult struct {
	// SchemaURI is a file:// URI to the cached, integrity-verified schema file.
	// The client (yaml-language-server, via the contributor callback) reads the
	// file directly from disk.
	SchemaURI string `json:"schemaUri"`
}

// State mirrors lsp.State; kept here as int to avoid an import cycle.
type State int

const (
	StateUnindexable State = 0
	StatePinned      State = 1
	StateUnpinned    State = 2
	StatePreview     State = 3
	StateError       State = 4
)

// GetDocumentStateParams is the request payload for MethodGetDocumentState.
type GetDocumentStateParams struct {
	URI string `json:"uri"`
}

// GetDocumentStateResult is the response payload.
type GetDocumentStateResult struct {
	State   State  `json:"state"`
	Group   string `json:"group,omitempty"`
	Kind    string `json:"kind,omitempty"`
	Version string `json:"version,omitempty"`
	Source  string `json:"source,omitempty"`
	ErrMsg  string `json:"errMsg,omitempty"`
}

// ListVersionsForGroupParams / Result.
type ListVersionsForGroupParams struct {
	Group string `json:"group"`
}
type ListVersionsForGroupResult struct {
	Versions []string `json:"versions"`
	Indexed  bool     `json:"indexed"`
}

// SetDocumentVersionOverrideParams / Result. Version == "" clears the override.
type SetDocumentVersionOverrideParams struct {
	URI     string `json:"uri"`
	Version string `json:"version"`
}
type SetDocumentVersionOverrideResult struct {
	OK bool `json:"ok"`
}

// RetryResolveParams / Result.
type RetryResolveParams struct {
	Group string `json:"group"`
	URI   string `json:"uri,omitempty"`
}
type RetryResolveResult struct {
	OK bool `json:"ok"`
}

// CacheDebugResult is intentionally opaque JSON (a snapshot of internal maps).
type CacheDebugResult struct {
	Snapshot map[string]any `json:"snapshot"`
}

// DocumentStateChangedParams is the notification payload (no result).
type DocumentStateChangedParams struct {
	URI     string `json:"uri"`
	State   State  `json:"state"`
	Group   string `json:"group,omitempty"`
	Kind    string `json:"kind,omitempty"`
	Version string `json:"version,omitempty"`
	Source  string `json:"source,omitempty"`
	ErrMsg  string `json:"errMsg,omitempty"`
}

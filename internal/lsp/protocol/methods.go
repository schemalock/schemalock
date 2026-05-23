// Package protocol defines SchemaLock-specific LSP method-name constants and
// custom request/response types. Standard LSP method constants (MethodInitialize,
// MethodHover, etc.) are provided by go.lsp.dev/protocol. Seven
// schemalock/* custom method names that have no equivalent in the spec live here.
package protocol

const (
	// MethodResolveSchema is a SchemaLock-specific request used by the VS Code
	// extension's redhat.vscode-yaml contributor callback to resolve an
	// (apiVersion, kind) pair to a file:// URI of the integrity-verified schema
	// on disk. Custom methods are namespaced with the "schemalock/" prefix.
	MethodResolveSchema = "schemalock/resolveSchema"
)

const (
	// MethodGetDocumentState returns the current ResolveResult for a URI.
	MethodGetDocumentState = "schemalock/getDocumentState"
	// MethodListVersionsForGroup returns the CDN versions list for a group.
	MethodListVersionsForGroup = "schemalock/listVersionsForGroup"
	// MethodSetDocumentVersionOverride sets or clears a per-doc version override.
	MethodSetDocumentVersionOverride = "schemalock/setDocumentVersionOverride"
	// MethodRetryResolve clears the back-off for (group) and re-resolves uri.
	MethodRetryResolve = "schemalock/retryResolve"
	// MethodCacheDebug returns an internal snapshot of all caches.
	MethodCacheDebug = "schemalock/cacheDebug"
	// MethodDocumentStateChanged is a server->client notification fired when
	// a URI's ResolveResult changes (lockfile reload, override set/cleared,
	// retry success).
	MethodDocumentStateChanged = "schemalock/documentStateChanged"
)

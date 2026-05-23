// Package protocol defines SchemaLock-specific LSP server error codes and
// custom method constants for the SchemaLock LSP server.
//
// SchemaLock-specific server error codes occupy the server-defined range
// (-32000 to -32099) reserved by the JSON-RPC 2.0 specification. They are
// used in error responses to schemalock/* custom requests and are passed via
// jsonrpc2.NewError(jsonrpc2.Code(code), msg) once the server migrates to
// go.lsp.dev/jsonrpc2.
package protocol

// SchemaLock-specific server error codes (server-defined range: -32099..-32000).
const (
	// CodeNoMatch is returned by schemalock/resolveSchema when no lock entry
	// matches the requested (apiVersion, kind) pair.
	CodeNoMatch = -32001

	// CodeAmbiguousKind is returned by schemalock/resolveSchema when two or
	// more lock entries share the same group and kind.
	CodeAmbiguousKind = -32002

	// CodeFetchFailed is returned by schemalock/resolveSchema when the schema
	// is not in cache and the CDN fetch failed.
	CodeFetchFailed = -32003
)

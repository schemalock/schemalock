package lsp

import (
	"context"
	"runtime"
	"strings"

	"go.lsp.dev/jsonrpc2"
	lsp "go.lsp.dev/protocol"

	"github.com/schemalock/app/internal/lsp/protocol"
	"github.com/schemalock/app/internal/yamldoc"
)

// bootstrapKindCompletions is a placeholder for the kind-value completion
// trigger that used to consult the lockfile-backed SchemaResolver's
// KindsForGroup index. The intent-based resolver does not maintain a
// global kind→group index; replacing this requires fetching manifest.json
// for the intent-pinned version of the doc's group and listing its kinds.
//
// TODO(intent-bootstrap): reimplement against intent.Lookup + a per-pin
// CDN manifest fetch. Until then, bootstrap completion returns nil and the
// regular schema-driven completion path takes over.
func bootstrapKindCompletions(
	doc yamldoc.Document,
	cursorCtx cursorContext,
) []lsp.CompletionItem {
	_ = doc
	_ = cursorCtx
	return nil
}

// requeueAllOpenDocuments iterates over all open documents and re-submits
// validation jobs with the current resolver snapshot. Uses cancelMu to safely
// snapshot the set of open URIs. For each document it also fires a
// schemalock/documentStateChanged notification so the editor status bar
// reflects the new state immediately (e.g. an Unpinned doc becoming Pinned
// after the user committed it to the lockfile). The client deduplicates
// identical states, so notifying unconditionally is safe.
func (s *Server) requeueAllOpenDocuments() {
	// Snapshot the open URI set under cancelMu.
	s.cancelMu.Lock()
	uris := make([]string, 0, len(s.cancelFuncs))
	for uri := range s.cancelFuncs {
		uris = append(uris, uri)
	}
	s.cancelMu.Unlock()

	ctx := context.Background()
	for _, uri := range uris {
		text, version, ok := s.docs.Get(uri)
		if !ok {
			continue
		}
		// Re-resolve to pick up fresh CDN schema bytes (e.g. after a lockfile
		// reload that may have added or removed a pin for this document).
		res := s.router.Resolve(ctx, uri, text)
		s.enqueueValidation(uri, text, version, res)
		if res.State != StateUnindexable {
			s.notifyDocumentStateChanged(ctx, uri, res)
		}
	}
}

// isIntentOrLockfileEvent returns true if the URI refers to a schemalock.yaml
// or schemalock.lock file — either of which should trigger intent invalidation.
func isIntentOrLockfileEvent(uri string) bool {
	return strings.HasSuffix(uri, "schemalock.yaml") ||
		strings.HasSuffix(uri, "schemalock.lock")
}

// uriToPath converts a file:// URI to a local filesystem path.
// Non-file URIs are returned unchanged.
func uriToPath(uri string) string {
	if len(uri) > 7 && uri[:7] == "file://" {
		return uri[7:]
	}
	return uri
}

// resolveSchemaHelper is the pure business-logic core of schemalock/resolveSchema.
// Returns the result and an error. When non-nil, the error is a jsonrpc2.Error
// carrying the appropriate SchemaLock error code from protocol/codes.go.
//
// The lockfile-based resolver has been replaced by intent.Lookup in Task 5.
// This handler returns CodeNoMatch for all requests until a replacement
// implementation is wired in a later task.
func (s *Server) resolveSchemaHelper(_ context.Context, params protocol.ResolveSchemaParams) (protocol.ResolveSchemaResult, error) {
	s.log.Printf("resolveSchema: lockfile resolver removed; no schema URI for %s/%s", params.APIVersion, params.Kind)
	return protocol.ResolveSchemaResult{}, jsonrpc2.NewError(
		jsonrpc2.Code(protocol.CodeNoMatch),
		"resolveSchema: schema URI resolution via lockfile is no longer supported",
	)
}

// pathToFileURI converts a local filesystem path to a file:// URI.
// On Unix, absolute paths (starting with "/") become "file://<path>".
// On Windows, absolute paths are normalised so backslashes become forward
// slashes and the result is "file:///<drive>/<rest>" (three slashes per RFC).
func pathToFileURI(path string) string {
	if runtime.GOOS == "windows" {
		// Normalise backslashes and add the extra leading slash.
		path = strings.ReplaceAll(path, `\`, "/")
		if len(path) > 0 && path[0] != '/' {
			// Absolute Windows path, e.g. "C:/foo" → "file:///C:/foo".
			return "file:///" + path
		}
		return "file://" + path
	}
	// Unix: absolute paths start with "/".
	return "file://" + path
}

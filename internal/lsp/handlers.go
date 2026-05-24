package lsp

import (
	"context"
	"fmt"
	"runtime"
	"strings"

	"go.lsp.dev/jsonrpc2"
	lsp "go.lsp.dev/protocol"

	"github.com/schemalock/app/internal/lsp/protocol"
	"github.com/schemalock/app/internal/yamldoc"
)

// bootstrapKindCompletions returns completion items for the value of a
// root-level `kind` field when apiVersion is set on the document but kind is
// still empty. Used to surface every Kind in the apiVersion's group so the
// user can pick one without typing it from memory.
//
// Returns nil when not applicable:
//   - doc.APIVersion is empty
//   - doc.Kind is already set (don't override schema-driven completion)
//   - apiVersion has no "/" (core types — out of scope per Resolve semantics)
//   - cursor is not at the value of the root-level `kind` field
//   - the apiVersion's group has no entries in the lockfile
//
// Callers should check for nil and fall back to the regular completion path.
// An empty (non-nil) slice means "the trigger fired but produced zero kinds"
// and should still terminate the bootstrap branch.
//
// Note: bootstrapKindCompletions is never called with a nil resolver in
// production — the caller short-circuits at the resolver-nil check before
// reaching this helper. Do not add a redundant nil check here.
func bootstrapKindCompletions(
	doc yamldoc.Document,
	cursorCtx cursorContext,
	resolver *SchemaResolver,
) []lsp.CompletionItem {
	if doc.APIVersion == "" || doc.Kind != "" {
		return nil
	}
	if cursorCtx.Pointer != "/kind" || cursorCtx.IsKeyPosition {
		return nil
	}
	group, _, ok := strings.Cut(doc.APIVersion, "/")
	if !ok || group == "" {
		return nil
	}
	kinds := resolver.KindsForGroup(group)
	if len(kinds) == 0 {
		return nil
	}
	detail := "Kind from " + group
	items := make([]lsp.CompletionItem, 0, len(kinds))
	for i, kind := range kinds {
		items = append(items, lsp.CompletionItem{
			Label:      kind,
			Kind:       completionKindEnumMember,
			InsertText: kind,
			SortText:   fmt.Sprintf("%04d_%s", i, kind),
			Detail:     detail,
			Documentation: lsp.MarkupContent{
				Kind:  lsp.Markdown,
				Value: "_Resolved from schemalock.lock_",
			},
		})
	}
	return items
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

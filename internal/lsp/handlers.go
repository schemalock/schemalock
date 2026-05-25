package lsp

import (
	"context"
	"fmt"
	"runtime"
	"sort"
	"strings"

	"go.lsp.dev/jsonrpc2"
	lsp "go.lsp.dev/protocol"

	"github.com/schemalock/app/internal/intent"
	"github.com/schemalock/app/internal/lsp/protocol"
	"github.com/schemalock/app/internal/yamldoc"
)

// bootstrapKindCompletions returns a sorted list of Kind completion items for
// the value position of "kind:" in a YAML document whose apiVersion is set but
// kind is empty.
//
// Trigger predicate (all must be true):
//   - doc.APIVersion is non-empty
//   - doc.Kind is empty
//   - cursorCtx.Pointer == "/kind"
//   - cursorCtx.IsKeyPosition is false
//   - cdn is non-nil
//
// Version-resolution order: intent pin for the document's directory first,
// CDN versions()[0] as fallback. On any resolution or fetch failure the
// function returns nil (silent skip); the caller falls through to the empty
// sentinel branch in Completion.
func bootstrapKindCompletions(
	ctx context.Context,
	doc yamldoc.Document,
	cursorCtx cursorContext,
	uri string,
	intentLookup *intent.Lookup,
	cdn *cdnResolver,
	wordRange lsp.Range,
) []lsp.CompletionItem {
	if doc.APIVersion == "" || doc.Kind != "" {
		return nil
	}
	if cursorCtx.Pointer != "/kind" || cursorCtx.IsKeyPosition {
		return nil
	}
	if cdn == nil {
		return nil
	}

	group := groupFromAPIVersion(doc.APIVersion)
	if group == "" {
		return nil
	}

	// Resolve operator version: intent pin first, CDN latest as fallback.
	version := ""
	if intentLookup != nil {
		v, ok, _ := intentLookup.PinFor(dirFromURI(uri), "kubernetes", group)
		if ok && v != "" {
			version = v
		}
	}
	if version == "" {
		versions, err := cdn.versions(ctx, "kubernetes", group)
		if err != nil || len(versions) == 0 {
			return nil
		}
		version = versions[0]
	}

	manifest, err := cdn.manifest(ctx, "kubernetes", group, version)
	if err != nil {
		return nil
	}

	kinds := make([]string, 0, len(manifest.Kinds))
	for k := range manifest.Kinds {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)

	if len(kinds) == 0 {
		return nil
	}

	items := make([]lsp.CompletionItem, 0, len(kinds))
	for i, kind := range kinds {
		items = append(items, lsp.CompletionItem{
			Label:      kind,
			Kind:       completionKindEnumMember,
			Detail:     "Kind from " + group + " @ " + version,
			FilterText: kind,
			SortText:   fmt.Sprintf("%04d_%s", i, kind),
			TextEdit: &lsp.TextEdit{
				Range:   wordRange,
				NewText: kind,
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

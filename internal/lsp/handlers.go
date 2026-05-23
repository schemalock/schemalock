package lsp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"go.lsp.dev/jsonrpc2"
	lsp "go.lsp.dev/protocol"

	"github.com/schemalock/app/internal/cache"
	"github.com/schemalock/app/internal/lockfile"
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

// isLockfileEvent returns true if the given URI refers to the server's
// schemalock.lock file. It matches either by exact file:// URI equality
// against the known lockfilePath, or by a URI suffix of "schemalock.lock".
func isLockfileEvent(uri, lockfilePath string) bool {
	if strings.HasSuffix(uri, "schemalock.lock") {
		return true
	}
	if lockfilePath == "" {
		return false
	}
	return uri == "file://"+lockfilePath
}

// findAndReadLock walks up from dir until it finds schemalock.lock or reaches
// the filesystem root. Returns the parsed LockFile and the path where it was
// found.
func findAndReadLock(dir string) (lockfile.LockFile, string, error) {
	if dir == "" {
		return lockfile.LockFile{}, "", fmt.Errorf("no workspace root provided")
	}

	current := dir
	for {
		candidate := filepath.Join(current, "schemalock.lock")
		if _, err := os.Stat(candidate); err == nil {
			lf, err := lockfile.ReadLock(candidate)
			if err != nil {
				return lockfile.LockFile{}, "", fmt.Errorf("reading %s: %w", candidate, err)
			}
			return lf, candidate, nil
		}

		parent := filepath.Dir(current)
		if parent == current {
			// Reached the filesystem root.
			break
		}
		current = parent
	}
	return lockfile.LockFile{}, "", fmt.Errorf("schemalock.lock not found (walked up from %s)", dir)
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
func (s *Server) resolveSchemaHelper(ctx context.Context, params protocol.ResolveSchemaParams) (protocol.ResolveSchemaResult, error) {
	s.mu.RLock()
	resolver := s.resolver
	s.mu.RUnlock()

	if resolver == nil {
		s.log.Printf("resolveSchema: no resolver (no lockfile loaded at initialize time)")
		return protocol.ResolveSchemaResult{}, jsonrpc2.NewError(
			jsonrpc2.Code(protocol.CodeNoMatch),
			"resolveSchema: no lockfile was loaded at initialize time; cannot resolve schemas",
		)
	}

	entry, err := resolver.Resolve(params.APIVersion, params.Kind)
	if err != nil {
		if errors.Is(err, ErrAmbiguousKind) {
			s.log.Printf("resolveSchema: ambiguous kind: %v", err)
			return protocol.ResolveSchemaResult{}, jsonrpc2.NewError(
				jsonrpc2.Code(protocol.CodeAmbiguousKind),
				fmt.Sprintf("resolveSchema: %s", err),
			)
		}
		if errors.Is(err, ErrNoMatch) {
			s.log.Printf("resolveSchema: no match for %s/%s: %v", params.APIVersion, params.Kind, err)
			return protocol.ResolveSchemaResult{}, jsonrpc2.NewError(
				jsonrpc2.Code(protocol.CodeNoMatch),
				fmt.Sprintf("resolveSchema: %s", err),
			)
		}
		s.log.Printf("resolveSchema: resolve error: %v", err)
		return protocol.ResolveSchemaResult{}, jsonrpc2.NewError(
			jsonrpc2.InternalError,
			fmt.Sprintf("resolveSchema: %s", err),
		)
	}

	// Ensure the schema bytes are on disk; fetch from CDN if necessary.
	_, readErr := s.cacheDir.ReadSchema(entry.Ecosystem, entry.Group, entry.ReleaseVersion, entry.Kind)
	if readErr != nil {
		if errors.Is(readErr, cache.ErrNotFound) {
			// Cache miss — fetch from CDN and persist.
			schemaBytes, fetchErr := s.reg.FetchSchema(ctx, entry.Ecosystem, entry.Group, entry.ReleaseVersion, entry.Kind)
			if fetchErr != nil {
				s.log.Printf("resolveSchema: fetch %s/%s/%s/%s: %v",
					entry.Ecosystem, entry.Group, entry.ReleaseVersion, entry.Kind, fetchErr)
				return protocol.ResolveSchemaResult{}, jsonrpc2.NewError(
					jsonrpc2.Code(protocol.CodeFetchFailed),
					fmt.Sprintf("resolveSchema: fetch failed: %s", fetchErr),
				)
			}
			if writeErr := s.cacheDir.WriteSchema(
				entry.Ecosystem, entry.Group, entry.ReleaseVersion, entry.Kind,
				entry.Integrity, schemaBytes,
			); writeErr != nil {
				s.log.Printf("resolveSchema: write cache %s/%s/%s/%s: %v",
					entry.Ecosystem, entry.Group, entry.ReleaseVersion, entry.Kind, writeErr)
				return protocol.ResolveSchemaResult{}, jsonrpc2.NewError(
					jsonrpc2.Code(protocol.CodeFetchFailed),
					fmt.Sprintf("resolveSchema: cache write failed: %s", writeErr),
				)
			}
		} else {
			s.log.Printf("resolveSchema: read cache %s/%s/%s/%s: %v",
				entry.Ecosystem, entry.Group, entry.ReleaseVersion, entry.Kind, readErr)
			return protocol.ResolveSchemaResult{}, jsonrpc2.NewError(
				jsonrpc2.InternalError,
				fmt.Sprintf("resolveSchema: cache read error: %s", readErr),
			)
		}
	}

	path := s.cacheDir.SchemaPath(entry.Ecosystem, entry.Group, entry.ReleaseVersion, entry.Kind)
	uri := pathToFileURI(path)

	s.log.Printf("resolveSchema: %s/%s -> %s", params.APIVersion, params.Kind, uri)
	return protocol.ResolveSchemaResult{SchemaURI: uri}, nil
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

// Package lsp implements the SchemaLock LSP server over stdio.
package lsp

import "sync"

// docState holds the current content of one open text document.
type docState struct {
	version uint32
	text    string
}

// DocumentStore is a thread-safe in-memory store of open text documents keyed
// by URI. It tracks version numbers and full document text. PoC supports
// full-text sync only (TextDocumentSyncKind.Full).
type DocumentStore struct {
	mu   sync.Mutex
	docs map[string]docState
}

// newDocumentStore returns an empty [DocumentStore].
func newDocumentStore() *DocumentStore {
	return &DocumentStore{docs: make(map[string]docState)}
}

// Open records that the document at uri was opened with the given version and
// text. Any prior state for the URI is replaced. version is uint32 to match
// lsp.PublishDiagnosticsParams.Version.
func (s *DocumentStore) Open(uri string, version uint32, text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.docs[uri] = docState{version: version, text: text}
}

// Change updates the stored text and version for an open document. If the URI
// is not currently open it is inserted (defensive; editors should always send
// didOpen before didChange). version is uint32 to match
// lsp.PublishDiagnosticsParams.Version.
func (s *DocumentStore) Change(uri string, version uint32, text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.docs[uri] = docState{version: version, text: text}
}

// Close removes the document at uri from the store. If the URI is not open,
// Close is a no-op.
func (s *DocumentStore) Close(uri string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.docs, uri)
}

// Get returns the current text and version for uri, plus a bool indicating
// whether the document is currently open. version is uint32 to match
// lsp.PublishDiagnosticsParams.Version.
func (s *DocumentStore) Get(uri string) (text string, version uint32, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.docs[uri]
	if !ok {
		return "", 0, false
	}
	return d.text, d.version, true
}

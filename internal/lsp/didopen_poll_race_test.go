package lsp

// didopen_poll_race_test.go — regression test for the status-bar poll/didOpen
// cache-poisoning race.
//
// The IntelliJ status-bar widget polls schemalock/getDocumentState on editor
// selection. That poll can reach the server BEFORE textDocument/didOpen. When
// it does, the handler used to resolve the not-yet-open document with empty
// text → StateUnindexable, and the Router cached that result keyed by URI.
// Because DidOpen did not invalidate the cache before resolving, it hit the
// poisoned entry, never enqueued validation, and (StateUnindexable) suppressed
// the documentStateChanged notification — leaving the widget permanently blank.
// VS Code never polls before didOpen, so only the JetBrains client tripped it.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	lspprot "go.lsp.dev/protocol"

	"github.com/schemalock/app/internal/lsp/protocol"
)

// TestGetDocumentStatePollBeforeDidOpen_StillNotifies reproduces the race: a
// getDocumentState poll arrives before didOpen. After the fix, DidOpen must
// still resolve the document via the CDN and emit a StateUnpinned
// documentStateChanged notification (proving the earlier poll did not poison
// the router cache).
func TestGetDocumentStatePollBeforeDidOpen_StillNotifies(t *testing.T) {
	schemaBody := `{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","required":["apiVersion","kind"]}`

	cdnSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/kubernetes/operator.victoriametrics.com/versions.json":
			_, _ = w.Write([]byte(`["0.70.0"]`))
		case "/kubernetes/operator.victoriametrics.com/0.70.0/manifest.json":
			body := `{"version":1,"kinds":{"VMAuth":{"integrity":"` + integrityOf(schemaBody) + `","size":` + sizeStr(schemaBody) + `}}}`
			_, _ = w.Write([]byte(body))
		case "/kubernetes/operator.victoriametrics.com/0.70.0/VMAuth.json":
			_, _ = w.Write([]byte(schemaBody))
		default:
			http.NotFound(w, r)
		}
	}))
	defer cdnSrv.Close()

	notified := make(chan protocol.DocumentStateChangedParams, 8)

	s := newTestServerWithCDN(t, cdnSrv.URL, true)
	s.notifyClient = func(_ context.Context, m string, p any) error {
		if m == protocol.MethodDocumentStateChanged {
			notified <- p.(protocol.DocumentStateChangedParams)
		}
		return nil
	}

	uri := "file:///vmauth.yaml"
	text := "apiVersion: operator.victoriametrics.com/v1beta1\nkind: VMAuth\n"
	ctx := context.Background()

	// --- 1. Poll getDocumentState BEFORE didOpen (the race the widget triggers).
	// The document is not in the store yet, so the handler sees empty text.
	pre, err := s.handleGetDocumentState(ctx, protocol.GetDocumentStateParams{URI: uri})
	if err != nil {
		t.Fatalf("pre-open getDocumentState: %v", err)
	}
	if pre.State != protocol.StateUnindexable {
		t.Fatalf("pre-open getDocumentState: State=%v, want StateUnindexable (doc not open yet)", pre.State)
	}

	// --- 2. didOpen arrives with the real text. It MUST resolve via the CDN and
	// emit a StateUnpinned notification — the pre-open poll must not have
	// poisoned the router cache.
	if err := s.DidOpen(ctx, &lspprot.DidOpenTextDocumentParams{
		TextDocument: lspprot.TextDocumentItem{
			URI:        lspprot.DocumentURI(uri),
			LanguageID: "yaml",
			Version:    1,
			Text:       text,
		},
	}); err != nil {
		t.Fatalf("DidOpen: %v", err)
	}

	got := waitNotifyE2E(t, notified, 5*time.Second)
	if got.State != protocol.StateUnpinned || got.Version != "0.70.0" {
		t.Fatalf("post-open notification: got State=%v Version=%q, want StateUnpinned 0.70.0", got.State, got.Version)
	}

	// --- 3. A subsequent poll must now report the resolved state.
	post, err := s.handleGetDocumentState(ctx, protocol.GetDocumentStateParams{URI: uri})
	if err != nil {
		t.Fatalf("post-open getDocumentState: %v", err)
	}
	if post.State != protocol.StateUnpinned || post.Version != "0.70.0" {
		t.Fatalf("post-open getDocumentState: State=%v Version=%q, want StateUnpinned 0.70.0", post.State, post.Version)
	}
}

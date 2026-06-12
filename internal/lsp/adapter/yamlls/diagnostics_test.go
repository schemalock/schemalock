package yamlls_test

import (
	"context"
	"io"
	"log"
	"sync"
	"testing"
	"time"

	"github.com/schemalock/schemalock/internal/lsp/adapter/yamlls"
	"github.com/schemalock/schemalock/internal/lsp/adapter/yamlls/mock"
	lsp "go.lsp.dev/protocol"
)

// mockEditorClient is a minimal lsp.Client stub that records PublishDiagnostics
// calls so tests can assert whether the editor was (or was not) notified.
type mockEditorClient struct {
	mu    sync.Mutex
	calls []*lsp.PublishDiagnosticsParams
}

func (m *mockEditorClient) PublishDiagnostics(_ context.Context, p *lsp.PublishDiagnosticsParams) error {
	m.mu.Lock()
	m.calls = append(m.calls, p)
	m.mu.Unlock()
	return nil
}

func (m *mockEditorClient) publishCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

// Remaining lsp.Client methods are no-ops.
func (m *mockEditorClient) Progress(_ context.Context, _ *lsp.ProgressParams) error {
	return nil
}
func (m *mockEditorClient) WorkDoneProgressCreate(_ context.Context, _ *lsp.WorkDoneProgressCreateParams) error {
	return nil
}
func (m *mockEditorClient) LogMessage(_ context.Context, _ *lsp.LogMessageParams) error {
	return nil
}
func (m *mockEditorClient) ShowMessage(_ context.Context, _ *lsp.ShowMessageParams) error {
	return nil
}
func (m *mockEditorClient) ShowMessageRequest(_ context.Context, _ *lsp.ShowMessageRequestParams) (*lsp.MessageActionItem, error) {
	return nil, nil
}
func (m *mockEditorClient) Telemetry(_ context.Context, _ any) error { return nil }
func (m *mockEditorClient) RegisterCapability(_ context.Context, _ *lsp.RegistrationParams) error {
	return nil
}
func (m *mockEditorClient) UnregisterCapability(_ context.Context, _ *lsp.UnregistrationParams) error {
	return nil
}
func (m *mockEditorClient) ApplyEdit(_ context.Context, _ *lsp.ApplyWorkspaceEditParams) (bool, error) {
	return false, nil
}
func (m *mockEditorClient) Configuration(_ context.Context, _ *lsp.ConfigurationParams) ([]any, error) {
	return nil, nil
}
func (m *mockEditorClient) WorkspaceFolders(_ context.Context) ([]lsp.WorkspaceFolder, error) {
	return nil, nil
}

var _ lsp.Client = (*mockEditorClient)(nil)

// buildConnectorWithEditorClient wires a Connector over an in-process mock,
// calls CallInitialize, installs the given editorClient, and returns the
// connector ready for use.
func buildConnectorWithEditorClient(t *testing.T, editorClient lsp.Client) *yamlls.Connector {
	t.Helper()

	m := mock.New() // version 1.15.0 — passes probe

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)

	c := yamlls.NewConnectorFromRWC(ctx, m.Connect(ctx), yamlls.Config{
		Logger:       log.New(io.Discard, "", 0),
		EditorClient: editorClient,
	})
	if err := c.CallInitialize(ctx, &lsp.InitializeParams{RootURI: "file:///ws"}); err != nil {
		t.Fatalf("CallInitialize: %v", err)
	}
	t.Cleanup(func() { _ = c.Shutdown(context.Background()) })
	return c
}

// makeDiag returns a trivial diagnostic for use in tests.
func makeDiag(msg string) lsp.Diagnostic {
	return lsp.Diagnostic{
		Message: msg,
		Range: lsp.Range{
			Start: lsp.Position{Line: 0, Character: 0},
			End:   lsp.Position{Line: 0, Character: 1},
		},
	}
}

// TestPublishDiagnostics_OwnedURIDropped asserts that PublishDiagnostics
// does NOT forward diagnostics to the editor when the ownership checker
// returns true for the URI.
func TestPublishDiagnostics_OwnedURIDropped(t *testing.T) {
	t.Parallel()

	editor := &mockEditorClient{}
	c := buildConnectorWithEditorClient(t, editor)

	const ownedURI = "file:///owned.yaml"

	// Install an ownership checker that claims ownedURI as owned.
	c.SetOwnershipChecker(func(docURI string) bool {
		return docURI == ownedURI
	})

	// Simulate yaml-ls pushing diagnostics for the owned URI.
	err := c.PublishDiagnostics(context.Background(), &lsp.PublishDiagnosticsParams{
		URI:         lsp.DocumentURI(ownedURI),
		Diagnostics: []lsp.Diagnostic{makeDiag("some yaml-ls error")},
	})
	if err != nil {
		t.Fatalf("PublishDiagnostics returned error: %v", err)
	}

	// The editor must NOT have been called — diagnostics are dropped for owned URIs.
	if n := editor.publishCount(); n != 0 {
		t.Errorf("editor.PublishDiagnostics was called %d time(s); want 0 (owned URI must be dropped)", n)
	}
}

// TestPublishDiagnostics_UnownedURIForwarded asserts that PublishDiagnostics
// DOES forward diagnostics to the editor when the ownership checker returns
// false for the URI.
func TestPublishDiagnostics_UnownedURIForwarded(t *testing.T) {
	t.Parallel()

	editor := &mockEditorClient{}
	c := buildConnectorWithEditorClient(t, editor)

	const unownedURI = "file:///unowned.yaml"

	// Install an ownership checker that never claims ownership.
	c.SetOwnershipChecker(func(docURI string) bool {
		return false
	})

	err := c.PublishDiagnostics(context.Background(), &lsp.PublishDiagnosticsParams{
		URI:         lsp.DocumentURI(unownedURI),
		Diagnostics: []lsp.Diagnostic{makeDiag("some yaml error")},
	})
	if err != nil {
		t.Fatalf("PublishDiagnostics returned error: %v", err)
	}

	// The editor must have been called exactly once for the unowned URI.
	if n := editor.publishCount(); n != 1 {
		t.Errorf("editor.PublishDiagnostics was called %d time(s); want 1 (unowned URI must be forwarded)", n)
	}
}

// TestPublishDiagnostics_OwnedUnownedMixed asserts that for a connector with an
// ownership checker, diagnostics for an owned URI are dropped while diagnostics
// for an unowned URI are forwarded — both in the same test to confirm isolation.
func TestPublishDiagnostics_OwnedUnownedMixed(t *testing.T) {
	t.Parallel()

	editor := &mockEditorClient{}
	c := buildConnectorWithEditorClient(t, editor)

	const ownedURI = "file:///owned.yaml"
	const unownedURI = "file:///unowned.yaml"

	c.SetOwnershipChecker(func(docURI string) bool {
		return docURI == ownedURI
	})

	// Owned: should be dropped.
	if err := c.PublishDiagnostics(context.Background(), &lsp.PublishDiagnosticsParams{
		URI:         lsp.DocumentURI(ownedURI),
		Diagnostics: []lsp.Diagnostic{makeDiag("owned error")},
	}); err != nil {
		t.Fatalf("PublishDiagnostics(owned): %v", err)
	}

	// Unowned: should be forwarded.
	if err := c.PublishDiagnostics(context.Background(), &lsp.PublishDiagnosticsParams{
		URI:         lsp.DocumentURI(unownedURI),
		Diagnostics: []lsp.Diagnostic{makeDiag("unowned error")},
	}); err != nil {
		t.Fatalf("PublishDiagnostics(unowned): %v", err)
	}

	if n := editor.publishCount(); n != 1 {
		t.Errorf("editor.PublishDiagnostics was called %d time(s); want 1 (only the unowned URI)", n)
	}
}

// TestPublishDiagnostics_NoCheckerPassesThrough asserts that when no ownership
// checker is installed (nil), all diagnostics are forwarded to the editor.
func TestPublishDiagnostics_NoCheckerPassesThrough(t *testing.T) {
	t.Parallel()

	editor := &mockEditorClient{}
	c := buildConnectorWithEditorClient(t, editor)
	// No SetOwnershipChecker call — checker remains nil.

	err := c.PublishDiagnostics(context.Background(), &lsp.PublishDiagnosticsParams{
		URI:         "file:///anything.yaml",
		Diagnostics: []lsp.Diagnostic{makeDiag("some error")},
	})
	if err != nil {
		t.Fatalf("PublishDiagnostics returned error: %v", err)
	}

	if n := editor.publishCount(); n != 1 {
		t.Errorf("editor.PublishDiagnostics was called %d time(s); want 1 (no checker → pass through)", n)
	}
}

// TestPublishDiagnostics_CheckerRaceCondition verifies that concurrent
// SetOwnershipChecker and PublishDiagnostics calls do not race.
// Run with -race to detect issues.
func TestPublishDiagnostics_CheckerRaceCondition(t *testing.T) {
	t.Parallel()

	editor := &mockEditorClient{}
	c := buildConnectorWithEditorClient(t, editor)

	const workers = 8
	var wg sync.WaitGroup
	wg.Add(workers * 2)

	// Half the goroutines call SetOwnershipChecker; the other half call PublishDiagnostics.
	for range workers {
		go func() {
			defer wg.Done()
			c.SetOwnershipChecker(func(string) bool { return true })
		}()
		go func() {
			defer wg.Done()
			_ = c.PublishDiagnostics(context.Background(), &lsp.PublishDiagnosticsParams{
				URI:         "file:///any.yaml",
				Diagnostics: []lsp.Diagnostic{makeDiag("msg")},
			})
		}()
	}
	wg.Wait()
}

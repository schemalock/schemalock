package yamlls

import (
	"context"
	"strings"

	lsp "go.lsp.dev/protocol"
)

// PublishDiagnostics is the lsp.Client callback invoked by the protocol
// ClientHandler when yaml-ls pushes a textDocument/publishDiagnostics
// notification to us. We are yaml-ls's client; yaml-ls is our server.
//
// Routing logic:
//
//   - If the URI belongs to an owned file → drop entirely. The schemalock
//     worker publishes its own diagnostics for owned files. Forwarding
//     yaml-ls's diagnostics here would double-publish in the editor's Problems
//     panel (architecture invariant #4).
//   - If the URI belongs to an unowned file → rewrite Source to "yaml-ls",
//     apply messageFilters, and re-publish to the editor via EditorClient.
//
// The ownership checker is wired by server_typed.go via SetOwnershipChecker
// after initialization. When no checker is installed all diagnostics pass
// through (graceful degradation).
func (c *Connector) PublishDiagnostics(ctx context.Context, params *lsp.PublishDiagnosticsParams) error {
	if c.cfg.EditorClient == nil {
		return nil
	}

	// Drop diagnostics for owned URIs: schemalock publishes its own.
	c.mu.RLock()
	checker := c.ownershipChecker
	c.mu.RUnlock()
	if checker != nil && checker(string(params.URI)) {
		return nil
	}

	filtered := filterDiagnostics(params.Diagnostics)
	if len(filtered) == 0 && len(params.Diagnostics) == 0 {
		// Pass through the empty-clear notification unchanged.
		return c.cfg.EditorClient.PublishDiagnostics(ctx, params)
	}

	// Rewrite Source so the editor's Problems panel attributes them correctly.
	for i := range filtered {
		filtered[i].Source = "yaml-ls"
	}

	return c.cfg.EditorClient.PublishDiagnostics(ctx, &lsp.PublishDiagnosticsParams{
		URI:         params.URI,
		Version:     params.Version,
		Diagnostics: filtered,
	})
}

// filterDiagnostics removes diagnostics whose message matches any entry in
// messageFilters (substring match, case-sensitive). The initial set is empty;
// add entries by referencing a tracked yaml-ls upstream issue.
func filterDiagnostics(diags []lsp.Diagnostic) []lsp.Diagnostic {
	if len(messageFilters) == 0 {
		out := make([]lsp.Diagnostic, len(diags))
		copy(out, diags)
		return out
	}
	out := make([]lsp.Diagnostic, 0, len(diags))
	for _, d := range diags {
		if !matchesAnyFilter(d.Message) {
			out = append(out, d)
		}
	}
	return out
}

func matchesAnyFilter(msg string) bool {
	for _, f := range messageFilters {
		if strings.Contains(msg, f) {
			return true
		}
	}
	return false
}

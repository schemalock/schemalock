package yamlls

import (
	"context"

	lsp "go.lsp.dev/protocol"
)

// Connector implements lsp.Client so it can be passed to
// protocol.ClientHandler as the client for yaml-ls→schemalock callbacks.
// The methods below are the full lsp.Client interface surface.

// compile-time assertion.
var _ lsp.Client = (*Connector)(nil)

// Progress implements lsp.Client. No-op: yaml-ls progress notifications are
// not forwarded to the editor in v1.
func (c *Connector) Progress(_ context.Context, _ *lsp.ProgressParams) error {
	return nil
}

// WorkDoneProgressCreate implements lsp.Client. No-op: yaml-ls work-done
// progress is not forwarded to the editor in v1.
func (c *Connector) WorkDoneProgressCreate(_ context.Context, _ *lsp.WorkDoneProgressCreateParams) error {
	return nil
}

// LogMessage implements lsp.Client. Forwards to the adapter's logger at the
// same severity; does not forward to the editor.
func (c *Connector) LogMessage(_ context.Context, params *lsp.LogMessageParams) error {
	c.logger.Printf("[yamlls log] %s", params.Message)
	return nil
}

// ShowMessage implements lsp.Client. Forwards to the editor when EditorClient
// is set; otherwise logs and drops.
func (c *Connector) ShowMessage(ctx context.Context, params *lsp.ShowMessageParams) error {
	if c.cfg.EditorClient != nil {
		return c.cfg.EditorClient.ShowMessage(ctx, params)
	}
	c.logger.Printf("[yamlls show] %s", params.Message)
	return nil
}

// ShowMessageRequest implements lsp.Client. No-op: yaml-ls rarely sends
// message-request; we return nil (dismiss) in v1.
func (c *Connector) ShowMessageRequest(_ context.Context, _ *lsp.ShowMessageRequestParams) (*lsp.MessageActionItem, error) {
	return nil, nil
}

// Telemetry implements lsp.Client. Dropped: no telemetry forwarded.
func (c *Connector) Telemetry(_ context.Context, _ any) error {
	return nil
}

// RegisterCapability implements lsp.Client. No-op in v1: yaml-ls capability
// registrations are not propagated to the editor.
func (c *Connector) RegisterCapability(_ context.Context, _ *lsp.RegistrationParams) error {
	c.logger.Printf("[yamlls] RegisterCapability: not supported in v1, ignoring")
	return nil
}

// UnregisterCapability implements lsp.Client. No-op in v1.
func (c *Connector) UnregisterCapability(_ context.Context, _ *lsp.UnregistrationParams) error {
	return nil
}

// ApplyEdit implements lsp.Client. No-op: yaml-ls workspace edits are not
// forwarded to the editor in v1.
func (c *Connector) ApplyEdit(_ context.Context, _ *lsp.ApplyWorkspaceEditParams) (bool, error) {
	return false, nil
}

// Configuration implements lsp.Client. Returns an empty list: schemalock does
// not honour workspace/configuration callbacks from yaml-ls in v1. yaml-ls
// may miss some user configuration as a result, but that is acceptable because
// schemalock controls the schema association via custom/schema/request instead.
func (c *Connector) Configuration(_ context.Context, _ *lsp.ConfigurationParams) ([]any, error) {
	c.logger.Printf("[yamlls] workspace/configuration: not supported in v1, returning empty")
	return []any{}, nil
}

// WorkspaceFolders implements lsp.Client. Returns the workspace folders that
// were provided during initialization.
func (c *Connector) WorkspaceFolders(_ context.Context) ([]lsp.WorkspaceFolder, error) {
	return c.cfg.WorkspaceFolders, nil
}

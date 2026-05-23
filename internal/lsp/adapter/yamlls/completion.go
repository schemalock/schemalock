package yamlls

import (
	"context"

	lsp "go.lsp.dev/protocol"
)

// CallCompletion proxies a textDocument/completion request to yaml-ls and
// returns its response. Returns (nil, nil) when the connector is inactive
// so callers can distinguish absent yaml-ls from an empty completion list.
func (c *Connector) CallCompletion(ctx context.Context, params *lsp.CompletionParams) (*lsp.CompletionList, error) {
	server, _ := c.active()
	if server == nil {
		return nil, nil
	}
	return server.Completion(ctx, params)
}

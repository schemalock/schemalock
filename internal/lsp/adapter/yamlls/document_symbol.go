package yamlls

import (
	"context"

	lsp "go.lsp.dev/protocol"
)

// CallDocumentSymbol proxies a textDocument/documentSymbol request to yaml-ls.
// Returns (nil, nil) when the connector is inactive.
func (c *Connector) CallDocumentSymbol(ctx context.Context, params *lsp.DocumentSymbolParams) ([]any, error) {
	server, _ := c.active()
	if server == nil {
		return nil, nil
	}
	return server.DocumentSymbol(ctx, params)
}

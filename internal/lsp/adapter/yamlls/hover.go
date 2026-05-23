package yamlls

import (
	"context"

	lsp "go.lsp.dev/protocol"
)

// CallHover proxies a textDocument/hover request to yaml-ls and returns its
// response. Returns (nil, nil) when the connector is inactive (empty-connector
// pattern) so callers can treat absent yaml-ls identically to a nil result.
func (c *Connector) CallHover(ctx context.Context, params *lsp.HoverParams) (*lsp.Hover, error) {
	server, _ := c.active()
	if server == nil {
		return nil, nil
	}
	return server.Hover(ctx, params)
}

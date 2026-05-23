package yamlls

import (
	"context"

	lsp "go.lsp.dev/protocol"
)

// CallDefinition proxies a textDocument/definition request to yaml-ls.
// Returns (nil, nil) when the connector is inactive.
func (c *Connector) CallDefinition(ctx context.Context, params *lsp.DefinitionParams) ([]lsp.Location, error) {
	server, _ := c.active()
	if server == nil {
		return nil, nil
	}
	return server.Definition(ctx, params)
}

// CallReferences proxies a textDocument/references request to yaml-ls.
// Returns (nil, nil) when the connector is inactive.
func (c *Connector) CallReferences(ctx context.Context, params *lsp.ReferenceParams) ([]lsp.Location, error) {
	server, _ := c.active()
	if server == nil {
		return nil, nil
	}
	return server.References(ctx, params)
}

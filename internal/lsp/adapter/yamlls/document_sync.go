package yamlls

import (
	"context"

	lsp "go.lsp.dev/protocol"
)

// DocumentDidOpen forwards a textDocument/didOpen notification to yaml-ls.
// Fan-out: called for every open document regardless of owned/unowned routing.
// Returns immediately (no-op) when the connector is inactive.
func (c *Connector) DocumentDidOpen(params *lsp.DidOpenTextDocumentParams) {
	server, _ := c.active()
	if server == nil {
		return
	}
	if err := server.DidOpen(context.Background(), params); err != nil {
		if params != nil {
			c.logger.Printf("[yamlls] didOpen %s: %v", params.TextDocument.URI, err)
		}
	}
}

// DocumentDidChange forwards a textDocument/didChange notification to yaml-ls.
// Fan-out: called for every change regardless of owned/unowned routing.
// Returns immediately (no-op) when the connector is inactive.
func (c *Connector) DocumentDidChange(params *lsp.DidChangeTextDocumentParams) {
	server, _ := c.active()
	if server == nil {
		return
	}
	if err := server.DidChange(context.Background(), params); err != nil {
		if params != nil {
			c.logger.Printf("[yamlls] didChange %s: %v", params.TextDocument.URI, err)
		}
	}
}

// DocumentDidClose forwards a textDocument/didClose notification to yaml-ls.
// Fan-out: called for every close regardless of owned/unowned routing.
// Returns immediately (no-op) when the connector is inactive.
func (c *Connector) DocumentDidClose(params *lsp.DidCloseTextDocumentParams) {
	server, _ := c.active()
	if server == nil {
		return
	}
	if err := server.DidClose(context.Background(), params); err != nil {
		if params != nil {
			c.logger.Printf("[yamlls] didClose %s: %v", params.TextDocument.URI, err)
		}
	}
}

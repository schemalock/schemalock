package yamlls

import (
	"context"
	"encoding/json"

	"go.lsp.dev/jsonrpc2"
	"go.lsp.dev/uri"
)

// SchemaProvider is a function that, given a document URI, returns the file://
// URI of the cached JSON Schema for that document, or "" if the document is
// unowned. The function must be fast (it runs on the jsonrpc2 read goroutine).
//
// Chunk 3 wires this to the SchemaResolver + cache lookup. In Chunk 1 the
// connector is constructed without a provider (nil), which causes
// handleCustomSchemaRequest to return empty URIs for all documents.
type SchemaProvider func(ctx context.Context, docURI string) string

// SetSchemaProvider installs the schema provider on the connector. This is
// called by the server after the resolver is constructed (Chunk 3). The
// provider is stored on the connector and consulted by handleCustomSchemaRequest.
// Safe to call concurrently with other operations.
func (c *Connector) SetSchemaProvider(p SchemaProvider) {
	c.mu.Lock()
	c.schemaProvider = p
	c.mu.Unlock()
}

// SetOwnershipChecker installs the ownership checker on the connector. The
// function is called by PublishDiagnostics to determine whether a URI belongs
// to a schemalock-owned document. When it returns true the diagnostics are
// dropped: schemalock publishes its own diagnostics for owned documents via the
// worker, so forwarding yaml-ls's diagnostics would result in double-publishing
// in the editor's Problems panel (architecture invariant #4).
//
// The checker must be fast — it is invoked on the yaml-ls jsonrpc2 goroutine,
// which is the same goroutine that dispatches all incoming yaml-ls notifications.
// The implementation in server_typed.go uses router.IsOwned which is O(1) on a
// cache hit (the common case for any open document).
//
// Safe to call concurrently with other operations.
func (c *Connector) SetOwnershipChecker(fn func(docURI string) bool) {
	c.mu.Lock()
	c.ownershipChecker = fn
	c.mu.Unlock()
}

// handleCustomSchemaRequest handles the yaml-ls→schemalock custom/schema/request
// callback. yaml-ls sends a list of document URIs and expects back a list of
// schema URIs (one per document, empty string if no schema is available).
//
// This handler is invoked on the jsonrpc2 read goroutine inside buildCustomHandler.
func (c *Connector) handleCustomSchemaRequest(ctx context.Context, reply jsonrpc2.Replier, req jsonrpc2.Request) error {
	var requestedURIs []string
	if err := json.Unmarshal(req.Params(), &requestedURIs); err != nil {
		return reply(ctx, nil, err)
	}

	if len(requestedURIs) == 0 {
		return reply(ctx, []uri.URI{}, nil)
	}

	// Capture schema provider under read lock.
	c.mu.RLock()
	provider := c.schemaProvider
	c.mu.RUnlock()

	results := make([]uri.URI, len(requestedURIs))
	for i, docURI := range requestedURIs {
		if provider != nil {
			schemaURI := provider(ctx, docURI)
			results[i] = uri.URI(schemaURI)
		}
		// else: leave results[i] as "" (yaml-ls treats empty as "no schema")
	}

	c.logger.Printf("[yamlls] custom/schema/request for %v → %v", requestedURIs, results)
	return reply(ctx, results, nil)
}

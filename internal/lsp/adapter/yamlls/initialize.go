package yamlls

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"go.lsp.dev/jsonrpc2"
	lsp "go.lsp.dev/protocol"
)

// minMajor and minMinor are the minimum yaml-language-server version that
// schemalock supports. The floor is v1.14.0 — the version we have tested
// against. Earlier versions may behave differently (e.g. different custom
// schema protocol), so we hard-reject them rather than silently degrade.
const (
	minMajor = 1
	minMinor = 14
)

// CallInitialize sends the LSP initialize request to yaml-ls, reads the
// result, applies the version probe, and (on success) sends the initialized
// notification plus the custom-schema registration notification.
//
// If the version probe fails (version < v1.14.0 or unparseable), the
// connector is degraded to the empty-connector state (conn and server are set
// to nil) and the method returns nil — the server should continue without
// yaml-ls features.
func (c *Connector) CallInitialize(ctx context.Context, editorParams *lsp.InitializeParams) error {
	server, conn := c.active()
	if server == nil || conn == nil {
		return nil
	}

	params := &lsp.InitializeParams{
		ProcessID: int32(os.Getpid()),
		ClientInfo: &lsp.ClientInfo{
			Name: "schemalock",
		},
		Capabilities: lsp.ClientCapabilities{
			TextDocument: &lsp.TextDocumentClientCapabilities{
				PublishDiagnostics: &lsp.PublishDiagnosticsClientCapabilities{},
				DocumentSymbol: &lsp.DocumentSymbolClientCapabilities{
					HierarchicalDocumentSymbolSupport: true,
				},
				Hover: &lsp.HoverTextDocumentClientCapabilities{
					ContentFormat: []lsp.MarkupKind{lsp.Markdown, lsp.PlainText},
				},
				Completion: &lsp.CompletionTextDocumentClientCapabilities{
					CompletionItem: &lsp.CompletionTextDocumentClientCapabilitiesItem{
						DocumentationFormat: []lsp.MarkupKind{lsp.Markdown, lsp.PlainText},
					},
				},
			},
		},
	}

	// Propagate workspace context from the editor to yaml-ls.
	if editorParams != nil {
		params.RootURI = editorParams.RootURI //nolint:staticcheck
		params.WorkspaceFolders = editorParams.WorkspaceFolders
	} else {
		params.RootURI = c.cfg.RootURI //nolint:staticcheck // SA1019: forwarding caller-supplied legacy field
		params.WorkspaceFolders = c.cfg.WorkspaceFolders
	}

	c.logger.Printf("[yamlls] sending initialize")
	initCtx, initCancel := context.WithTimeout(ctx, 10*time.Second)
	defer initCancel()

	result, err := server.Initialize(initCtx, params)
	if err != nil {
		c.logger.Printf("[yamlls] initialize error: %v; disabling yaml-ls features", err)
		c.degradeToEmpty(server, conn)
		return nil
	}

	// Version probe.
	if result.ServerInfo == nil || result.ServerInfo.Version == "" {
		c.logger.Printf("[yamlls] initialize: no ServerInfo.Version in response; disabling yaml-ls features")
		c.degradeToEmpty(server, conn)
		return nil
	}

	version := result.ServerInfo.Version
	if !versionMeetsFloor(version, minMajor, minMinor) {
		c.logger.Printf("[yamlls] version %s is below minimum v%d.%d.0; disabling yaml-ls features", version, minMajor, minMinor)
		c.degradeToEmpty(server, conn)
		return nil
	}

	c.logger.Printf("[yamlls] initialized ok (version %s)", version)

	// Send initialized notification.
	if err := server.Initialized(ctx, &lsp.InitializedParams{}); err != nil {
		c.logger.Printf("[yamlls] initialized notification: %v", err)
	}

	// Register our custom schema provider with yaml-ls.
	if err := c.postInitialize(ctx, conn); err != nil {
		c.logger.Printf("[yamlls] post-initialize: %v", err)
	}

	return nil
}

// degradeToEmpty clears the conn and server references, putting the connector
// into the empty (no-op) state. Called when the version probe rejects the
// yaml-ls version. server and conn are the live references captured before the
// lock was taken.
func (c *Connector) degradeToEmpty(server lsp.Server, conn jsonrpc2.Conn) {
	// Send shutdown + exit best-effort before dropping the connection.
	shutCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = server.Shutdown(shutCtx)
	_ = conn.Notify(shutCtx, "exit", nil)
	_ = conn.Close()

	// Mark inactive under the write lock.
	c.mu.Lock()
	c.conn = nil
	c.server = nil
	c.mu.Unlock()
}

// versionMeetsFloor returns true when versionStr is >= major.minor.0.
// The version string must be in semver or "major.minor[.patch[+extra]]" form.
// Semver pre-release suffixes (e.g. "-beta") cause the version to be treated
// as not meeting the floor (conservative).
func versionMeetsFloor(versionStr string, major, minor int) bool {
	// Strip any leading "v".
	v := strings.TrimPrefix(versionStr, "v")

	// A pre-release indicator before the first dash means an unofficial build;
	// treat conservatively.
	if dash := strings.IndexByte(v, '-'); dash >= 0 {
		v = v[:dash]
	}

	// Strip build metadata (+...).
	if plus := strings.IndexByte(v, '+'); plus >= 0 {
		v = v[:plus]
	}

	parts := strings.SplitN(v, ".", 3)
	if len(parts) < 2 {
		return false
	}

	gotMajor, err := strconv.Atoi(parts[0])
	if err != nil {
		return false
	}
	gotMinor, err := strconv.Atoi(parts[1])
	if err != nil {
		return false
	}

	if gotMajor != major {
		return gotMajor > major
	}
	return gotMinor >= minor
}

// postInitialize sends the yaml/registerCustomSchemaRequest notification that
// tells yaml-ls to use our custom/schema/request handler for schema URIs.
func (c *Connector) postInitialize(ctx context.Context, conn jsonrpc2.Conn) error {
	if err := conn.Notify(ctx, "yaml/registerCustomSchemaRequest", nil); err != nil {
		return fmt.Errorf("yaml/registerCustomSchemaRequest: %w", err)
	}
	return nil
}

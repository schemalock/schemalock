// Package yamlls implements a proxy adapter between the SchemaLock LSP server
// and a yaml-language-server subprocess. The adapter spawns yaml-ls as a child
// process, wires it over jsonrpc2, and exposes typed Call* methods that the
// server uses for fan-out document sync and proxy requests (hover, completion,
// definition, references, documentSymbol).
//
// When yaml-ls is absent or fails the version probe, [NewConnector] returns an
// empty *Connector whose every Call* method is a silent no-op. This lets the
// server treat both cases identically without mode switches.
package yamlls

import (
	"context"
	"fmt"
	"io"
	"log"
	"os/exec"
	"sync"
	"time"

	"go.lsp.dev/jsonrpc2"
	lsp "go.lsp.dev/protocol"
	"go.uber.org/zap"
)

// messageFilters is the list of yaml-language-server diagnostic message
// substrings that should be suppressed before forwarding to the editor.
// Initially empty; populate by referencing a tracked yaml-ls upstream issue.
// See helm-ls/internal/adapter/yamlls/diagnostics.go for prior art.
var messageFilters = []string{ /* none yet */ }

// Config holds construction parameters for a [Connector].
type Config struct {
	// Path is the resolved absolute path to the yaml-language-server binary.
	// If empty, NewConnector returns an empty (no-op) Connector immediately.
	Path string

	// Logger receives diagnostic and lifecycle messages from the adapter.
	// Must not write to stdout.
	Logger *log.Logger

	// EditorClient is the typed LSP client for the editor connection (VS Code ↔
	// schemalock). The connector forwards ShowMessage / PublishDiagnostics calls
	// toward the editor through this interface.
	// May be nil during tests that don't exercise those paths.
	EditorClient lsp.Client

	// WorkspaceFolders is the set of workspace folders received from the editor
	// during Initialize. Forwarded verbatim to yaml-ls.
	WorkspaceFolders []lsp.WorkspaceFolder

	// RootURI is the root URI for the workspace. Used when WorkspaceFolders is
	// empty (legacy clients).
	RootURI lsp.DocumentURI
}

// Connector is a proxy adapter that manages the yaml-language-server subprocess.
// A zero-value / empty Connector (where conn == nil) is valid and all Call*
// methods are silent no-ops. Construct via [NewConnector].
type Connector struct {
	cfg              Config
	cmd              *exec.Cmd // subprocess handle; nil when inactive or no subprocess
	drain            io.Reader // subprocess stdout pipe; drained after conn.Close to prevent pipe-full deadlock
	logger           *log.Logger
	schemaProvider   SchemaProvider    // set via SetSchemaProvider; may be nil
	ownershipChecker func(string) bool // set via SetOwnershipChecker; may be nil

	// mu guards server, conn, schemaProvider, and ownershipChecker. All reads
	// and writes to these fields must hold mu. This is necessary because
	// CallInitialize (on the caller goroutine) can set them to nil via
	// degradeToEmpty while background goroutines watch conn.Done() or Call*
	// methods run concurrently.
	mu     sync.RWMutex
	server lsp.Server    // typed dispatch surface to call yaml-ls; nil when inactive
	conn   jsonrpc2.Conn // jsonrpc2 connection to the subprocess; nil when inactive
}

// NewConnector starts the yaml-language-server subprocess and returns a Connector
// ready for [Connector.CallInitialize]. If cfg.Path is empty, or if the
// subprocess cannot be started, it returns an empty *Connector whose every
// method is a silent no-op. Callers must call [Connector.Shutdown] when done.
func NewConnector(ctx context.Context, cfg Config) *Connector {
	lg := cfg.Logger
	if lg == nil {
		lg = log.New(io.Discard, "", 0)
	}

	if cfg.Path == "" {
		lg.Printf("[yamlls] no path configured; yaml-ls features disabled")
		return &Connector{logger: lg, cfg: cfg}
	}

	// Spawn yaml-language-server --stdio.
	cmd := exec.CommandContext(ctx, cfg.Path, "--stdio") //nolint:gosec
	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		lg.Printf("[yamlls] stdin pipe: %v; yaml-ls features disabled", err)
		return &Connector{logger: lg, cfg: cfg}
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		lg.Printf("[yamlls] stdout pipe: %v; yaml-ls features disabled", err)
		return &Connector{logger: lg, cfg: cfg}
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		lg.Printf("[yamlls] stderr pipe: %v; yaml-ls features disabled", err)
		return &Connector{logger: lg, cfg: cfg}
	}

	if err := cmd.Start(); err != nil {
		lg.Printf("[yamlls] start: %v; yaml-ls features disabled", err)
		return &Connector{logger: lg, cfg: cfg}
	}

	lg.Printf("[yamlls] started pid=%d path=%s", cmd.Process.Pid, cfg.Path)

	// Drain stderr at TRACE level so subprocess noise doesn't swamp the log.
	go func() {
		buf := make([]byte, 4096)
		for {
			n, readErr := stderrPipe.Read(buf)
			if n > 0 {
				lg.Printf("[yamlls stderr] %s", buf[:n])
			}
			if readErr != nil {
				return
			}
		}
	}()

	rwc := readWriteCloseSubprocess{stdout: stdoutPipe, stdin: stdinPipe}
	c := newConnectorWired(ctx, rwc, cmd, cfg, lg)
	c.drain = stdoutPipe
	return c
}

// NewConnectorFromRWC builds a Connector over an already-open ReadWriteCloser
// instead of spawning a subprocess. This is intended for tests that use an
// in-process mock yaml-ls (e.g. [mock.MockYamlls.Connect]).
//
// The caller is responsible for closing rwc when done; Shutdown will call
// conn.Close() which causes the stream to error, which is benign.
func NewConnectorFromRWC(ctx context.Context, rwc io.ReadWriteCloser, cfg Config) *Connector {
	lg := cfg.Logger
	if lg == nil {
		lg = log.New(io.Discard, "", 0)
	}
	return newConnectorWired(ctx, rwc, nil, cfg, lg)
}

// newConnectorWired wires the jsonrpc2 connection and starts the read loop.
// It is the shared implementation for NewConnector and NewConnectorFromRWC.
func newConnectorWired(ctx context.Context, rwc io.ReadWriteCloser, cmd *exec.Cmd, cfg Config, lg *log.Logger) *Connector {
	stream := jsonrpc2.NewStream(rwc)
	conn := jsonrpc2.NewConn(stream)

	zapNop := zap.NewNop()
	server := lsp.ServerDispatcher(conn, zapNop.Named("yamlls"))

	c := &Connector{
		cfg:    cfg,
		cmd:    cmd,
		logger: lg,
		server: server,
		conn:   conn,
	}

	// Wire the handler chain: custom yaml-ls extensions (yaml/*, custom/*)
	// intercept first, then protocol.ClientHandler dispatches standard client
	// callbacks (publishDiagnostics, showMessage, etc.) to c as lsp.Client.
	// lsp.CancelHandler (from go.lsp.dev/protocol) wraps jsonrpc2.CancelHandler
	// and returns a single Handler (not the (Handler, canceller) tuple).
	customH := c.buildCustomHandler(lsp.ClientHandler(c, jsonrpc2.MethodNotFoundHandler))
	conn.Go(ctx, lsp.CancelHandler(customH))

	// Watch for unexpected subprocess exit.
	go func() {
		<-conn.Done()
		c.mu.RLock()
		active := c.conn != nil
		c.mu.RUnlock()
		if active {
			lg.Printf("[yamlls] connection closed unexpectedly")
		}
	}()

	return c
}

// active returns the current server and conn under the read lock.
// Returns (nil, nil) when the connector is inactive.
func (c *Connector) active() (lsp.Server, jsonrpc2.Conn) {
	if c == nil {
		return nil, nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.server, c.conn
}

// ShouldRun returns true when the connector is active — that is, it holds a
// live connection to a yaml-language-server subprocess that passed the version
// probe. Returns false for the empty-connector (yaml-ls absent or rejected).
func (c *Connector) ShouldRun() bool {
	if c == nil {
		return false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.conn != nil
}

// Shutdown performs a graceful shutdown sequence:
//  1. Send the LSP shutdown request (up to 2 s).
//  2. Send the exit notification.
//  3. Close the connection.
//  4. Wait for the subprocess to exit (up to 1 s).
//  5. SIGKILL on timeout.
//
// Shutdown is idempotent: calling it on an empty or already-shut-down Connector
// is a no-op.
func (c *Connector) Shutdown(ctx context.Context) error {
	if c == nil {
		return nil
	}

	// Capture references then mark the connector inactive so concurrent Call*
	// methods return immediately after this point.
	c.mu.Lock()
	server := c.server
	conn := c.conn
	c.conn = nil
	c.server = nil
	c.mu.Unlock()

	if server == nil || conn == nil {
		return nil // already shut down
	}

	// 1. Send shutdown (2 s timeout).
	shutCtx, shutCancel := context.WithTimeout(ctx, 2*time.Second)
	defer shutCancel()
	if err := server.Shutdown(shutCtx); err != nil {
		c.logger.Printf("[yamlls] shutdown request: %v", err)
	}

	// 2. Send exit notification (best-effort, ignore error).
	exitCtx, exitCancel := context.WithTimeout(ctx, time.Second)
	defer exitCancel()
	_ = conn.Notify(exitCtx, "exit", nil)

	// 3. Close the connection.
	_ = conn.Close()

	// Drain subprocess stdout so it can exit cleanly. After conn.Close() the
	// jsonrpc2 read loop has stopped; if yaml-ls has buffered output it would
	// stall writing to a full pipe and block cmd.Wait(). The goroutine exits
	// when the pipe reaches EOF after the subprocess exits.
	if c.drain != nil {
		go func() { _, _ = io.Copy(io.Discard, c.drain) }()
	}

	cmd := c.cmd
	if cmd == nil || cmd.Process == nil {
		return nil
	}

	// 4. Wait for subprocess exit (1 s), then SIGKILL.
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			c.logger.Printf("[yamlls] subprocess exit: %v", err)
		}
	case <-time.After(time.Second):
		c.logger.Printf("[yamlls] subprocess did not exit within 1s; killing")
		if killErr := cmd.Process.Kill(); killErr != nil {
			return fmt.Errorf("killing yaml-ls subprocess: %w", killErr)
		}
	}
	return nil
}

// buildCustomHandler constructs the handler for incoming yaml-ls→schemalock
// messages. It intercepts custom yaml-ls extension methods (yaml/*, custom/*)
// and delegates everything else to next (which is the standard ClientHandler).
func (c *Connector) buildCustomHandler(next jsonrpc2.Handler) jsonrpc2.Handler {
	return func(ctx context.Context, reply jsonrpc2.Replier, req jsonrpc2.Request) error {
		switch req.Method() {
		case "custom/schema/request":
			return c.handleCustomSchemaRequest(ctx, reply, req)
		default:
			return next(ctx, reply, req)
		}
	}
}

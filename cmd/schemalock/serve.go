package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"

	"github.com/schemalock/schemalock/internal/cache"
	"github.com/schemalock/schemalock/internal/lsp"
	"github.com/schemalock/schemalock/internal/registry"
)

const defaultBaseURL = "https://cdn.schemalock.dev"

// registryBaseURLEnv lets integration tests point the binary at a fake CDN.
// Empty value (the default) uses defaultBaseURL.
const registryBaseURLEnv = "SCHEMALOCK_REGISTRY_BASE"

// runServe is the entry point for the `serve` subcommand.
// It parses flags from args and launches the LSP server on stdio.
// --stdio must be set for the PoC; other transports are not supported.
func runServe(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintf(stderr, `Usage: schemalock serve --stdio

Run the SchemaLock LSP server, reading JSON-RPC from stdin and writing to
stdout. --stdio is required; other transports are not supported in the PoC.

Flags:
`)
		fs.PrintDefaults()
	}

	useStdio := fs.Bool("stdio", false, "use stdio for LSP transport (required)")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return fmt.Errorf("%w: %s", ErrUsage, err)
	}

	if !*useStdio {
		fmt.Fprintln(stderr, "serve: --stdio is required")
		return fmt.Errorf("%w: serve requires --stdio", ErrUsage)
	}

	// Resolve the cache root (~/.schemalock/cache). The directory is created
	// lazily by the cache package on first write.
	cacheRoot, err := cache.DefaultRoot()
	if err != nil {
		return fmt.Errorf("%w: resolving cache root: %s", ErrIO, err)
	}

	c := cache.New(cacheRoot)
	baseURL := defaultBaseURL
	if env := os.Getenv(registryBaseURLEnv); env != "" {
		baseURL = env
	}
	reg := registry.NewClient(baseURL)
	logger := log.New(stderr, "schemalock-lsp ", log.LstdFlags|log.Lmicroseconds)

	srv := lsp.NewServer(lsp.Config{
		Cache:    c,
		Registry: reg,
		Logger:   logger,
	})

	// Wire stdin+stdout into a ReadWriteCloser for the jsonrpc2-based Run path.
	// Close is a no-op — the jsonrpc2 stream closes the connection via
	// conn.Close() on exit, not by closing the underlying os.Stdin/os.Stdout.
	return srv.Run(ctx, stdioRWC{r: stdin, w: stdout})
}

// stdioRWC wraps an io.Reader and io.Writer into an io.ReadWriteCloser with a
// no-op Close. Used to bridge os.Stdin+os.Stdout into the jsonrpc2-based Run
// path, which expects an io.ReadWriteCloser.
type stdioRWC struct {
	r io.Reader
	w io.Writer
}

func (s stdioRWC) Read(p []byte) (int, error)  { return s.r.Read(p) }
func (s stdioRWC) Write(p []byte) (int, error) { return s.w.Write(p) }
func (s stdioRWC) Close() error                { return nil }

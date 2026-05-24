package main

import (
	"fmt"
	"io"
)

// printVersion writes the version line to w in the format:
//
//	schemalock <version> (commit <commit>, built <buildTime>)
//
// The three fields are injected at build time via -ldflags; see README for
// the exact flag names and the CI snippet that sets them.
func printVersion(w io.Writer) {
	fmt.Fprintf(w, "schemalock %s (commit %s, built %s)\n", version, commit, buildTime)
}

// printRootHelp writes the top-level usage summary to w.
func printRootHelp(w io.Writer) {
	fmt.Fprintf(w, `schemalock — pin and verify Kubernetes CRD schemas

Usage:
  schemalock <subcommand> [flags]

Subcommands:
  verify   Validate every YAML manifest under --path against pinned schemas
  add      Append or replace a name@version in the nearest schemalock.yaml
  fmt      Canonicalize a schemalock.yaml (sort ecosystems and specs)
  serve    Run the LSP server on stdio (requires --stdio)

Flags:
  -h, --help       Show this help
  -v, --version    Print version and build info, then exit

Run 'schemalock <subcommand> --help' for subcommand-specific flags.
`)
}

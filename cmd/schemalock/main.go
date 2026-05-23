// Package main is the entry point for the schemalock binary.
// It dispatches to one of three subcommands: lock, verify, or serve.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
)

// version is the semver string injected at build time via -ldflags.
// Defaults to "dev" for un-stamped local builds.
var version = "dev"

// commit is the git commit SHA at build time, injected via -ldflags.
var commit = "unknown"

// buildTime is the RFC3339 UTC timestamp of the build, injected via -ldflags.
var buildTime = "unknown"

// Sentinel errors for exit-code mapping. These are package-level values only;
// no init() function is used.

// ErrDrift indicates that schemalock.lock has drifted from the live CDN manifest.
var ErrDrift = errors.New("lockfile drift")

// ErrIO indicates an I/O, network, or CDN error.
var ErrIO = errors.New("i/o or network error")

// ErrUsage indicates a usage error (bad flags, unknown subcommand).
var ErrUsage = errors.New("usage error")

// ErrNotImplemented indicates a subcommand not yet implemented.
var ErrNotImplemented = errors.New("not implemented")

// exitCodeFor maps sentinel errors to exit codes per the go-cli-guide.
// 0 = success, 1 = usage, 2 = drift, 3 = I/O.
func exitCodeFor(err error) int {
	if err == nil {
		return 0
	}
	switch {
	case errors.Is(err, ErrDrift):
		return 2
	case errors.Is(err, ErrIO):
		return 3
	case errors.Is(err, ErrUsage):
		return 1
	case errors.Is(err, ErrNotImplemented):
		return 1
	default:
		return 2 // general failure
	}
}

func main() {
	if len(os.Args) < 2 {
		printRootHelp(os.Stdout)
		os.Exit(1)
	}

	sub, rest := os.Args[1], os.Args[2:]
	ctx := context.Background()

	var err error
	switch sub {
	case "lock":
		err = runLock(ctx, rest, os.Stdout, os.Stderr)
	case "verify":
		err = runVerify(ctx, rest, os.Stdout, os.Stderr)
	case "serve":
		err = runServe(ctx, rest, os.Stdin, os.Stdout, os.Stderr)
	case "-h", "--help", "help":
		printRootHelp(os.Stdout)
		return
	case "-v", "--version", "version":
		printVersion(os.Stdout)
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n", sub)
		printRootHelp(os.Stderr)
		os.Exit(1)
	}

	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(exitCodeFor(err))
	}
}

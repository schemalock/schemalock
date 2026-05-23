package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/schemalock/app/internal/lockfile"
	"github.com/schemalock/app/internal/registry"
)

// runVerify is the entry point for the `verify` subcommand.
// It parses flags from args, reads both schemalock.yaml and schemalock.lock,
// fetches the live manifest from the CDN, and reports any drift.
func runVerify(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintf(stdout, `Usage: schemalock verify [flags]

Compare schemalock.lock against the live CDN manifest. Exits 0 if clean,
exits 2 if any integrity hash has drifted.

Flags:
`)
		fs.PrintDefaults()
	}

	filePath := fs.String("file", "schemalock.yaml", "path to schemalock.yaml intent file")
	lockfilePath := fs.String("lockfile", "schemalock.lock", "path to lockfile")
	registryURL := fs.String("registry", "https://cdn.schemalock.dev", "base URL of the schema registry")
	_ = fs.Bool("strict", true, "strict mode (reserved; always on for PoC)")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return fmt.Errorf("%w: %s", ErrUsage, err)
	}

	// Read and decode the intent file.
	intentBytes, err := os.ReadFile(*filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%w: intent file not found: %s", ErrUsage, *filePath)
		}
		return fmt.Errorf("%w: reading %s: %v", ErrIO, *filePath, err)
	}
	intent, err := lockfile.DecodeIntent(intentBytes)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUsage, err)
	}

	// Read and decode the lockfile.
	lockBytes, err := os.ReadFile(*lockfilePath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%w: lockfile not found — run `schemalock lock` first: %s", ErrUsage, *lockfilePath)
		}
		return fmt.Errorf("%w: reading %s: %v", ErrIO, *lockfilePath, err)
	}
	lf, err := lockfile.DecodeLock(lockBytes)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUsage, err)
	}

	// Build the set of IDs declared in the intent.
	intentIDs := make(map[string]struct{})
	for eco, specs := range intent.Ecosystems {
		for _, spec := range specs {
			id := eco + "/" + spec
			intentIDs[id] = struct{}{}
		}
	}

	// Build the set of IDs present in the lockfile.
	lockIDs := make(map[string]struct{}, len(lf.Entries))
	for _, entry := range lf.Entries {
		lockIDs[entry.ID] = struct{}{}
	}

	// Collect drift findings (don't fail-fast).
	var drifts []string

	// Every intent ID must be in the lockfile.
	for id := range intentIDs {
		if _, ok := lockIDs[id]; !ok {
			drifts = append(drifts, fmt.Sprintf("drift: %s missing from lockfile (run `schemalock lock`)", id))
		}
	}

	// Every lockfile ID must be in the intent.
	for id := range lockIDs {
		if _, ok := intentIDs[id]; !ok {
			drifts = append(drifts, fmt.Sprintf("drift: %s in lockfile but not in schemalock.yaml", id))
		}
	}

	// If set membership already has drift, report and return immediately
	// (we cannot verify manifests for entries that are completely absent).
	if len(drifts) > 0 {
		for _, d := range drifts {
			fmt.Fprintln(stdout, d)
		}
		return fmt.Errorf("schemalock.lock has drifted from schemalock.yaml: %w", ErrDrift)
	}

	// Fetch live manifests and compare integrity + size for each Kind.
	client := registry.NewClient(*registryURL)

	for _, entry := range lf.Entries {
		eco, group, version, idErr := lockfile.ResolveID(entry.ID)
		if idErr != nil {
			return fmt.Errorf("%w: malformed lock entry ID %q: %v", ErrUsage, entry.ID, idErr)
		}

		manifest, fetchErr := client.FetchManifest(ctx, eco, group, version)
		if fetchErr != nil {
			if errors.Is(fetchErr, registry.ErrNotFound) {
				return fmt.Errorf("%w: manifest not found for %s: %v", ErrIO, entry.ID, fetchErr)
			}
			return fmt.Errorf("%w: fetching manifest for %s: %v", ErrIO, entry.ID, fetchErr)
		}

		// Check every Kind recorded in the lockfile against the live manifest.
		for kind, locked := range entry.Schemas {
			live, ok := manifest.Kinds[kind]
			if !ok {
				drifts = append(drifts,
					fmt.Sprintf("drift: %s/%s@%s %s kind missing from live manifest", eco, group, version, kind))
				continue
			}
			if locked.Integrity != live.Integrity {
				drifts = append(drifts,
					fmt.Sprintf("drift: %s/%s@%s %s expected=%s actual=%s",
						eco, group, version, kind,
						truncateIntegrity(locked.Integrity),
						truncateIntegrity(live.Integrity)))
			}
			if locked.Size != live.Size {
				drifts = append(drifts,
					fmt.Sprintf("drift: %s/%s@%s %s size expected=%d actual=%d",
						eco, group, version, kind, locked.Size, live.Size))
			}
		}

	}

	if len(drifts) > 0 {
		for _, d := range drifts {
			fmt.Fprintln(stdout, d)
		}
		return fmt.Errorf("schemalock.lock has drifted from live manifest: %w", ErrDrift)
	}

	fmt.Fprintf(stdout, "ok: %d entries verified\n", len(lf.Entries))
	return nil
}

// truncateIntegrity returns the first 20 characters of s followed by "…" if
// s is longer than 20 characters, making drift output readable on one line.
func truncateIntegrity(s string) string {
	const limit = 20
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "…"
}

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/schemalock/app/internal/atomicfs"
	"github.com/schemalock/app/internal/lockfile"
	"github.com/schemalock/app/internal/registry"
)

// runLock is the entry point for the `lock` subcommand.
// It parses flags from args, reads schemalock.yaml, fetches manifests from
// the CDN registry, and writes a deterministic schemalock.lock.
func runLock(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("lock", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintf(stdout, `Usage: schemalock lock [flags]

Fetch manifests from the CDN for each ecosystem entry in schemalock.yaml and
write a deterministic schemalock.lock pinning the integrity of every Kind.

Flags:
`)
		fs.PrintDefaults()
	}

	filePath := fs.String("file", "schemalock.yaml", "path to schemalock.yaml intent file")
	lockfilePath := fs.String("lockfile", "schemalock.lock", "path to output lockfile")
	registryURL := fs.String("registry", "https://cdn.schemalock.dev", "base URL of the schema registry")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return fmt.Errorf("%w: %s", ErrUsage, err)
	}

	// Read the intent file.
	intentBytes, err := os.ReadFile(*filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%w: intent file not found: %s", ErrUsage, *filePath)
		}
		return fmt.Errorf("%w: reading %s: %v", ErrIO, *filePath, err)
	}

	// Decode the intent.
	intent, err := lockfile.DecodeIntent(intentBytes)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUsage, err)
	}

	// Build the registry client.
	client := registry.NewClient(*registryURL)

	// Fetch manifests and build lock entries.
	var entries []lockfile.LockEntry
	for eco, specs := range intent.Ecosystems {
		for _, spec := range specs {
			// Each spec is "group@version"; prepend ecosystem to build a full ID.
			id := eco + "/" + spec
			ecoParsed, group, version, idErr := lockfile.ResolveID(id)
			if idErr != nil {
				return fmt.Errorf("%w: %v", ErrUsage, idErr)
			}

			manifest, fetchErr := client.FetchManifest(ctx, ecoParsed, group, version)
			if fetchErr != nil {
				if errors.Is(fetchErr, registry.ErrNotFound) {
					return fmt.Errorf("%w: manifest not found for %s: %v", ErrIO, id, fetchErr)
				}
				return fmt.Errorf("%w: fetching manifest for %s: %v", ErrIO, id, fetchErr)
			}

			// Build the schemas map from the manifest.
			schemas := make(map[string]lockfile.SchemaEntry, len(manifest.Kinds))
			for kind, ke := range manifest.Kinds {
				schemas[kind] = lockfile.SchemaEntry{
					Integrity: ke.Integrity,
					Size:      ke.Size,
				}
			}

			entries = append(entries, lockfile.LockEntry{
				ID:      id,
				Base:    ecoParsed + "/" + group + "/" + version + "/",
				Schemas: schemas,
			})
		}
	}

	// Build and encode the lockfile.
	lf := lockfile.LockFile{
		Version:     1,
		Registry:    *registryURL,
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Entries:     entries,
	}

	encoded, err := lockfile.EncodeLock(lf)
	if err != nil {
		return fmt.Errorf("%w: encoding lockfile: %v", ErrIO, err)
	}

	if err := atomicfs.AtomicWrite(*lockfilePath, encoded); err != nil {
		return fmt.Errorf("%w: writing lockfile: %v", ErrIO, err)
	}

	fmt.Fprintf(stdout, "wrote %s (%d entries)\n", *lockfilePath, len(entries))
	return nil
}

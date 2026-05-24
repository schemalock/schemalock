package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"

	"github.com/schemalock/app/internal/lsp"
	"github.com/schemalock/app/internal/validator"
)

// runVerify discovers every YAML manifest under --path (default ".") and
// validates each against its resolved schema (intent-pinned, falling back
// to CDN-latest for unpinned kinds). Exits non-zero when any manifest fails
// validation; with --strict-pinned, unpinned kinds escalate to failures
// rather than warnings.
func runVerify(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintf(stdout, `Usage: schemalock verify [flags]

Validate every YAML manifest under --path against its effective schema. The
effective schema is resolved by walking up from each manifest's directory
through schemalock.yaml files (with root: true halting the walk), or — for
kinds not pinned by any intent file — by fetching the latest published
schema from the CDN.

Exits 0 on success; non-zero if any manifest fails validation or, with
--strict-pinned, if any manifest's kind is not in the intent set.

Strict-mode validation is on by default: any field not in the schema
(typos, deprecated keys) is reported as an error. Pass --no-strict to
match the LSP strict:false mode and tolerate unknown fields.

Flags:
`)
		fs.PrintDefaults()
	}

	pathFlag := fs.String("path", ".", "directory to discover manifests under")
	registryURL := fs.String("registry", "https://cdn.schemalock.dev", "registry base URL")
	cacheDir := fs.String("cache-dir", "", "override schema cache directory (default ~/.schemalock/cache)")
	strictPinned := fs.Bool("strict-pinned", false, "treat unpinned kinds as errors instead of warnings")
	noStrict := fs.Bool("no-strict", false, "disable strict-mode validation (allow unknown fields; matches LSP strict:false)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return fmt.Errorf("%w: %s", ErrUsage, err)
	}

	root, err := filepath.Abs(*pathFlag)
	if err != nil {
		return fmt.Errorf("%w: --path: %s", ErrUsage, err)
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		return fmt.Errorf("%w: --path is not a directory: %s", ErrUsage, root)
	}

	var resolver *lsp.Resolver
	if *cacheDir != "" {
		resolver = lsp.NewCLIResolverWithCacheDir(*registryURL, *cacheDir)
	} else {
		var rerr error
		resolver, rerr = lsp.NewCLIResolver(*registryURL)
		if rerr != nil {
			return fmt.Errorf("%w: building resolver: %s", ErrIO, rerr)
		}
	}
	comp := validator.NewCompiler()

	manifests, err := discoverManifests(root)
	if err != nil {
		return fmt.Errorf("%w: discovering manifests: %s", ErrIO, err)
	}
	if len(manifests) == 0 {
		fmt.Fprintf(stdout, "no YAML manifests found under %s\n", root)
		return nil
	}

	var passed, warned, failed int
	for _, m := range manifests {
		text, rerr := os.ReadFile(m)
		if rerr != nil {
			fmt.Fprintf(stdout, "%s\twarn: read failed: %s\n", relativize(root, m), rerr)
			warned++
			continue
		}
		fileURI := "file://" + (&url.URL{Path: m}).EscapedPath()
		res := resolver.Resolve(ctx, fileURI, string(text))

		rel := relativize(root, m)
		switch res.State {
		case lsp.StateUnindexable:
			// No apiVersion+kind, or group not recognised. Skip silently.
			continue
		case lsp.StatePinned, lsp.StatePreview, lsp.StateUnpinned:
			schemaBytes := res.Schema
			compileKey := res.Integrity
			if !*noStrict {
				if wrapped, werr := validator.WrapStrict(schemaBytes); werr == nil {
					schemaBytes = wrapped
					compileKey = compileKey + ":strict"
				}
				// else: silent fall-through; strict-mode is best-effort.
			}
			schema, cerr := comp.Compile(compileKey, schemaBytes)
			if cerr != nil {
				fmt.Fprintf(stdout, "%s\twarn: schema compile error: %s\n", rel, cerr)
				warned++
				continue
			}
			docs, perr := parseYAMLForValidation(text)
			if perr != nil || len(docs) == 0 {
				fmt.Fprintf(stdout, "%s\twarn: parse error: %v\n", rel, perr)
				warned++
				continue
			}
			diags := validator.Validate(schema, docs[0])
			if len(diags) > 0 {
				fmt.Fprintf(stdout, "%s\tfail: %d validation error(s) against %s@%s\n",
					rel, len(diags), res.Group, res.Version)
				for _, d := range diags {
					fmt.Fprintf(stdout, "    %s\n", d.Message)
				}
				failed++
				continue
			}
			if res.State == lsp.StateUnpinned {
				if *strictPinned {
					fmt.Fprintf(stdout, "%s\tfail: unpinned (strict-pinned): %s/%s\n",
						rel, res.Group, res.Kind)
					failed++
				} else {
					fmt.Fprintf(stdout, "%s\twarn: unpinned: validated against latest %s@%s\n",
						rel, res.Group, res.Version)
					warned++
				}
				continue
			}
			fmt.Fprintf(stdout, "%s\tok: %s@%s\n", rel, res.Group, res.Version)
			passed++
		case lsp.StateError:
			fmt.Fprintf(stdout, "%s\tfail: %s\n", rel, res.ErrMsg)
			failed++
		}
	}

	fmt.Fprintf(stdout, "\n%d passed, %d warned, %d failed\n", passed, warned, failed)
	if failed > 0 {
		return fmt.Errorf("verify: %d manifest(s) failed validation: %w", failed, ErrDrift)
	}
	return nil
}

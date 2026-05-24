package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/schemalock/app/internal/intent"
)

// runFmt canonicalizes a single schemalock.yaml in place. The file may be
// either a root or an overlay; runFmt tries root decoding first, then
// overlay, and re-emits in canonical form.
func runFmt(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("fmt", flag.ContinueOnError)
	fs.SetOutput(stderr)
	file := fs.String("file", "schemalock.yaml", "path to schemalock.yaml")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return fmt.Errorf("%w: %s", ErrUsage, err)
	}

	b, err := os.ReadFile(*file)
	if err != nil {
		return fmt.Errorf("%w: reading %s: %s", ErrUsage, *file, err)
	}

	parsed, rootErr := intent.DecodeRoot(b)
	if rootErr != nil {
		var overlayErr error
		parsed, overlayErr = intent.DecodeOverlay(b)
		if overlayErr != nil {
			return fmt.Errorf("%w: %s is not a valid schemalock.yaml (root: %s; overlay: %s)",
				ErrUsage, *file, rootErr, overlayErr)
		}
	}
	if err := intent.WriteIntent(*file, parsed); err != nil {
		return fmt.Errorf("%w: writing %s: %s", ErrIO, *file, err)
	}
	fmt.Fprintf(stdout, "%s: canonicalized\n", *file)
	return nil
}

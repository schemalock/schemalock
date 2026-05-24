package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/schemalock/app/internal/intent"
	"github.com/schemalock/app/internal/registry"
)

const defaultRegistry = "https://cdn.schemalock.dev"

// runAdd appends or replaces a name@version pin. By default it walks up from
// the working directory and writes to the nearest existing schemalock.yaml.
// --file overrides the target (and creates the file if missing as an
// overlay-shape file). --no-validate skips the CDN existence check.
func runAdd(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("add", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fileFlag := fs.String("file", "", "explicit schemalock.yaml to write (creates if missing)")
	ecosystem := fs.String("ecosystem", "kubernetes", "target ecosystem")
	registryURL := fs.String("registry", defaultRegistry, "registry base URL")
	noValidate := fs.Bool("no-validate", false, "skip CDN existence check")
	cwdForTest := fs.String("cwd-for-test", "", "(test only) override cwd for the nearest-file search")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return fmt.Errorf("%w: %s", ErrUsage, err)
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("%w: exactly one <name@version> argument required", ErrUsage)
	}
	spec := fs.Arg(0)
	name, version, err := intent.ParseSpec(spec)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrUsage, err)
	}

	// Choose target file.
	target := *fileFlag
	if target == "" {
		startDir := *cwdForTest
		if startDir == "" {
			startDir, err = os.Getwd()
			if err != nil {
				return fmt.Errorf("%w: getwd: %s", ErrIO, err)
			}
		}
		target, err = findNearestIntentFile(startDir)
		if err != nil {
			return fmt.Errorf("%w: %s", ErrUsage, err)
		}
	}

	if !*noValidate {
		if err := validateVersionOnCDN(ctx, *registryURL, *ecosystem, name, version); err != nil {
			return err
		}
	}

	// Load current contents, if any. --file may point at a non-existent path
	// (overlay-create); in that case current stays zero.
	var current intent.IntentFile
	if b, rerr := os.ReadFile(target); rerr == nil {
		if f, derr := intent.DecodeRoot(b); derr == nil {
			current = f
		} else if f, derr := intent.DecodeOverlay(b); derr == nil {
			current = f
		} else {
			return fmt.Errorf("%w: %s is not a valid schemalock.yaml", ErrUsage, target)
		}
	} else if !errors.Is(rerr, os.ErrNotExist) {
		return fmt.Errorf("%w: reading %s: %s", ErrIO, target, rerr)
	}

	mutated, replaced, next := upsertPin(current.Ecosystems, *ecosystem, name, version)
	if !mutated {
		fmt.Fprintf(stdout, "%s/%s@%s: already present in %s, no change\n",
			*ecosystem, name, version, target)
		return nil
	}
	current.Ecosystems = next

	if err := intent.WriteIntent(target, current); err != nil {
		return fmt.Errorf("%w: writing %s: %s", ErrIO, target, err)
	}
	if replaced {
		fmt.Fprintf(stdout, "%s/%s: pinned to %s in %s (was previously pinned)\n",
			*ecosystem, name, version, target)
	} else {
		fmt.Fprintf(stdout, "%s/%s@%s: added to %s\n", *ecosystem, name, version, target)
	}
	return nil
}

// findNearestIntentFile walks up from startDir looking for a schemalock.yaml.
// Returns the absolute path of the first one found.
func findNearestIntentFile(startDir string) (string, error) {
	dir, err := filepath.Abs(startDir)
	if err != nil {
		return "", err
	}
	for {
		candidate := filepath.Join(dir, "schemalock.yaml")
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no schemalock.yaml found in %s or any parent (use --file to create one)", startDir)
		}
		dir = parent
	}
}

// upsertPin returns (mutated, replaced, newMap):
//   - mutated=false → exact duplicate, caller should no-op.
//   - mutated=true, replaced=false → fresh add.
//   - mutated=true, replaced=true → existing same-name pin replaced.
func upsertPin(eco map[string][]string, ecosystem, name, version string) (mutated, replaced bool, out map[string][]string) {
	out = make(map[string][]string, len(eco)+1)
	for k, v := range eco {
		out[k] = append([]string(nil), v...)
	}
	target := name + "@" + version

	existing := out[ecosystem]
	for _, s := range existing {
		if s == target {
			return false, false, out
		}
	}
	filtered := existing[:0]
	for _, s := range existing {
		n, _, perr := intent.ParseSpec(s)
		if perr == nil && n == name {
			replaced = true
			continue
		}
		filtered = append(filtered, s)
	}
	out[ecosystem] = append(filtered, target)
	return true, replaced, out
}

// validateVersionOnCDN fetches versions.json for the group and confirms the
// requested version is listed. ErrUsage on absent, ErrIO on network error.
func validateVersionOnCDN(ctx context.Context, registryURL, ecosystem, name, version string) error {
	c := registry.NewClient(registryURL)
	vs, err := c.FetchVersions(ctx, ecosystem, name)
	if err != nil {
		if errors.Is(err, registry.ErrNotFound) {
			return fmt.Errorf("%w: group %s/%s not in registry", ErrUsage, ecosystem, name)
		}
		return fmt.Errorf("%w: fetching versions for %s/%s: %s", ErrIO, ecosystem, name, err)
	}
	for _, v := range vs {
		if v == version {
			return nil
		}
	}
	return fmt.Errorf("%w: version %s not published for %s/%s (available: %v)",
		ErrUsage, version, ecosystem, name, vs)
}

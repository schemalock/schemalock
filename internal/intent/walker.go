package intent

import (
	"fmt"
	"os"
	"path/filepath"

	goyaml "github.com/goccy/go-yaml"
)

// LoadedFile pairs a parsed IntentFile with the absolute path it came from.
// Returned by WalkUp in root→leaf order.
type LoadedFile struct {
	// Path is the absolute filesystem path of the schemalock.yaml.
	Path string
	// File is the decoded contents.
	File IntentFile
}

// WalkUp discovers all schemalock.yaml files applicable to startDir, walking
// from startDir up to the filesystem root. The first file encountered with
// `root: true` or `version: <non-zero>` halts the walk (that file is
// included; nothing above it). The returned chain is ordered root→leaf so
// callers can fold-left when merging.
//
// Decoding role:
//   - The topmost file in the returned chain (i.e. the one closest to /) is
//     decoded as Root.
//   - Every other file is decoded as Overlay.
//   - A file with `root: true` declares itself the top of the walk
//     regardless of position; it is decoded as Root.
func WalkUp(startDir string) ([]LoadedFile, error) {
	var candidates []string // collected leaf→root
	dir := startDir
	for {
		candidate := filepath.Join(dir, "schemalock.yaml")
		info, err := os.Stat(candidate)
		isFile := err == nil && !info.IsDir()

		if isFile {
			isRoot, prescanErr := looksLikeRootFile(candidate)
			if prescanErr != nil {
				return nil, prescanErr
			}
			candidates = append(candidates, candidate)
			if isRoot {
				break
			}
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			break // filesystem root reached
		}
		dir = parent
	}

	// candidates is leaf→root. Reverse to root→leaf and decode each.
	out := make([]LoadedFile, 0, len(candidates))
	for i := len(candidates) - 1; i >= 0; i-- {
		path := candidates[i]
		isTop := i == len(candidates)-1
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", path, err)
		}
		var f IntentFile
		if isTop {
			f, err = DecodeRoot(b)
		} else {
			f, err = DecodeOverlay(b)
		}
		if err != nil {
			return nil, fmt.Errorf("decoding %s: %w", path, err)
		}
		out = append(out, LoadedFile{Path: path, File: f})
	}
	return out, nil
}

// looksLikeRootFile returns true if the YAML at path declares `root: true`
// or `version: <non-zero>` (both signal a root file by the spec). The check
// is intentionally permissive — full validation happens during decode.
func looksLikeRootFile(path string) (bool, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("reading %s: %w", path, err)
	}
	type marker struct {
		Version int  `yaml:"version,omitempty"`
		Root    bool `yaml:"root,omitempty"`
	}
	var m marker
	// Non-strict: unknown fields tolerated here; structural validation
	// happens during the real DecodeRoot/DecodeOverlay pass.
	if err := goyaml.Unmarshal(b, &m); err != nil {
		return false, fmt.Errorf("%w: %s: %s", ErrMalformedIntent, path, err)
	}
	return m.Root || m.Version > 0, nil
}

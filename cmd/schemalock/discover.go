package main

import (
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/schemalock/app/internal/yamldoc"
)

// discoverManifests walks root and returns the absolute paths of every
// .yaml/.yml file (excluding any schemalock.yaml itself).
//
// .gitignore is not honored in this PoC; the spec calls for it as a follow-up.
// Symlinks are not followed.
func discoverManifests(root string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		if name == "schemalock.yaml" {
			return nil
		}
		if !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml") {
			return nil
		}
		out = append(out, path)
		return nil
	})
	return out, err
}

// parseYAMLForValidation parses src into yamldoc.Documents.
func parseYAMLForValidation(src []byte) ([]yamldoc.Document, error) {
	return yamldoc.Parse(src)
}

// relativize returns p relative to root, or p unchanged on error.
func relativize(root, p string) string {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return p
	}
	return rel
}

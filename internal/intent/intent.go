// Package intent implements the schemalock.yaml file model: types,
// hierarchical discovery (walker), and effective-set merge.
//
// Two file shapes:
//   - Root: top-level schemalock.yaml OR any file with `root: true`.
//     May declare `version`, `ecosystems`, and `root: true`.
//   - Overlay: any nested schemalock.yaml without `root: true`.
//     May declare only `ecosystems`. Unknown fields are a parse error.
package intent

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	goyaml "github.com/goccy/go-yaml"

	"github.com/schemalock/app/internal/atomicfs"
)

// ErrMalformedIntent is returned when a schemalock.yaml file cannot be
// decoded, fails structural validation, or carries fields that are not
// allowed for its role (root vs overlay).
var ErrMalformedIntent = errors.New("malformed schemalock.yaml")

// IntentFile is one decoded schemalock.yaml.
//
// For root files: Version is required (non-zero), Ecosystems may be set,
// Root may be true (explicit) or false (top-level by position).
// For overlay files: only Ecosystems may be set; presence of Version or
// Root yields ErrMalformedIntent.
type IntentFile struct {
	// Version is the schema-file format version. Currently always 1.
	// Required on root files; absent on overlays.
	Version int `yaml:"version,omitempty"`
	// Root, when true, marks this file as an inheritance halt point during
	// the hierarchy walk.
	Root bool `yaml:"root,omitempty"`
	// Ecosystems maps ecosystem names (e.g. "kubernetes") to a list of
	// "name@version" specifiers.
	Ecosystems map[string][]string `yaml:"ecosystems"`
}

// DecodeRoot parses a root schemalock.yaml from b. Requires Version > 0.
// Rejects unknown fields.
func DecodeRoot(b []byte) (IntentFile, error) {
	var f IntentFile
	if err := goyaml.UnmarshalWithOptions(b, &f, goyaml.Strict()); err != nil {
		return IntentFile{}, fmt.Errorf("%w: %s", ErrMalformedIntent, err)
	}
	if f.Version == 0 {
		return IntentFile{}, fmt.Errorf("%w: missing or zero version field (root files require version: 1)", ErrMalformedIntent)
	}
	return f, nil
}

// ReadRoot reads and decodes a root schemalock.yaml from disk.
func ReadRoot(path string) (IntentFile, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return IntentFile{}, fmt.Errorf("reading intent file %s: %w", path, err)
	}
	return DecodeRoot(b)
}

// overlayShape is used to reject root-only fields when decoding overlays.
// Unknown fields are caught by the strict decoder; presence of Version or
// Root in the resulting IntentFile is rejected explicitly.
type overlayShape struct {
	Version    int                 `yaml:"version,omitempty"`
	Root       bool                `yaml:"root,omitempty"`
	Ecosystems map[string][]string `yaml:"ecosystems"`
}

// DecodeOverlay parses an overlay schemalock.yaml from b. Rejects any
// declaration of `version:` or `root: true`; only `ecosystems:` is allowed.
func DecodeOverlay(b []byte) (IntentFile, error) {
	var s overlayShape
	if err := goyaml.UnmarshalWithOptions(b, &s, goyaml.Strict()); err != nil {
		return IntentFile{}, fmt.Errorf("%w: %s", ErrMalformedIntent, err)
	}
	if s.Version != 0 {
		return IntentFile{}, fmt.Errorf("%w: overlay files must not declare `version:` (only root files may)", ErrMalformedIntent)
	}
	if s.Root {
		return IntentFile{}, fmt.Errorf("%w: overlay files must not declare `root: true`", ErrMalformedIntent)
	}
	return IntentFile{Ecosystems: s.Ecosystems}, nil
}

// ReadOverlay reads and decodes an overlay schemalock.yaml from disk.
func ReadOverlay(path string) (IntentFile, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return IntentFile{}, fmt.Errorf("reading overlay intent file %s: %w", path, err)
	}
	return DecodeOverlay(b)
}

// intentForEncode mirrors IntentFile with a MapSlice for deterministic
// ecosystem-key ordering. Fields with zero values are omitted via the
// omitempty tag.
type intentForEncode struct {
	Version    int             `yaml:"version,omitempty"`
	Root       bool            `yaml:"root,omitempty"`
	Ecosystems goyaml.MapSlice `yaml:"ecosystems"`
}

// EncodeIntent emits canonical YAML: ecosystem keys sorted alphabetically,
// spec list within each ecosystem sorted alphabetically, 2-space indent,
// single trailing newline. Works for both root and overlay shapes (driven
// by which fields are non-zero on f).
func EncodeIntent(f IntentFile) ([]byte, error) {
	keys := make([]string, 0, len(f.Ecosystems))
	for k := range f.Ecosystems {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	eco := make(goyaml.MapSlice, 0, len(keys))
	for _, k := range keys {
		specs := append([]string(nil), f.Ecosystems[k]...)
		sort.Strings(specs)
		eco = append(eco, goyaml.MapItem{Key: k, Value: specs})
	}

	out := intentForEncode{
		Version:    f.Version,
		Root:       f.Root,
		Ecosystems: eco,
	}
	b, err := goyaml.MarshalWithOptions(out, goyaml.Indent(2), goyaml.IndentSequence(true))
	if err != nil {
		return nil, fmt.Errorf("encoding intent: %w", err)
	}
	// Normalise to a single trailing newline.
	b = []byte(strings.TrimRight(string(b), "\n") + "\n")
	return b, nil
}

// WriteIntent writes f to path atomically in canonical form.
func WriteIntent(path string, f IntentFile) error {
	b, err := EncodeIntent(f)
	if err != nil {
		return err
	}
	if err := atomicfs.AtomicWrite(path, b); err != nil {
		return fmt.Errorf("writing intent file %s: %w", path, err)
	}
	return nil
}

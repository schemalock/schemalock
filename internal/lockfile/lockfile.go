package lockfile

import (
	"fmt"
	"os"
	"sort"
	"strings"

	goyaml "github.com/goccy/go-yaml"
	"github.com/schemalock/app/internal/atomicfs"
)

// LockFile represents schemalock.lock — the generated, integrity-pinned
// record of every schema group declared in schemalock.yaml.
type LockFile struct {
	// Version is the schema version of this file. Currently always 1.
	Version int `yaml:"version"`
	// Registry is the base URL of the CDN that was queried when this
	// lockfile was generated.
	Registry string `yaml:"registry"`
	// GeneratedAt is the RFC3339 UTC timestamp of when this lockfile was
	// last written (provided by the caller for deterministic tests).
	GeneratedAt string `yaml:"generated_at"`
	// Entries lists one entry per pinned group, sorted by ID ascending.
	Entries []LockEntry `yaml:"entries"`
}

// LockEntry is one resolved pinned group. The Schemas map is sorted
// alphabetically by Kind when emitted.
type LockEntry struct {
	// ID is the canonical identifier: "ecosystem/group@version",
	// e.g. "kubernetes/operator.victoriametrics.com@0.70.0".
	ID string `yaml:"id"`
	// Base is the CDN path prefix for schemas in this entry,
	// e.g. "kubernetes/operator.victoriametrics.com/0.70.0/".
	Base string `yaml:"base"`
	// Schemas maps Kind names (e.g. "VMCluster") to their SchemaEntry.
	// On read this is populated from YAML; on write EncodeLock sorts the keys.
	Schemas map[string]SchemaEntry `yaml:"schemas"`
}

// SchemaEntry holds the integrity and size of one Kind's schema file.
type SchemaEntry struct {
	// Integrity is the SRI string: "sha256-<base64-std-encoded SHA-256>".
	Integrity string `yaml:"integrity"`
	// Size is the byte length of the schema file.
	Size int `yaml:"size"`
}

// lockEntryForEncode is a version of LockEntry suitable for deterministic
// YAML encoding. The Schemas field is replaced by an explicitly ordered
// goyaml.MapSlice so that goccy/go-yaml emits keys in sorted order.
type lockEntryForEncode struct {
	ID      string          `yaml:"id"`
	Base    string          `yaml:"base"`
	Schemas goyaml.MapSlice `yaml:"schemas"`
}

// lockFileForEncode mirrors LockFile but with Entries using lockEntryForEncode.
type lockFileForEncode struct {
	Version     int                  `yaml:"version"`
	Registry    string               `yaml:"registry"`
	GeneratedAt string               `yaml:"generated_at"`
	Entries     []lockEntryForEncode `yaml:"entries"`
}

// ReadLock parses schemalock.lock from the file at path.
func ReadLock(path string) (LockFile, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return LockFile{}, fmt.Errorf("reading lock file %s: %w", path, err)
	}
	return DecodeLock(b)
}

// DecodeLock parses schemalock.lock from b.
// Returns ErrMalformedLock if the YAML is invalid.
func DecodeLock(b []byte) (LockFile, error) {
	var lf LockFile
	if err := goyaml.Unmarshal(b, &lf); err != nil {
		return LockFile{}, fmt.Errorf("%w: %s", ErrMalformedLock, err)
	}
	return lf, nil
}

// EncodeLock marshals lf to deterministic YAML bytes with the following
// invariants:
//   - entries sorted ascending by ID
//   - within each entry, schemas sorted alphabetically by Kind name
//   - 2-space indent, LF line endings, single trailing newline
//   - ASCII-only output for integrity and id strings
func EncodeLock(lf LockFile) ([]byte, error) {
	// Sort entries by ID.
	sorted := make([]LockEntry, len(lf.Entries))
	copy(sorted, lf.Entries)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].ID < sorted[j].ID
	})

	// Build the encoding-friendly representation.
	enc := lockFileForEncode{
		Version:     lf.Version,
		Registry:    lf.Registry,
		GeneratedAt: lf.GeneratedAt,
		Entries:     make([]lockEntryForEncode, len(sorted)),
	}

	for i, e := range sorted {
		// Collect and sort schema Kind names.
		kinds := make([]string, 0, len(e.Schemas))
		for k := range e.Schemas {
			kinds = append(kinds, k)
		}
		sort.Strings(kinds)

		// Build an ordered MapSlice for this entry's schemas.
		// goccy/go-yaml preserves MapSlice order on encode.
		schemas := make(goyaml.MapSlice, len(kinds))
		for j, kind := range kinds {
			schemas[j] = goyaml.MapItem{
				Key:   kind,
				Value: e.Schemas[kind],
			}
		}

		enc.Entries[i] = lockEntryForEncode{
			ID:      e.ID,
			Base:    e.Base,
			Schemas: schemas,
		}
	}

	b, err := goyaml.MarshalWithOptions(enc, goyaml.Indent(2))
	if err != nil {
		return nil, fmt.Errorf("encoding lockfile: %w", err)
	}

	// Ensure exactly one trailing newline (goccy/go-yaml already adds one,
	// but we normalise defensively).
	b = []byte(strings.TrimRight(string(b), "\n") + "\n")

	return b, nil
}

// WriteLock encodes lf and atomically writes it to path.
func WriteLock(path string, lf LockFile) error {
	b, err := EncodeLock(lf)
	if err != nil {
		return fmt.Errorf("writing lock file %s: %w", path, err)
	}
	if err := atomicfs.AtomicWrite(path, b); err != nil {
		return fmt.Errorf("writing lock file %s: %w", path, err)
	}
	return nil
}

// ResolveID splits an entry ID of the form "ecosystem/group@version" into its
// three components. Returns ErrMalformedID if the ID does not conform to this
// format or if any component is empty.
func ResolveID(id string) (ecosystem, group, version string, err error) {
	ecosystem, rest, ok := strings.Cut(id, "/")
	if !ok {
		return "", "", "", fmt.Errorf("%w: %q (missing /)", ErrMalformedID, id)
	}
	group, version, ok = strings.Cut(rest, "@")
	if !ok {
		return "", "", "", fmt.Errorf("%w: %q (missing @)", ErrMalformedID, id)
	}

	if ecosystem == "" {
		return "", "", "", fmt.Errorf("%w: %q (empty ecosystem)", ErrMalformedID, id)
	}
	if group == "" {
		return "", "", "", fmt.Errorf("%w: %q (empty group)", ErrMalformedID, id)
	}
	if version == "" {
		return "", "", "", fmt.Errorf("%w: %q (empty version)", ErrMalformedID, id)
	}

	return ecosystem, group, version, nil
}

// BuildID is the inverse of ResolveID. It returns "ecosystem/group@version".
func BuildID(ecosystem, group, version string) string {
	return ecosystem + "/" + group + "@" + version
}

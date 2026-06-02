// Package cache implements a filesystem mirror of the SchemaLock CDN.
// Schemas are stored under a root directory using the same path layout as
// cdn.schemalock.dev: <root>/<ecosystem>/<group>/<version>/<Kind>.json.
//
// Writes go through integrity verification before the bytes are persisted.
// Reads do not re-verify; the filesystem is authoritative after a verified
// write. Concurrent writes to the same path are safe because the underlying
// [atomicfs.AtomicWrite] uses a temp-file-then-rename strategy.
package cache

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/schemalock/app/internal/atomicfs"
	"github.com/schemalock/app/internal/registry"
)

// ErrNotFound is returned by [Cache.ReadSchema] when no cached file exists for
// the requested (ecosystem, group, version, kind) tuple.
var ErrNotFound = errors.New("cache: not found")

// Cache mirrors the CDN URL layout under a root directory.
// The zero value is not usable; construct with [New].
type Cache struct {
	root string
}

// New returns a Cache rooted at the given directory.
// The directory need not exist yet; it is created lazily when the first schema
// is written via [Cache.WriteSchema].
func New(root string) *Cache {
	return &Cache{root: root}
}

// DefaultRoot returns the canonical default cache directory for the current
// user: <homeDir>/.schemalock/cache.
//
// This is the only entry point in this package that reads $HOME. It is
// intended to be called once by cmd/schemalock during startup, not by any
// internal package.
func DefaultRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cache: resolve home dir: %w", err)
	}
	return filepath.Join(home, ".schemalock", "cache"), nil
}

// Root returns the root directory of this Cache. It is the value passed to
// [New]. Callers that need the raw filesystem root (e.g. to pass to a
// resolver chain) should use this rather than re-deriving it from SchemaPath.
func (c *Cache) Root() string { return c.root }

// SchemaPath returns the on-disk path for an (ecosystem, group, version, kind)
// tuple. Returns an error if any segment is unsafe (contains "..", path
// separators, or null bytes). The layout is byte-identical to the CDN URL:
//
//	<root>/<ecosystem>/<group>/<version>/<Kind>.json
//
// This is a pure function; it performs no I/O.
func (c *Cache) SchemaPath(ecosystem, group, version, kind string) (string, error) {
	for label, seg := range map[string]string{
		"ecosystem": ecosystem,
		"group":     group,
		"version":   version,
		"kind":      kind,
	} {
		if !safeSegment(seg) {
			return "", fmt.Errorf("cache: unsafe %s segment %q", label, seg)
		}
	}
	return filepath.Join(c.root, ecosystem, group, version, kind+".json"), nil
}

// safeSegment reports whether s is safe to use as a single filesystem path
// segment. It rejects empty strings, ".", "..", and strings containing path
// separators or null bytes.
func safeSegment(s string) bool {
	if s == "" || s == "." || s == ".." {
		return false
	}
	return !strings.ContainsAny(s, "/\\\x00")
}

// ReadSchema returns the cached schema bytes for the given tuple.
//
// If no file exists for the tuple, ReadSchema returns an error wrapping
// [ErrNotFound]. All other I/O errors are wrapped with [fmt.Errorf].
// Integrity is not re-verified on read; the filesystem is authoritative after
// a successful [Cache.WriteSchema].
func (c *Cache) ReadSchema(ecosystem, group, version, kind string) ([]byte, error) {
	path, err := c.SchemaPath(ecosystem, group, version, kind)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("cache: read %s: %w", path, ErrNotFound)
		}
		return nil, fmt.Errorf("cache: read %s: %w", path, err)
	}
	return data, nil
}

// WriteSchema verifies the integrity of body against the given SRI integrity
// string, then atomically writes body to the cache.
//
// If integrity verification fails, WriteSchema returns an error wrapping
// [registry.ErrIntegrityMismatch] and no file is created or modified.
// Callers can distinguish this case with:
//
//	errors.Is(err, registry.ErrIntegrityMismatch)
//
// On verification success, body is written atomically via [atomicfs.AtomicWrite],
// which creates parent directories as needed (mode 0o755) and leaves no
// partial file behind on failure.
func (c *Cache) WriteSchema(ecosystem, group, version, kind, integrity string, body []byte) error {
	if err := registry.VerifyIntegrity(body, integrity); err != nil {
		return fmt.Errorf("cache: write %s/%s/%s/%s: %w", ecosystem, group, version, kind, err)
	}
	path, err := c.SchemaPath(ecosystem, group, version, kind)
	if err != nil {
		return fmt.Errorf("cache: write %s/%s/%s/%s: %w", ecosystem, group, version, kind, err)
	}
	if err := atomicfs.AtomicWrite(path, body); err != nil {
		return fmt.Errorf("cache: write %s/%s/%s/%s: %w", ecosystem, group, version, kind, err)
	}
	return nil
}

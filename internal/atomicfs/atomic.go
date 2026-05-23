// Package atomicfs provides helpers for atomic filesystem writes.
// All writes go through a temp-file-then-rename pattern so that concurrent
// readers never observe a half-written file.
package atomicfs

import (
	"fmt"
	"os"
	"path/filepath"
)

// AtomicWrite writes data to path atomically via a sibling temp file.
// It creates parent directories (mode 0o755) if they do not exist.
// The final file is created with mode 0o644.
// On success the temp file is renamed over path; on any error the temp file
// is removed so no partial file is left behind.
func AtomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("atomicfs: create parent dirs for %s: %w", path, err)
	}

	tmp, err := os.CreateTemp(dir, ".schemalock-tmp-*")
	if err != nil {
		return fmt.Errorf("atomicfs: create temp file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()

	// Ensure cleanup on any failure path.
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("atomicfs: write to temp file %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("atomicfs: close temp file %s: %w", tmpName, err)
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return fmt.Errorf("atomicfs: chmod temp file %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("atomicfs: rename %s → %s: %w", tmpName, path, err)
	}

	ok = true
	return nil
}

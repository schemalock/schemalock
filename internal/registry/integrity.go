package registry

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
)

// ComputeIntegrity returns the canonical W3C SRI integrity string for body.
// The format is "sha256-<base64-std>" using standard (padded) base64 encoding,
// which is byte-identical to what ingest/internal/crd.SRIIntegrity produces.
func ComputeIntegrity(body []byte) string {
	sum := sha256.Sum256(body)
	return "sha256-" + base64.StdEncoding.EncodeToString(sum[:])
}

// VerifyIntegrity checks that the SHA-256 digest of body matches integrity.
// integrity must be in "sha256-<base64-std>" format. Returns nil on success,
// or a wrapped ErrIntegrityMismatch on failure.
func VerifyIntegrity(body []byte, integrity string) error {
	got := ComputeIntegrity(body)
	if got == integrity {
		return nil
	}
	return fmt.Errorf("registry: integrity mismatch (got %s, want %s): %w", got, integrity, ErrIntegrityMismatch)
}

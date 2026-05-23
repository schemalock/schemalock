// Package registry provides an HTTP client for fetching manifests and schemas
// from a SchemaLock-compatible CDN (currently cdn.schemalock.dev).
package registry

import "errors"

// ErrIntegrityMismatch is returned by VerifyIntegrity when the computed
// SHA-256 digest of the body does not match the expected integrity string.
var ErrIntegrityMismatch = errors.New("schema integrity mismatch")

// ErrNotFound is returned when the CDN responds with HTTP 404 for a manifest
// or schema request.
var ErrNotFound = errors.New("not found")

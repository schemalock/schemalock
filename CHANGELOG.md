# Changelog

All notable changes to the `schemalock` binary are documented here.
Format follows [Keep a Changelog](https://keepachangelog.com/en/1.0.0/);
this project uses [Semantic Versioning](https://semver.org/).

## Unreleased

### Added

- Apache-2.0 LICENSE.
- This CHANGELOG.

## 0.1.0 — 2026-05-22

Initial public release. Single Go binary that combines a CLI
(`schemalock lock`, `schemalock verify`) and an LSP server
(`schemalock serve --stdio`).

### Added

- `schemalock lock` — reads `schemalock.yaml`, fetches per-kind schemas
  from `cdn.schemalock.dev`, and writes a deterministic, integrity-pinned
  `schemalock.lock`.
- `schemalock verify` — exits non-zero when the lockfile has drifted from
  the live CDN.
- `schemalock serve --stdio` — language server over stdin/stdout providing
  diagnostics, completion, and hover for YAML documents whose
  `apiVersion`+`kind` resolves to an entry in the lockfile.
- **CDN fallback for unpinned documents.** When a document's
  `apiVersion`+`kind` is not in the lockfile, the server fetches the
  latest available schema from `cdn.schemalock.dev` and validates against
  it (integrity-verified, cached on disk under `~/.schemalock/cache/`).
- **Per-document version override.** Custom LSP methods
  `schemalock/getDocumentState`, `listVersionsForGroup`,
  `setDocumentVersionOverride`, `retryResolve`, `cacheDebug`, plus the
  `schemalock/documentStateChanged` server-push notification. Lets an
  editor preview a different schema version for an open file without
  modifying the lockfile.
- **Bundled yaml-language-server proxy.** For YAML documents not owned by
  the lockfile (`apiVersion`+`kind` does not resolve), the server spawns
  `yaml-language-server` as a subprocess and proxies requests through it.
  Owned documents are served by schemalock natively; diagnostics for
  owned files are dropped from the yaml-language-server stream to avoid
  duplicates.
- GitHub Actions release workflow producing 5 platform binaries
  (linux-x64, linux-arm64, darwin-x64, darwin-arm64, win32-x64) plus
  `SHA256SUMS` on every `v*` tag push.

# SchemaLock — app

`schemalock` is a single Go binary that combines a CLI and a Language
Server Protocol (LSP) server for Kubernetes operator schema validation.
It reads a `schemalock.yaml` intent file to determine which operators to
pin, fetches CRD JSON Schemas from the
[SchemaLock CDN](https://cdn.schemalock.dev), and writes an
integrity-pinned `schemalock.lock`. When invoked as an LSP server it
validates open YAML documents, providing real-time diagnostics,
auto-completion, and hover documentation in any editor that supports
LSP.

For Kubernetes CRD documents not present in the lockfile, the server
auto-fetches the latest available schema from the SchemaLock CDN
(integrity-verified, cached on disk) so users can work with new CRDs
without authoring a lockfile first.

## Install

### Pre-built binaries (recommended)

Download the binary for your platform from the
[latest GitHub release](https://github.com/schemalock/app/releases/latest)
and place it on your `PATH`. Each release attaches
`schemalock-{linux,darwin}-{x64,arm64}` and `schemalock-win32-x64.exe`
plus a `SHA256SUMS` file.

### From source

Requires Go 1.26 or newer.

```bash
go install github.com/schemalock/app/cmd/schemalock@latest
```

Or clone and build:

```bash
git clone https://github.com/schemalock/app
cd app
go build -o bin/schemalock ./cmd/schemalock
```

The release workflow injects version metadata via `-ldflags`; a manual
build without ldflags prints `schemalock dev (commit unknown, built unknown)`
for `schemalock --version`.

## Usage

```bash
schemalock verify                            # validate every YAML under cwd
schemalock verify --path manifests/ --strict-pinned
schemalock verify --no-strict   # tolerate unknown YAML fields
schemalock add operator.victoriametrics.com@0.70.0
schemalock add --file teamA/schemalock.yaml operator.victoriametrics.com@0.69.0
schemalock fmt                               # canonicalize nearest schemalock.yaml
schemalock fmt --file teamA/schemalock.yaml
schemalock serve --stdio                     # LSP server for editors
```

### Hierarchical `schemalock.yaml`

`schemalock.yaml` can be nested. The effective pin set for a manifest is the
**union** of every `schemalock.yaml` walking up from the manifest's
directory, with the closest file winning on same-name pin conflicts.

A nested file with `root: true` halts the walk — useful for sub-projects
inside a monorepo that should not inherit grandparent pins.

Nested files (without `root: true`) may only declare `ecosystems:`; the
`version:` field is root-only.

Example root `schemalock.yaml`:

```yaml
version: 1
ecosystems:
  kubernetes:
    - cert-manager.io@v1.16.1
    - operator.victoriametrics.com@0.70.0
```

Example overlay at `teamA/schemalock.yaml`:

```yaml
ecosystems:
  kubernetes:
    - operator.victoriametrics.com@0.69.0   # downgrade for teamA only
```

### `verify` — CI drift gate

```bash
schemalock verify --path .
```

For each YAML document under `--path`:

1. Walk up from the manifest's directory to build its effective pin set.
2. If the `(group, kind)` is owned by a pinned version → validate against
   the pinned schema (fetched on demand, cached in `~/.schemalock/cache/`).
3. If not pinned → fall back to the latest schema on `cdn.schemalock.dev`
   and warn (or fail, with `--strict-pinned`).

Exits **0** when no manifest fails; non-zero otherwise.

Unknown fields in any manifest (typos, deprecated keys not in the
schema) fail verification by default. Pass `--no-strict` to match
the LSP `strict: false` mode and tolerate them.

### `add` and `fmt`

`schemalock add <name@version>` appends or replaces a pin in the nearest
existing `schemalock.yaml` (walks up from cwd). Use `--file <path>` to
target a specific file — or to create a new overlay (the file is
materialised on first write).

`schemalock fmt [--file path]` canonicalises a single `schemalock.yaml`
in place: ecosystem keys + spec lists sorted alphabetically, 2-space
indent, single trailing newline. Works on both root and overlay files.

### `serve --stdio` — LSP server for editors

```bash
schemalock serve --stdio
```

Speaks the Language Server Protocol over stdin/stdout. Editors spawn
this process and communicate via JSON-RPC framed with `Content-Length`
headers. The server resolves each open YAML document through a chain:

1. Per-document override (set by the editor for "preview this version").
2. Intent — walk up from the document's directory through every
   `schemalock.yaml` to find the effective pinned version, then fetch
   that version's schema from the CDN cache (or CDN directly).
3. CDN fallback — latest version published for the document's
   `(group, kind)` when no intent pin matches.

Capabilities advertised: full text-document sync, completion, hover,
definition, references, document symbols.

Schemas fetched from the CDN are verified against their integrity hash
(from `manifest.json` on the CDN) before being written to the disk cache
(`~/.schemalock/cache/`, byte-identical mirror of the CDN URL layout).

For YAML documents the server cannot resolve a schema for (plain
manifests, GitHub workflows, etc.), the server spawns
`yaml-language-server` as a subprocess and proxies requests to it. The
host editor sees one LSP — schemalock — and never has to coordinate two
servers itself.

`yaml-language-server` v1.14 or newer is required for the proxy path.
When `yaml-language-server` is not on `PATH`, the server continues to
operate but only provides diagnostics for documents it can resolve a
schema for (lockfile or CDN fallback).

### Configuration via `initializationOptions`

The server reads an optional `initializationOptions` object from the LSP
`initialize` request params:

```jsonc
{
  "yamlLanguageServer": { "path": "/abs/path/to/yaml-language-server" }
}
```

| Key | Type | Default | Meaning |
|---|---|---|---|
| `yamlLanguageServer.path` | string | `""` | Override the path to `yaml-language-server`. Empty = search `PATH`. |

Unknown values are silently ignored; `initialize` never fails because
of a malformed payload.

## Editor setup

### Visual Studio Code

Install the SchemaLock extension from the Marketplace
([SchemaLock.schemalock](https://marketplace.visualstudio.com/items?itemName=SchemaLock.schemalock))
or from the
[`schemalock/vscode`](https://github.com/schemalock/vscode) repo. The
extension bundles a per-platform `schemalock` binary, so most users do
not need to install anything from this repo.

### Neovim (`nvim-lspconfig`)

```lua
local lspconfig = require("lspconfig")
local configs   = require("lspconfig.configs")

if not configs.schemalock then
  configs.schemalock = {
    default_config = {
      cmd          = { "schemalock", "serve", "--stdio" },
      filetypes    = { "yaml" },
      root_dir     = lspconfig.util.root_pattern("schemalock.lock", "schemalock.yaml"),
      single_file_support = true,
      settings     = {},
    },
  }
end

lspconfig.schemalock.setup({
  on_attach = function(client, bufnr)
    vim.keymap.set("n", "K",          vim.lsp.buf.hover,       { buffer = bufnr })
    vim.keymap.set("n", "<leader>ca", vim.lsp.buf.code_action, { buffer = bufnr })
  end,
})
```

The server discovers `schemalock.lock` by walking up from the buffer's
directory.

## Testing

```bash
# Offline unit + integration tests (no network, race-clean):
go test ./... -race -count=1

# LSP stress test (100 iterations, race detector):
go test ./internal/lsp/... -race -count=100

# Live CDN smoke (requires network access):
SCHEMALOCK_LIVE=1 go test -tags live ./internal/lsp/... -count=1 -run LiveCDN
```

The live CDN smoke test is gated by `//go:build live` and the
`SCHEMALOCK_LIVE=1` environment variable — it is never executed by the
default offline suite.

## Testdata workspaces

```
testdata/
  workspace_good/
    schemalock.yaml      # pins operator.victoriametrics.com@0.70.0
    schemalock.lock      # generated against cdn.schemalock.dev
    vmcluster.yaml       # valid VMCluster document (zero diagnostics)
  workspace_bad/
    schemalock.yaml      # same intent as workspace_good
    schemalock.lock      # same lockfile as workspace_good
    vmcluster_typo.yaml  # replicationFactor: "three" — type error
```

To regenerate `schemalock.lock` after a schema update:

```bash
bin/schemalock lock \
  --file     testdata/workspace_good/schemalock.yaml \
  --lockfile testdata/workspace_good/schemalock.lock \
  --registry https://cdn.schemalock.dev
cp testdata/workspace_good/schemalock.lock testdata/workspace_bad/schemalock.lock
```

## License

Licensed under the Apache License, Version 2.0 — see [LICENSE](LICENSE).

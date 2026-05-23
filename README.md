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

### `lock` — pin schemas from the CDN

In a directory containing `schemalock.yaml`:

```bash
schemalock lock
```

Or with explicit paths:

```bash
schemalock lock \
  --file     path/to/schemalock.yaml \
  --lockfile path/to/schemalock.lock \
  --registry https://cdn.schemalock.dev
```

Reads `schemalock.yaml`, fetches the manifest for each declared operator
version from the CDN, and writes a deterministic `schemalock.lock`
pinning every schema's SHA-256 integrity hash.

Example `schemalock.yaml`:

```yaml
version: 1
ecosystems:
  kubernetes:
    - operator.victoriametrics.com@0.70.0
```

### `verify` — check for drift

```bash
schemalock verify
```

Exits **0** when the lockfile matches the live CDN, **2** when any
integrity hash or schema size has drifted, **1** on usage errors
(missing file, malformed intent), **3** on network or I/O errors.

### `serve --stdio` — LSP server for editors

```bash
schemalock serve --stdio
```

Speaks the Language Server Protocol over stdin/stdout. Editors spawn
this process and communicate via JSON-RPC framed with `Content-Length`
headers. The server locates `schemalock.lock` by walking up from the
workspace root and validates every open YAML document against the
appropriate schema on `textDocument/didOpen` and `textDocument/didChange`.

Capabilities advertised: full text-document sync, completion, hover,
definition, references, document symbols.

For YAML documents whose `apiVersion`+`kind` is not in the lockfile, the
server consults a chain of resolvers:

1. Per-document override (set by the editor for "preview this version").
2. Lockfile entry, fetched from the CDN cache (or the CDN directly).
3. CDN fallback — latest version published for the document's
   `(group, kind)`.

Schemas fetched from the CDN are verified against their integrity hash
before being written to the disk cache (`~/.schemalock/cache/`,
byte-identical mirror of the CDN URL layout).

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

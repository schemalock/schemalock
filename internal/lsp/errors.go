package lsp

import "errors"

// ErrNoMatch is returned by [SchemaResolver.Resolve] when no lock entry
// matches the given (apiVersion, kind) pair.
var ErrNoMatch = errors.New("lsp: no schema entry for apiVersion/kind")

// ErrAmbiguousKind is returned by [NewSchemaResolver] (and [SchemaResolver.Reload])
// when two or more lock entries share the same (group, kind) pair at different
// release versions. The user must narrow their schemalock.yaml to a single
// release per group.
var ErrAmbiguousKind = errors.New("lsp: ambiguous kind — multiple locked releases share the same group and kind")

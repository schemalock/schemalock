package lsp

import (
	"errors"
	"fmt"

	goyaml "github.com/goccy/go-yaml"
	"github.com/goccy/go-yaml/parser"
	"github.com/goccy/go-yaml/token"
	lsp "go.lsp.dev/protocol"
)

// BaselineDiagnostics parses text as YAML and returns LSP diagnostics for any
// parse errors found. It is a pure function — no Server reference, no I/O.
//
// This is the fallback path used when yaml-language-server is absent and the
// document is unowned: the user still sees YAML syntax errors even without
// full schema validation.
//
// Returns an empty (non-nil) slice when the document parses cleanly, which
// signals the editor to clear any prior diagnostics for this URI.
//
// Position conversion: goccy reports 1-based line and column numbers. The LSP
// protocol uses 0-based positions. Values are converted by subtracting 1 and
// clamping to zero to prevent negative values (mirrors toLSPDiagnostic in
// workers.go).
func BaselineDiagnostics(text string) []lsp.Diagnostic {
	if text == "" {
		return []lsp.Diagnostic{}
	}

	_, parseErr := parser.ParseBytes([]byte(text), 0)
	if parseErr == nil {
		return []lsp.Diagnostic{}
	}

	return []lsp.Diagnostic{diagnosticFromError(parseErr)}
}

// diagnosticFromError converts a goccy parse error into an lsp.Diagnostic.
// It attempts to extract the token position from the well-known goccy error
// types. When no position is available it falls back to 0:0.
func diagnosticFromError(err error) lsp.Diagnostic {
	// Try each concrete public goccy error type in turn.
	line, col, msg := positionFromError(err)

	return lsp.Diagnostic{
		Range: lsp.Range{
			Start: lsp.Position{Line: uint32(line), Character: uint32(col)},
			End:   lsp.Position{Line: uint32(line), Character: uint32(col)},
		},
		Severity: lsp.DiagnosticSeverityError,
		Source:   "schemalock",
		Message:  fmt.Sprintf("YAML parse error: %s", msg),
	}
}

// positionFromError returns 0-based (line, col) and a short message string
// from a goccy parse error. All positions are clamped to >= 0.
func positionFromError(err error) (line, col int, msg string) {
	if syntaxErr, ok := errors.AsType[*goyaml.SyntaxError](err); ok {
		return tokenPos(syntaxErr.GetToken(), syntaxErr.GetMessage())
	}
	if dupErr, ok := errors.AsType[*goyaml.DuplicateKeyError](err); ok {
		return tokenPos(dupErr.GetToken(), dupErr.GetMessage())
	}
	if unknownErr, ok := errors.AsType[*goyaml.UnknownFieldError](err); ok {
		return tokenPos(unknownErr.GetToken(), unknownErr.GetMessage())
	}
	if typeErr, ok := errors.AsType[*goyaml.TypeError](err); ok {
		return tokenPos(typeErr.GetToken(), typeErr.GetMessage())
	}
	if overflowErr, ok := errors.AsType[*goyaml.OverflowError](err); ok {
		return tokenPos(overflowErr.GetToken(), overflowErr.GetMessage())
	}
	if unexpectedErr, ok := errors.AsType[*goyaml.UnexpectedNodeTypeError](err); ok {
		return tokenPos(unexpectedErr.GetToken(), unexpectedErr.GetMessage())
	}

	// Generic fallback — no position available.
	return 0, 0, err.Error()
}

// tokenPos converts a goccy *token.Token (1-based) to 0-based (line, col)
// clamped to >= 0. Returns 0, 0 when tok is nil or has no position.
func tokenPos(tok *token.Token, msg string) (line, col int, outMsg string) {
	if tok != nil && tok.Position != nil {
		line = max(tok.Position.Line-1, 0)
		col = max(tok.Position.Column-1, 0)
	}
	return line, col, msg
}

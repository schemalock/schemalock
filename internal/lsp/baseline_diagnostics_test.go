package lsp_test

import (
	"testing"

	"github.com/schemalock/schemalock/internal/lsp"
	lspprot "go.lsp.dev/protocol"
)

func TestBaselineDiagnostics(t *testing.T) {
	tests := []struct {
		name       string
		text       string
		wantLen    int // expected number of diagnostics
		wantLine   uint32
		wantChar   uint32
		wantNonNil bool // result slice must be non-nil
		wantSource string
	}{
		{
			name:       "valid YAML returns empty non-nil slice",
			text:       "apiVersion: apps/v1\nkind: Deployment\n",
			wantLen:    0,
			wantNonNil: true,
		},
		{
			name:       "empty input returns empty non-nil slice",
			text:       "",
			wantLen:    0,
			wantNonNil: true,
		},
		{
			name:    "unclosed sequence bracket",
			text:    "apiVersion: [unclosed",
			wantLen: 1,
			// goccy reports 1-based line=1 col=13; LSP expects 0-based line=0 col=12
			wantLine:   0,
			wantChar:   12,
			wantSource: "schemalock",
		},
		{
			name:    "tab indent error at line 2",
			text:    "key:\n\t  value",
			wantLen: 1,
			// goccy reports 1-based line=2 col=1; LSP expects 0-based line=1 col=0
			wantLine:   1,
			wantChar:   0,
			wantSource: "schemalock",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			diags := lsp.BaselineDiagnostics(tt.text)

			if tt.wantNonNil && diags == nil {
				t.Fatal("BaselineDiagnostics returned nil, want non-nil slice")
			}

			if len(diags) != tt.wantLen {
				t.Fatalf("len(diags) = %d, want %d; diags: %+v", len(diags), tt.wantLen, diags)
			}

			if tt.wantLen == 0 {
				return
			}

			d := diags[0]
			if d.Range.Start.Line != tt.wantLine {
				t.Errorf("Start.Line = %d, want %d", d.Range.Start.Line, tt.wantLine)
			}
			if d.Range.Start.Character != tt.wantChar {
				t.Errorf("Start.Character = %d, want %d", d.Range.Start.Character, tt.wantChar)
			}
			if d.Severity != lspprot.DiagnosticSeverityError {
				t.Errorf("Severity = %v, want Error", d.Severity)
			}
			if d.Source != tt.wantSource {
				t.Errorf("Source = %q, want %q", d.Source, tt.wantSource)
			}
		})
	}
}

// TestBaselineDiagnostics_MultiDoc verifies that a multi-document YAML where
// the second document has a parse error reports the diagnostic at the
// correct line (0-based) within the error document.
func TestBaselineDiagnostics_MultiDoc(t *testing.T) {
	// The first doc is valid; the second has an unclosed bracket.
	// goccy reports the error at the position in the full stream.
	//
	// Stream:
	//   line 1: ---
	//   line 2: good: doc
	//   line 3: ---
	//   line 4: bad: [unclosed
	//
	// goccy SyntaxError: 1-based line=4 col=6 → 0-based line=3 col=5
	text := "---\ngood: doc\n---\nbad: [unclosed"
	diags := lsp.BaselineDiagnostics(text)

	if len(diags) != 1 {
		t.Fatalf("expected 1 diagnostic, got %d: %+v", len(diags), diags)
	}

	d := diags[0]
	if d.Range.Start.Line != 3 {
		t.Errorf("Start.Line = %d, want 3", d.Range.Start.Line)
	}
	if d.Range.Start.Character != 5 {
		t.Errorf("Start.Character = %d, want 5", d.Range.Start.Character)
	}
}

// TestBaselineDiagnostics_PositionIsZeroBased verifies the 1-based→0-based
// conversion property: for an error on the first line the result must have
// Line=0 (not Line=1).
func TestBaselineDiagnostics_PositionIsZeroBased(t *testing.T) {
	diags := lsp.BaselineDiagnostics("bad: [unclosed")
	if len(diags) == 0 {
		t.Fatal("expected at least one diagnostic")
	}
	if diags[0].Range.Start.Line != 0 {
		t.Errorf("position should be 0-based: got Line=%d, want 0", diags[0].Range.Start.Line)
	}
}

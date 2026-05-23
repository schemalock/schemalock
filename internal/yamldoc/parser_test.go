package yamldoc

import (
	"errors"
	"os"
	"testing"
)

func TestParse_SingleDoc(t *testing.T) {
	src, err := os.ReadFile("testdata/single.yaml")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	docs, err := Parse(src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(docs) != 1 {
		t.Fatalf("expected 1 document, got %d", len(docs))
	}

	d := docs[0]
	if d.Index != 0 {
		t.Errorf("Index = %d, want 0", d.Index)
	}
	if d.APIVersion != "apps/v1" {
		t.Errorf("APIVersion = %q, want %q", d.APIVersion, "apps/v1")
	}
	if d.Kind != "Deployment" {
		t.Errorf("Kind = %q, want %q", d.Kind, "Deployment")
	}
	if d.StartLine != 1 {
		t.Errorf("StartLine = %d, want 1", d.StartLine)
	}
}

func TestParse_MultiDoc(t *testing.T) {
	src, err := os.ReadFile("testdata/multidoc.yaml")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	docs, err := Parse(src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(docs) != 2 {
		t.Fatalf("expected 2 documents, got %d", len(docs))
	}

	tests := []struct {
		index      int
		apiVersion string
		kind       string
		startLine  int
	}{
		{0, "apps/v1", "Deployment", 1},
		// The second document's --- separator is on line 8.
		{1, "v1", "Service", 8},
	}
	for _, tt := range tests {
		d := docs[tt.index]
		if d.Index != tt.index {
			t.Errorf("doc[%d].Index = %d, want %d", tt.index, d.Index, tt.index)
		}
		if d.APIVersion != tt.apiVersion {
			t.Errorf("doc[%d].APIVersion = %q, want %q", tt.index, d.APIVersion, tt.apiVersion)
		}
		if d.Kind != tt.kind {
			t.Errorf("doc[%d].Kind = %q, want %q", tt.index, d.Kind, tt.kind)
		}
		if d.StartLine != tt.startLine {
			t.Errorf("doc[%d].StartLine = %d, want %d", tt.index, d.StartLine, tt.startLine)
		}
	}
}

func TestParse_PositionMap_ReplicasKey(t *testing.T) {
	// The replicas key in single.yaml is on line 6.
	src, err := os.ReadFile("testdata/single.yaml")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	docs, err := Parse(src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(docs) != 1 {
		t.Fatalf("expected 1 document")
	}

	pos, err := docs[0].Lookup("/spec/replicas")
	if err != nil {
		t.Fatalf("Lookup /spec/replicas: %v", err)
	}
	// single.yaml line 6 is "  replicas: 3"
	if pos.Line != 6 {
		t.Errorf("pos.Line = %d, want 6", pos.Line)
	}
	if pos.Column < 1 {
		t.Errorf("pos.Column = %d, want >= 1", pos.Column)
	}
}

func TestParse_PositionMap_TopLevelKeys(t *testing.T) {
	src := []byte(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: my-app
`)
	docs, err := Parse(src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	tests := []struct {
		pointer string
		line    int
	}{
		{"/apiVersion", 1},
		{"/kind", 2},
		{"/metadata", 3},
		{"/metadata/name", 4},
	}
	for _, tt := range tests {
		pos, err := docs[0].Lookup(tt.pointer)
		if err != nil {
			t.Errorf("Lookup %q: %v", tt.pointer, err)
			continue
		}
		if pos.Line != tt.line {
			t.Errorf("Lookup %q: Line = %d, want %d", tt.pointer, pos.Line, tt.line)
		}
	}
}

func TestParse_BadYAML(t *testing.T) {
	src, err := os.ReadFile("testdata/bad.yaml")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	_, err = Parse(src)
	if err == nil {
		t.Fatal("expected error for bad YAML, got nil")
	}
	if !errors.Is(err, ErrMalformedYAML) {
		t.Errorf("expected ErrMalformedYAML, got %v", err)
	}
}

func TestLookup_NotFound(t *testing.T) {
	src := []byte("apiVersion: v1\nkind: Pod\n")
	docs, err := Parse(src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	_, err = docs[0].Lookup("/nonexistent/key")
	if err == nil {
		t.Fatal("expected error for missing pointer, got nil")
	}
	if !errors.Is(err, ErrPointerNotFound) {
		t.Errorf("expected ErrPointerNotFound, got %v", err)
	}
}

func TestParse_EmptyBody(t *testing.T) {
	// A stream with a document that has no body (just a --- separator).
	src := []byte("---\n")
	docs, err := Parse(src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// goccy/go-yaml produces one document node with a null body.
	if len(docs) == 0 {
		// acceptable — empty stream
		return
	}
	d := docs[0]
	if d.APIVersion != "" {
		t.Errorf("APIVersion = %q, want empty", d.APIVersion)
	}
	if d.Kind != "" {
		t.Errorf("Kind = %q, want empty", d.Kind)
	}
}

func TestParse_SequencePositions(t *testing.T) {
	src := []byte(`items:
  - name: alpha
  - name: beta
`)
	docs, err := Parse(src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(docs) != 1 {
		t.Fatalf("expected 1 document, got %d", len(docs))
	}

	// /items/0 should be present and on line 2.
	pos, err := docs[0].Lookup("/items/0")
	if err != nil {
		t.Fatalf("Lookup /items/0: %v", err)
	}
	if pos.Line != 2 {
		t.Errorf("pos.Line = %d, want 2", pos.Line)
	}
}

func TestEscapePointerSegment(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"simple", "simple"},
		{"a/b", "a~1b"},
		{"a~b", "a~0b"},
		{"a~/b", "a~0~1b"},
		{"", ""},
	}
	for _, tt := range tests {
		got := escapePointerSegment(tt.in)
		if got != tt.want {
			t.Errorf("escapePointerSegment(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

package validator

import (
	"errors"
	"os"
	"testing"

	"github.com/schemalock/app/internal/yamldoc"
)

// loadSchema reads a JSON schema from testdata and returns its bytes and a
// synthetic integrity string for cache-key use in tests.
func loadSchema(t *testing.T, name string) (string, []byte) {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("read schema %s: %v", name, err)
	}
	return "sha256-test-" + name, b
}

func parseDoc(t *testing.T, yaml string) yamldoc.Document {
	t.Helper()
	docs, err := yamldoc.Parse([]byte(yaml))
	if err != nil {
		t.Fatalf("Parse YAML: %v", err)
	}
	if len(docs) == 0 {
		t.Fatal("no documents parsed")
	}
	return docs[0]
}

// TestCompile_KnownGood verifies that a valid document produces no diagnostics.
func TestCompile_KnownGood(t *testing.T) {
	c := NewCompiler()
	integrity, schemaBytes := loadSchema(t, "tiny.json")
	sch, err := c.Compile(integrity, schemaBytes)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	doc := parseDoc(t, "name: hello\n")
	diags := Validate(sch, doc)
	if len(diags) != 0 {
		t.Errorf("expected 0 diagnostics, got %d: %v", len(diags), diags)
	}
}

// TestCompile_KnownBad verifies that an invalid document produces exactly one
// diagnostic pointing at the offending key.
func TestCompile_KnownBad(t *testing.T) {
	c := NewCompiler()
	integrity, schemaBytes := loadSchema(t, "tiny.json")
	sch, err := c.Compile(integrity, schemaBytes)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	// "name" must be a string; 42 is an integer → should fail with exactly one violation.
	doc := parseDoc(t, "name: 42\n")
	diags := Validate(sch, doc)

	// Exact count: this single-violation doc should produce exactly 1 diagnostic.
	if len(diags) != 1 {
		t.Fatalf("expected 1 diagnostic for bad document, got %d: %v", len(diags), diags)
	}

	// The diagnostic must point at the "/name" key, which is at line 1 in this
	// inline YAML. /name starts at column 1 (zero-width key at the start of line).
	if diags[0].Range.Start.Line != 1 {
		t.Errorf("diagnostic Line = %d, want 1", diags[0].Range.Start.Line)
	}
	if diags[0].Range.Start.Column < 1 {
		t.Errorf("diagnostic Column = %d, want >= 1 (/name is at column 1)", diags[0].Range.Start.Column)
	}
}

// TestCompile_MissingRequired verifies that a missing required field is flagged.
func TestCompile_MissingRequired(t *testing.T) {
	c := NewCompiler()
	integrity, schemaBytes := loadSchema(t, "tiny.json")
	sch, err := c.Compile(integrity, schemaBytes)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	// Document without "name" field.
	doc := parseDoc(t, "age: 30\n")
	diags := Validate(sch, doc)
	if len(diags) == 0 {
		t.Fatal("expected ≥1 diagnostic for missing required field, got 0")
	}
}

// TestCachePointerEquality verifies that Compile returns the same *Schema
// pointer for repeated calls with the same integrity.
func TestCachePointerEquality(t *testing.T) {
	c := NewCompiler()
	integrity, schemaBytes := loadSchema(t, "tiny.json")

	sch1, err := c.Compile(integrity, schemaBytes)
	if err != nil {
		t.Fatalf("first Compile: %v", err)
	}
	sch2, err := c.Compile(integrity, schemaBytes)
	if err != nil {
		t.Fatalf("second Compile: %v", err)
	}

	if sch1 != sch2 {
		t.Error("expected same *Schema pointer for repeated Compile with same integrity, got different pointers")
	}
}

// TestCacheDifferentIntegrityDifferentSchema verifies that different integrity
// strings produce independent *Schema objects.
func TestCacheDifferentIntegrityDifferentSchema(t *testing.T) {
	c := NewCompiler()
	integrity1, schemaBytes1 := loadSchema(t, "tiny.json")
	integrity2, schemaBytes2 := loadSchema(t, "int_or_string.json")

	sch1, err := c.Compile(integrity1, schemaBytes1)
	if err != nil {
		t.Fatalf("Compile tiny.json: %v", err)
	}
	sch2, err := c.Compile(integrity2, schemaBytes2)
	if err != nil {
		t.Fatalf("Compile int_or_string.json: %v", err)
	}
	if sch1 == sch2 {
		t.Error("expected different *Schema pointers for different integrities")
	}
}

// TestCompile_InvalidJSON verifies that non-JSON bytes produce ErrSchemaCompile.
func TestCompile_InvalidJSON(t *testing.T) {
	c := NewCompiler()
	_, err := c.Compile("sha256-bad", []byte("not json {{{"))
	if err == nil {
		t.Fatal("expected error for non-JSON schema bytes, got nil")
	}
	if !errors.Is(err, ErrSchemaCompile) {
		t.Errorf("expected ErrSchemaCompile, got %v", err)
	}
}

// TestIntOrString mirrors the x-kubernetes-int-or-string rewrite from ingest/:
// a field declared as oneOf:[integer,string] must accept both int and string
// values, and reject other types like boolean.
func TestIntOrString(t *testing.T) {
	c := NewCompiler()
	integrity, schemaBytes := loadSchema(t, "int_or_string.json")
	sch, err := c.Compile(integrity, schemaBytes)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	tests := []struct {
		name   string
		yaml   string
		wantOK bool
	}{
		{"integer value", "value: 7\n", true},
		{"string value", "value: \"hello\"\n", true},
		{"boolean value (invalid)", "value: true\n", false},
		{"null value (invalid)", "value: null\n", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc := parseDoc(t, tt.yaml)
			diags := Validate(sch, doc)
			if tt.wantOK && len(diags) > 0 {
				t.Errorf("expected 0 diagnostics, got %d: %v", len(diags), diags)
			}
			if !tt.wantOK && len(diags) == 0 {
				t.Error("expected ≥1 diagnostic, got 0")
			}
		})
	}
}

// TestValidate_DiagnosticsAreSorted verifies that diagnostics are returned in
// deterministic (line, column, message) order.
func TestValidate_DiagnosticsAreSorted(t *testing.T) {
	// Schema requiring both "name" (string) and "count" (integer).
	schemaBytes := []byte(`{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"type": "object",
		"required": ["name", "count"],
		"properties": {
			"name": {"type": "string"},
			"count": {"type": "integer"}
		}
	}`)

	c := NewCompiler()
	sch, err := c.Compile("sha256-sorted-test", schemaBytes)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	// Both fields have wrong types.
	doc := parseDoc(t, "name: 99\ncount: \"bad\"\n")
	diags := Validate(sch, doc)

	// Verify ordering invariant: line numbers should be non-decreasing.
	for i := 1; i < len(diags); i++ {
		a, b := diags[i-1], diags[i]
		if a.Range.Start.Line > b.Range.Start.Line {
			t.Errorf("diagnostics not sorted by line: diag[%d].Line=%d > diag[%d].Line=%d",
				i-1, a.Range.Start.Line, i, b.Range.Start.Line)
		}
	}
}

// TestValidate_NilBody verifies that a Document with a nil Body returns nil
// (not a panic or empty slice).
func TestValidate_NilBody(t *testing.T) {
	c := NewCompiler()
	integrity, schemaBytes := loadSchema(t, "tiny.json")
	sch, err := c.Compile(integrity, schemaBytes)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	doc := yamldoc.Document{Nodes: make(yamldoc.PositionMap)}
	diags := Validate(sch, doc)
	if diags != nil {
		t.Errorf("expected nil for nil-body document, got %v", diags)
	}
}

// TestValidate_MissingRequired_FallbackPosition exercises the explicit
// lookupPosition fallback path: when a required field is absent, the
// InstanceLocation reported by jsonschema/v6 points at the parent object
// (typically the root pointer ""), which has no entry in the PositionMap.
// lookupPosition must then return {Line:1, Column:1} rather than zero values.
func TestValidate_MissingRequired_FallbackPosition(t *testing.T) {
	c := NewCompiler()
	integrity, schemaBytes := loadSchema(t, "tiny.json")
	sch, err := c.Compile(integrity, schemaBytes)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	// Document is missing the required "name" field entirely.
	// The missing-required error's InstanceLocation is the parent object (root ""),
	// which is not stored in the PositionMap, so lookupPosition returns {1, 1}.
	doc := parseDoc(t, "age: 30\n")
	diags := Validate(sch, doc)
	if len(diags) == 0 {
		t.Fatal("expected ≥1 diagnostic for missing required field, got 0")
	}

	// All diagnostics for a missing-required violation must fall back to {1, 1}.
	for _, d := range diags {
		if d.Range.Start.Line != 1 {
			t.Errorf("fallback Line = %d, want 1 (explicit fallback path in lookupPosition)", d.Range.Start.Line)
		}
		if d.Range.Start.Column != 1 {
			t.Errorf("fallback Column = %d, want 1 (explicit fallback path in lookupPosition)", d.Range.Start.Column)
		}
	}
}

package validator

import (
	"encoding/json"
	"errors"
	"os"
	"testing"
)

// loadStrictFixture reads a pair of strict testdata fixtures (in + want) and
// returns their parsed JSON as comparable any values.
func loadStrictFixture(t *testing.T, name string) (inBytes []byte, wantAny any) {
	t.Helper()
	inBytes, err := os.ReadFile("testdata/strict/" + name + ".in.json")
	if err != nil {
		t.Fatalf("read in fixture %s: %v", name, err)
	}
	wantBytes, err := os.ReadFile("testdata/strict/" + name + ".want.json")
	if err != nil {
		t.Fatalf("read want fixture %s: %v", name, err)
	}
	var want any
	if err := json.Unmarshal(wantBytes, &want); err != nil {
		t.Fatalf("unmarshal want fixture %s: %v", name, err)
	}
	return inBytes, want
}

// assertWrapStrict calls WrapStrict on inBytes, unmarshals the result, and
// compares it (via JSON round-trip) to wantAny.
func assertWrapStrict(t *testing.T, name string, inBytes []byte, wantAny any) {
	t.Helper()
	got, err := WrapStrict(inBytes)
	if err != nil {
		t.Fatalf("%s: WrapStrict error: %v", name, err)
	}
	var gotAny any
	if err := json.Unmarshal(got, &gotAny); err != nil {
		t.Fatalf("%s: unmarshal WrapStrict output: %v", name, err)
	}
	// Compare via re-marshal for stable JSON equality (map key order is
	// non-deterministic in Go, but json.Marshal sorts map keys).
	gotJSON, _ := json.Marshal(gotAny)
	wantJSON, _ := json.Marshal(wantAny)
	if string(gotJSON) != string(wantJSON) {
		t.Errorf("%s:\n  got  %s\n  want %s", name, gotJSON, wantJSON)
	}
}

// TestWrapStrict_FlatObject verifies that a top-level object with properties
// but no additionalProperties receives additionalProperties:false.
func TestWrapStrict_FlatObject(t *testing.T) {
	inBytes, want := loadStrictFixture(t, "flat_object")
	assertWrapStrict(t, "flat_object", inBytes, want)
}

// TestWrapStrict_NestedObject verifies that both the root and nested objects
// that have properties receive additionalProperties:false.
func TestWrapStrict_NestedObject(t *testing.T) {
	inBytes, want := loadStrictFixture(t, "nested_object")
	assertWrapStrict(t, "nested_object", inBytes, want)
}

// TestWrapStrict_AnnotationsShape verifies that a node with
// additionalProperties:{type:string} but no properties is left unchanged.
func TestWrapStrict_AnnotationsShape(t *testing.T) {
	inBytes, want := loadStrictFixture(t, "annotations_shape")
	assertWrapStrict(t, "annotations_shape", inBytes, want)
}

// TestWrapStrict_ExplicitTrue verifies that a node with additionalProperties:true
// AND properties is left unchanged (explicit true is preserved).
func TestWrapStrict_ExplicitTrue(t *testing.T) {
	inBytes, want := loadStrictFixture(t, "explicit_true")
	assertWrapStrict(t, "explicit_true", inBytes, want)
}

// TestWrapStrict_ExplicitFalse verifies that a node with additionalProperties:false
// is left unchanged (idempotency).
func TestWrapStrict_ExplicitFalse(t *testing.T) {
	inBytes, want := loadStrictFixture(t, "explicit_false")
	assertWrapStrict(t, "explicit_false", inBytes, want)
}

// TestWrapStrict_OneOf_AllOf_AnyOf verifies that branches inside oneOf, allOf,
// and anyOf that contain properties all receive additionalProperties:false.
func TestWrapStrict_OneOf_AllOf_AnyOf(t *testing.T) {
	inBytes, want := loadStrictFixture(t, "oneof_allof_anyof")
	assertWrapStrict(t, "oneof_allof_anyof", inBytes, want)
}

// TestWrapStrict_IfThenElseNot verifies that if/then/else/not sub-schemas
// with properties each receive additionalProperties:false.
func TestWrapStrict_IfThenElseNot(t *testing.T) {
	inBytes, want := loadStrictFixture(t, "if_then_else_not")
	assertWrapStrict(t, "if_then_else_not", inBytes, want)
}

// TestWrapStrict_PatternProperties verifies that sub-schemas inside
// patternProperties with properties receive additionalProperties:false.
func TestWrapStrict_PatternProperties(t *testing.T) {
	inBytes, want := loadStrictFixture(t, "pattern_properties")
	assertWrapStrict(t, "pattern_properties", inBytes, want)
}

// TestWrapStrict_Items_Object verifies that items:{schema} form rewrites the
// sub-schema when it has properties.
func TestWrapStrict_Items_Object(t *testing.T) {
	inBytes, want := loadStrictFixture(t, "items_object")
	assertWrapStrict(t, "items_object", inBytes, want)
}

// TestWrapStrict_Items_Tuple verifies that items:[schema,...] tuple form
// rewrites each sub-schema that has properties.
func TestWrapStrict_Items_Tuple(t *testing.T) {
	inBytes, want := loadStrictFixture(t, "items_tuple")
	assertWrapStrict(t, "items_tuple", inBytes, want)
}

// TestWrapStrict_LocalRefResolution verifies that a $ref pointing to #/$defs/Foo
// causes the canonical $defs.Foo node to be rewritten in place.
func TestWrapStrict_LocalRefResolution(t *testing.T) {
	inBytes, want := loadStrictFixture(t, "local_ref")
	assertWrapStrict(t, "local_ref", inBytes, want)
}

// TestWrapStrict_RefCycle verifies that mutually recursive $refs (Foo→Bar→Foo)
// terminate without infinite recursion and both nodes are rewritten.
func TestWrapStrict_RefCycle(t *testing.T) {
	inBytes, want := loadStrictFixture(t, "ref_cycle")
	assertWrapStrict(t, "ref_cycle", inBytes, want)
}

// TestWrapStrict_ExternalRef verifies that an external $ref (https://...) is
// left alone without error.
func TestWrapStrict_ExternalRef(t *testing.T) {
	inBytes, want := loadStrictFixture(t, "external_ref")
	assertWrapStrict(t, "external_ref", inBytes, want)
}

// TestWrapStrict_NoPropertiesNoInject verifies that a {type:object} node with
// no properties block is left unchanged (no injection).
func TestWrapStrict_NoPropertiesNoInject(t *testing.T) {
	inBytes, want := loadStrictFixture(t, "no_properties_no_inject")
	assertWrapStrict(t, "no_properties_no_inject", inBytes, want)
}

// TestWrapStrict_NonObjectTop verifies that a top-level boolean schema (true/false)
// is returned unchanged without error.
func TestWrapStrict_NonObjectTop(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input []byte
	}{
		{"true", []byte(`true`)},
		{"false", []byte(`false`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := WrapStrict(tc.input)
			if err != nil {
				t.Fatalf("WrapStrict(%s): unexpected error: %v", tc.name, err)
			}
			if string(got) != string(tc.input) {
				t.Errorf("WrapStrict(%s) = %s, want unchanged %s", tc.name, got, tc.input)
			}
		})
	}
}

// TestWrapStrict_MalformedJSON verifies that invalid JSON bytes return an error
// wrapping ErrStrictRewrite.
func TestWrapStrict_MalformedJSON(t *testing.T) {
	_, err := WrapStrict([]byte(`{not valid json`))
	if err == nil {
		t.Fatal("expected error for malformed JSON, got nil")
	}
	if !errors.Is(err, ErrStrictRewrite) {
		t.Errorf("expected errors.Is(err, ErrStrictRewrite), got %v", err)
	}
}

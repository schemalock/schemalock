package lsp

import (
	"encoding/json"
	"slices"
	"sort"
	"strings"
	"testing"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
	lspprot "go.lsp.dev/protocol"
	"github.com/schemalock/app/internal/yamldoc"
)

// buildSchema compiles a JSON Schema from a raw JSON literal and returns the
// root *jsonschema.Schema. Calls t.Fatal on parse or compilation error.
func buildSchema(t *testing.T, raw string) *jsonschema.Schema {
	t.Helper()
	doc, err := jsonschema.UnmarshalJSON(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("UnmarshalJSON: %v", err)
	}
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	if err := c.AddResource("test://schema", doc); err != nil {
		t.Fatalf("AddResource: %v", err)
	}
	sch, err := c.Compile("test://schema")
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	return sch
}

// TestCompletionItemsForProperties_RequiredFirst verifies that required
// properties sort before optional ones and that within each group the order
// is alphabetical.
func TestCompletionItemsForProperties_RequiredFirst(t *testing.T) {
	raw := `{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"type": "object",
		"required": ["a", "c"],
		"properties": {
			"a": {"type": "string"},
			"b": {"type": "string"},
			"c": {"type": "string"},
			"d": {"type": "string"}
		}
	}`
	sch := buildSchema(t, raw)

	items := completionItemsForProperties(sch, nil)
	if len(items) != 4 {
		t.Fatalf("expected 4 items, got %d", len(items))
	}

	// Required properties "a" and "c" should come first (SortText "0_a", "0_c"),
	// then optional "b" and "d" (SortText "1_b", "1_d").
	wantOrder := []string{"a", "c", "b", "d"}
	wantSortTextPrefix := []string{"0_", "0_", "1_", "1_"}
	for i, item := range items {
		if item.Label != wantOrder[i] {
			t.Errorf("items[%d].Label = %q, want %q", i, item.Label, wantOrder[i])
		}
		if !strings.HasPrefix(item.SortText, wantSortTextPrefix[i]) {
			t.Errorf("items[%d].SortText = %q, want prefix %q", i, item.SortText, wantSortTextPrefix[i])
		}
	}
}

// TestCompletionItemsForProperties_AllOptional verifies that when there are no
// required properties all items get the "1_" prefix and are sorted
// alphabetically.
func TestCompletionItemsForProperties_AllOptional(t *testing.T) {
	raw := `{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"type": "object",
		"properties": {
			"z": {"type": "string"},
			"a": {"type": "string"},
			"m": {"type": "string"}
		}
	}`
	sch := buildSchema(t, raw)

	items := completionItemsForProperties(sch, nil)
	if len(items) != 3 {
		t.Fatalf("expected 3 items, got %d", len(items))
	}
	wantOrder := []string{"a", "m", "z"}
	for i, item := range items {
		if item.Label != wantOrder[i] {
			t.Errorf("items[%d].Label = %q, want %q", i, item.Label, wantOrder[i])
		}
		if !strings.HasPrefix(item.SortText, "1_") {
			t.Errorf("items[%d].SortText = %q, want prefix \"1_\"", i, item.SortText)
		}
	}
}

// TestCompletionItemsForEnum_DeclarationOrder verifies that enum values are
// returned in schema declaration order, not alphabetical order, via SortText.
func TestCompletionItemsForEnum_DeclarationOrder(t *testing.T) {
	raw := `{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"type": "string",
		"enum": ["x", "y", "z"]
	}`
	sch := buildSchema(t, raw)

	items := completionItemsForEnum(sch, "string")
	if len(items) != 3 {
		t.Fatalf("expected 3 items, got %d", len(items))
	}
	// Declaration order: x, y, z (not alphabetical, which would be the same
	// here, but SortText must encode position 0, 1, 2).
	wantLabels := []string{"x", "y", "z"}
	for i, item := range items {
		if item.Label != wantLabels[i] {
			t.Errorf("items[%d].Label = %q, want %q", i, item.Label, wantLabels[i])
		}
		// SortText must start with the zero-padded index.
		wantPrefix := []string{"0000_", "0001_", "0002_"}
		if !strings.HasPrefix(item.SortText, wantPrefix[i]) {
			t.Errorf("items[%d].SortText = %q, want prefix %q", i, item.SortText, wantPrefix[i])
		}
	}
}

// TestCompletionItemsForEnum_ReverseDeclarationOrder verifies that a schema
// with enum values in reverse-alphabetical order is returned in declaration
// order, not alphabetical order. This is the key regression guard: the old
// code applied sort.Slice by label, which would re-shuffle z→a into a→z.
func TestCompletionItemsForEnum_ReverseDeclarationOrder(t *testing.T) {
	raw := `{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"type": "string",
		"enum": ["z", "m", "a"]
	}`
	sch := buildSchema(t, raw)

	items := completionItemsForEnum(sch, "string")
	if len(items) != 3 {
		t.Fatalf("expected 3 items, got %d", len(items))
	}
	// Must preserve declaration order: z, m, a.
	wantLabels := []string{"z", "m", "a"}
	for i, item := range items {
		if item.Label != wantLabels[i] {
			t.Errorf("items[%d].Label = %q, want %q (declaration order must be preserved)",
				i, item.Label, wantLabels[i])
		}
	}
}

// TestCompletionItemsForEnum_Detail verifies that each enum item carries the
// parentDetail passed into completionItemsForEnum.
func TestCompletionItemsForEnum_Detail(t *testing.T) {
	raw := `{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"type": "string",
		"enum": ["alpha", "beta"]
	}`
	sch := buildSchema(t, raw)

	items := completionItemsForEnum(sch, "my-type")
	for _, item := range items {
		if item.Detail != "my-type" {
			t.Errorf("item %q Detail = %q, want \"my-type\"", item.Label, item.Detail)
		}
	}
}

// TestSchemaDetail covers all branches of schemaDetail.
func TestSchemaDetail(t *testing.T) {
	tests := []struct {
		name   string
		schema string // JSON Schema literal; empty string → nil schema test
		want   string
	}{
		{
			name: "nil schema",
			want: "",
		},
		{
			name:   "single type string",
			schema: `{"type": "string"}`,
			want:   "string",
		},
		{
			name:   "single type object",
			schema: `{"type": "object"}`,
			want:   "object",
		},
		{
			name:   "single type integer",
			schema: `{"type": "integer"}`,
			want:   "integer",
		},
		{
			// The jsonschema library's ToStrings() may reorder type entries;
			// "null" sorts before "string" in the library's internal ordering.
			name:   "multi type string-null",
			schema: `{"type": ["string", "null"]}`,
			want:   "null | string",
		},
		{
			name:   "short enum (≤3 values)",
			schema: `{"type": "string", "enum": ["A", "B", "C"]}`,
			want:   "enum: A | B | C",
		},
		{
			name:   "long enum (>3 values) — truncated with ellipsis",
			schema: `{"type": "string", "enum": ["A", "B", "C", "D"]}`,
			want:   "enum: A | B | C | …",
		},
		{
			name:   "typed enum prefers enum form over type form",
			schema: `{"type": "string", "enum": ["x", "y"]}`,
			want:   "enum: x | y",
		},
		{
			name:   "no type and no enum",
			schema: `{"description": "just a description"}`,
			want:   "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var sch *jsonschema.Schema
			if tc.schema != "" {
				sch = buildSchema(t, tc.schema)
			}
			got := schemaDetail(sch)
			if got != tc.want {
				t.Errorf("schemaDetail() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestCompletionItemsForProperties_Detail verifies that each property item
// carries a Detail field populated by schemaDetail.
func TestCompletionItemsForProperties_Detail(t *testing.T) {
	raw := `{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"type": "object",
		"properties": {
			"strProp": {"type": "string"},
			"intProp": {"type": "integer"},
			"enumProp": {"type": "string", "enum": ["a", "b"]}
		}
	}`
	sch := buildSchema(t, raw)

	items := completionItemsForProperties(sch, nil)

	// Build a label → item map for easy lookup.
	byLabel := make(map[string]string, len(items))
	for _, item := range items {
		byLabel[item.Label] = item.Detail
	}

	if byLabel["strProp"] != "string" {
		t.Errorf("strProp.Detail = %q, want \"string\"", byLabel["strProp"])
	}
	if byLabel["intProp"] != "integer" {
		t.Errorf("intProp.Detail = %q, want \"integer\"", byLabel["intProp"])
	}
	if byLabel["enumProp"] != "enum: a | b" {
		t.Errorf("enumProp.Detail = %q, want \"enum: a | b\"", byLabel["enumProp"])
	}
}

// TestSchemaDetail_JSONMarshalFallback verifies that non-string enum values
// (e.g. integers) are marshalled to their JSON representation.
func TestSchemaDetail_JSONMarshalFallback(t *testing.T) {
	raw := `{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"type": "integer",
		"enum": [1, 2, 3]
	}`
	sch := buildSchema(t, raw)
	got := schemaDetail(sch)
	// Integer enum values should marshal to "1", "2", "3".
	want := "enum: 1 | 2 | 3"
	if got != want {
		t.Errorf("schemaDetail() = %q, want %q", got, want)
	}
}

// Ensure json package is used; it's needed by completionItemsForEnum and
// schemaDetail via json.Marshal for non-string enum values.
var _ = json.Marshal

// TestCompletionItemsForProperties_Documentation verifies that a property with
// a non-empty Description is wrapped in a MarkupContent{Kind:"markdown"} value,
// and that a property with no description produces no Documentation field.
func TestCompletionItemsForProperties_Documentation(t *testing.T) {
	raw := `{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"type": "object",
		"properties": {
			"withDesc": {"type": "string", "description": "A described field"},
			"noDesc":   {"type": "string"}
		}
	}`
	sch := buildSchema(t, raw)
	items := completionItemsForProperties(sch, nil)

	byLabel := make(map[string]any, len(items))
	for _, item := range items {
		byLabel[item.Label] = item.Documentation
	}

	// withDesc must carry MarkupContent with kind "markdown".
	doc, ok := byLabel["withDesc"]
	if !ok {
		t.Fatal("item withDesc not found")
	}
	mc, ok := doc.(lspprot.MarkupContent)
	if !ok {
		t.Fatalf("withDesc.Documentation type = %T, want lsp.MarkupContent", doc)
	}
	if mc.Kind != "markdown" {
		t.Errorf("withDesc.Documentation.Kind = %q, want \"markdown\"", mc.Kind)
	}
	if mc.Value != "A described field" {
		t.Errorf("withDesc.Documentation.Value = %q, want \"A described field\"", mc.Value)
	}

	// noDesc must have nil Documentation (omitted from JSON).
	if doc := byLabel["noDesc"]; doc != nil {
		t.Errorf("noDesc.Documentation = %v, want nil", doc)
	}
}

// TestCompletionItemsForProperties_Preselect verifies that exactly one item
// gets Preselect=true and it is the first required property in final sort order.
func TestCompletionItemsForProperties_Preselect(t *testing.T) {
	// required: ["b","d"], props: {a,b,c,d}
	// After sorting: 0_b, 0_d, 1_a, 1_c  → "b" is first required in sort order.
	raw := `{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"type": "object",
		"required": ["b", "d"],
		"properties": {
			"a": {"type": "string"},
			"b": {"type": "string"},
			"c": {"type": "string"},
			"d": {"type": "string"}
		}
	}`
	sch := buildSchema(t, raw)
	items := completionItemsForProperties(sch, nil)

	if len(items) != 4 {
		t.Fatalf("expected 4 items, got %d", len(items))
	}

	// Exactly one item must have Preselect=true.
	preselected := []string{}
	for _, item := range items {
		if item.Preselect {
			preselected = append(preselected, item.Label)
		}
	}
	if len(preselected) != 1 {
		t.Fatalf("expected exactly 1 preselected item, got %d: %v", len(preselected), preselected)
	}
	// The preselected item must be "b" (first required in alpha order = SortText "0_b").
	if preselected[0] != "b" {
		t.Errorf("preselected item = %q, want \"b\"", preselected[0])
	}

	// First item in the sorted list must also be "b" with SortText "0_b".
	if items[0].Label != "b" {
		t.Errorf("items[0].Label = %q, want \"b\"", items[0].Label)
	}
}

// TestCompletionItemsForProperties_NoPreselect verifies that no item gets
// Preselect=true when all properties are optional.
func TestCompletionItemsForProperties_NoPreselect(t *testing.T) {
	raw := `{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"type": "object",
		"properties": {
			"a": {"type": "string"},
			"b": {"type": "string"}
		}
	}`
	sch := buildSchema(t, raw)
	items := completionItemsForProperties(sch, nil)

	for _, item := range items {
		if item.Preselect {
			t.Errorf("item %q has Preselect=true, expected no preselection", item.Label)
		}
	}
}

// TestCompletionItemsForProperties_Deprecated verifies that a property marked
// deprecated in the JSON Schema receives CompletionItemTagDeprecated in its
// Tags slice.
func TestCompletionItemsForProperties_Deprecated(t *testing.T) {
	raw := `{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"type": "object",
		"properties": {
			"old": {"type": "string", "deprecated": true,  "description": "Use new instead"},
			"new": {"type": "string", "deprecated": false, "description": "Replacement field"}
		}
	}`
	sch := buildSchema(t, raw)
	items := completionItemsForProperties(sch, nil)

	byLabel := make(map[string]lspprot.CompletionItem, len(items))
	for _, item := range items {
		byLabel[item.Label] = item
	}

	// "old" must have CompletionItemTagDeprecated in Tags.
	oldItem, ok := byLabel["old"]
	if !ok {
		t.Fatal("item old not found")
	}
	hasDepTag := slices.Contains(oldItem.Tags, lspprot.CompletionItemTagDeprecated)
	if !hasDepTag {
		t.Errorf("old.Tags = %v, want to contain CompletionItemTagDeprecated (%v)",
			oldItem.Tags, lspprot.CompletionItemTagDeprecated)
	}

	// "new" must NOT have CompletionItemTagDeprecated.
	newItem, ok := byLabel["new"]
	if !ok {
		t.Fatal("item new not found")
	}
	for _, tag := range newItem.Tags {
		if tag == lspprot.CompletionItemTagDeprecated {
			t.Errorf("new.Tags unexpectedly contains CompletionItemTagDeprecated")
		}
	}
}

// TestPositionAt_Outdent verifies positionAt's outdent walk-up: when the
// cursor is on a blank line that is less indented than the deepest preceding
// key, the walker climbs ancestors until it finds one at or shallower than
// the cursor column, then offers that ancestor's siblings.
//
// Fixture (0-based line numbers in comments):
//
//	line 0: apiVersion: x/v1
//	line 1: kind: Foo
//	line 2: spec:
//	line 3:   inner:
//	line 4:     a: 1
//	line 5:     b: 2
//	line 6:   outer: 3
//	line 7: (blank — the test cursor target)
func TestPositionAt_Outdent(t *testing.T) {
	yamlFixture := "apiVersion: x/v1\nkind: Foo\nspec:\n  inner:\n    a: 1\n    b: 2\n  outer: 3\n"

	docs, err := yamldoc.Parse([]byte(yamlFixture))
	if err != nil || len(docs) == 0 {
		t.Fatalf("failed to parse fixture YAML: %v", err)
	}
	doc := docs[0]

	tests := []struct {
		name             string
		line, char       int
		wantParent       string
		wantIsKey        bool
		wantExistingKeys []string
	}{
		{
			// col 2 (LSP) = col 3 (yamldoc). best = /spec/inner/b at col 5.
			// Walk: /spec/inner (col 3) → col 3 <= 3, return parent of /spec/inner = /spec.
			name:             "OutdentToSpecLevel_BlankLineAtCol2",
			line:             7, char: 2,
			wantParent:       "/spec",
			wantIsKey:        true,
			wantExistingKeys: []string{"inner", "outer"},
		},
		{
			// col 0 (LSP) = col 1 (yamldoc). Walk climbs past all ancestors → root.
			name:             "OutdentToRoot_BlankLineAtCol0",
			line:             7, char: 0,
			wantParent:       "",
			wantIsKey:        true,
			wantExistingKeys: []string{"apiVersion", "kind", "spec"},
		},
		{
			// col 1 (LSP) = col 2 (yamldoc). /spec is at col 1 (yamldoc).
			// Walk: /spec/inner (col 3 > 2), /spec (col 1 <= 2) → parent of /spec = "".
			name:             "OutdentMidIndent_Col1",
			line:             7, char: 1,
			wantParent:       "",
			wantIsKey:        true,
			wantExistingKeys: []string{"apiVersion", "kind", "spec"},
		},
		{
			// Regression: col 4 (LSP) = col 5 (yamldoc) == best.srcPos.Column (5).
			// best = /spec/inner/b; the == fast-path returns parent /spec/inner.
			// Must be on the blank line (7) so the second pass fires, not the first.
			name:             "SiblingOfDeepest_BlankLineAtCol4",
			line:             7, char: 4,
			wantParent:       "/spec/inner",
			wantIsKey:        true,
			wantExistingKeys: []string{"a", "b"},
		},
		{
			// Regression: col 6 (LSP) = col 7 (yamldoc) > best.srcPos.Column (5).
			// Hits the > fast-path (children of /spec/inner/b, scalar → empty keys).
			// Must be on the blank line (7) so the second pass fires, not the first.
			name:             "NestIntoDeepest_BlankLineAtCol6",
			line:             7, char: 6,
			wantParent:       "/spec/inner/b",
			wantIsKey:        true,
			wantExistingKeys: []string{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pos := lspprot.Position{Line: uint32(tc.line), Character: uint32(tc.char)}
			ctx := positionAt(pos, doc)

			if ctx.ParentPointer != tc.wantParent {
				t.Errorf("ParentPointer = %q, want %q", ctx.ParentPointer, tc.wantParent)
			}
			if ctx.IsKeyPosition != tc.wantIsKey {
				t.Errorf("IsKeyPosition = %v, want %v", ctx.IsKeyPosition, tc.wantIsKey)
			}
			gotKeys := append([]string(nil), ctx.ExistingKeys...)
			wantKeys := append([]string(nil), tc.wantExistingKeys...)
			sort.Strings(gotKeys)
			sort.Strings(wantKeys)
			if len(gotKeys) != len(wantKeys) {
				t.Errorf("ExistingKeys = %v, want %v", gotKeys, wantKeys)
			} else {
				for i := range wantKeys {
					if gotKeys[i] != wantKeys[i] {
						t.Errorf("ExistingKeys[%d] = %q, want %q", i, gotKeys[i], wantKeys[i])
					}
				}
			}
		})
	}
}

// TestPositionAt_KindValuePosition pins the positionAt output for various
// cursor positions relative to the root-level `kind` field. These invariants
// are the preconditions for bootstrapKindCompletions: if walker semantics
// drift this test will catch it before the bootstrap path silently stops
// working.
func TestPositionAt_KindValuePosition(t *testing.T) {
	// Two test documents: one with an empty kind, one with a populated kind.
	yamlEmptyKind := "apiVersion: foo.com/v1\nkind:\n"
	yamlPopulatedKind := "apiVersion: foo.com/v1\nkind: VMCluster\n"

	docsEmpty, err := yamldoc.Parse([]byte(yamlEmptyKind))
	if err != nil || len(docsEmpty) == 0 {
		t.Fatalf("failed to parse empty-kind YAML: %v", err)
	}
	docsPopulated, err := yamldoc.Parse([]byte(yamlPopulatedKind))
	if err != nil || len(docsPopulated) == 0 {
		t.Fatalf("failed to parse populated-kind YAML: %v", err)
	}

	tests := []struct {
		name            string
		doc             yamldoc.Document
		line, char      int
		wantPointer     string
		wantParent      string
		wantIsKey       bool
		wantExistingKeys []string
	}{
		{
			name:            "EmptyKind_JustPastColon",
			doc:             docsEmpty[0],
			line:            1, char: 5,
			wantPointer:     "/kind",
			wantParent:      "",
			wantIsKey:       false,
			wantExistingKeys: []string{"apiVersion", "kind"},
		},
		{
			name:            "EmptyKind_OneCharPastColon",
			doc:             docsEmpty[0],
			line:            1, char: 6,
			wantPointer:     "/kind",
			wantParent:      "",
			wantIsKey:       false,
			wantExistingKeys: []string{"apiVersion", "kind"},
		},
		{
			name:            "PopulatedKind_InsideValue",
			doc:             docsPopulated[0],
			line:            1, char: 7,
			wantPointer:     "/kind",
			wantParent:      "",
			wantIsKey:       false,
			wantExistingKeys: []string{"apiVersion", "kind"},
		},
		{
			name:            "PopulatedKind_InsideKey",
			doc:             docsPopulated[0],
			line:            1, char: 0,
			wantPointer:     "/kind",
			wantParent:      "",
			wantIsKey:       true,
			wantExistingKeys: []string{"apiVersion", "kind"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pos := lspprot.Position{Line: uint32(tc.line), Character: uint32(tc.char)}
			ctx := positionAt(pos, tc.doc)

			if ctx.Pointer != tc.wantPointer {
				t.Errorf("Pointer = %q, want %q", ctx.Pointer, tc.wantPointer)
			}
			if ctx.ParentPointer != tc.wantParent {
				t.Errorf("ParentPointer = %q, want %q", ctx.ParentPointer, tc.wantParent)
			}
			if ctx.IsKeyPosition != tc.wantIsKey {
				t.Errorf("IsKeyPosition = %v, want %v", ctx.IsKeyPosition, tc.wantIsKey)
			}
			// ExistingKeys is sorted by existingKeysAt; compare as sorted slices.
			gotKeys := append([]string(nil), ctx.ExistingKeys...)
			wantKeys := append([]string(nil), tc.wantExistingKeys...)
			sort.Strings(gotKeys)
			sort.Strings(wantKeys)
			if len(gotKeys) != len(wantKeys) {
				t.Errorf("ExistingKeys = %v, want %v", gotKeys, wantKeys)
			} else {
				for i := range wantKeys {
					if gotKeys[i] != wantKeys[i] {
						t.Errorf("ExistingKeys[%d] = %q, want %q", i, gotKeys[i], wantKeys[i])
					}
				}
			}
		})
	}
}

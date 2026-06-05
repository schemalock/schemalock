package lsp

import (
	"encoding/json"
	"slices"
	"sort"
	"strings"
	"testing"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/schemalock/app/internal/yamldoc"
	lspprot "go.lsp.dev/protocol"
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

	items := completionItemsForProperties(sch, nil, lspprot.Range{})
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

	items := completionItemsForProperties(sch, nil, lspprot.Range{})
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

	items := completionItemsForEnum(sch, "string", lspprot.Range{})
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

	items := completionItemsForEnum(sch, "string", lspprot.Range{})
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

	items := completionItemsForEnum(sch, "my-type", lspprot.Range{})
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

	items := completionItemsForProperties(sch, nil, lspprot.Range{})

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
	items := completionItemsForProperties(sch, nil, lspprot.Range{})

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
	items := completionItemsForProperties(sch, nil, lspprot.Range{})

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
	items := completionItemsForProperties(sch, nil, lspprot.Range{})

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
	items := completionItemsForProperties(sch, nil, lspprot.Range{})

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

// TestPositionAt_Outdent verifies positionAt's indent-stack classification:
// when the cursor is on a blank line, the walker pops keys whose column is
// >= an incoming key's column (the YAML scope-close rule), then picks the
// deepest still-open key with column <= cursor column to determine whether
// the cursor is a sibling of (col ==) or nested into (col <) that key.
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
			// col 2 (LSP) = col 3 (yamldoc). Indent stack at line 7:
			// [/spec(1), /spec/outer(3)] — outer(col 3 on line 6) closed inner.
			// Deepest stack entry with col <= 3: /spec/outer at col 3.
			// col 3 == 3 → sibling of /spec/outer. Parent = /spec.
			name: "OutdentToSpecLevel_BlankLineAtCol2",
			line: 7, char: 2,
			wantParent:       "/spec",
			wantIsKey:        true,
			wantExistingKeys: []string{"inner", "outer"},
		},
		{
			// col 0 (LSP) = col 1 (yamldoc). Indent stack at line 7:
			// [/spec(1), /spec/outer(3)]. Deepest entry with col <= 1: /spec at col 1.
			// col 1 == 1 → sibling of /spec. Parent = "" (root completion).
			name: "OutdentToRoot_BlankLineAtCol0",
			line: 7, char: 0,
			wantParent:       "",
			wantIsKey:        true,
			wantExistingKeys: []string{"apiVersion", "kind", "spec"},
		},
		{
			// col 1 (LSP) = col 2 (yamldoc).
			// Indent stack at line 7: [/spec(1), /spec/outer(3)].
			// outer(col 3 at line 6) closed inner and its children — the stack never
			// sees /spec/inner after /spec/outer is pushed.
			// Deepest stack entry with col <= 2: /spec at col 1. col 1 < 2 → nest into
			// /spec. ParentPointer = "/spec", ExistingKeys = ["inner","outer"].
			name: "OutdentMidIndent_Col1",
			line: 7, char: 1,
			wantParent:       "/spec",
			wantIsKey:        true,
			wantExistingKeys: []string{"inner", "outer"},
		},
		{
			// col 4 (LSP) = col 5 (yamldoc).
			// Indent stack at line 7: [/spec(1), /spec/outer(3)].
			// /spec/outer at col 3 (line 6) closed /spec/inner and its children —
			// the old expected value (/spec/inner / ["a","b"]) pinned the bug.
			// Deepest stack entry with col <= 5: /spec/outer at col 3. col 3 < 5
			// → nest into /spec/outer. /spec/outer is a scalar ("3"), so ExistingKeys = [].
			name: "SiblingOfDeepest_BlankLineAtCol4",
			line: 7, char: 4,
			wantParent:       "/spec/outer",
			wantIsKey:        true,
			wantExistingKeys: []string{},
		},
		{
			// col 6 (LSP) = col 7 (yamldoc).
			// Indent stack at line 7: [/spec(1), /spec/outer(3)].
			// /spec/outer at col 3 (line 6) closed /spec/inner and its children —
			// the old expected value (/spec/inner/b) pinned the bug.
			// Deepest stack entry with col <= 7: /spec/outer at col 3. col 3 < 7
			// → nest into /spec/outer. /spec/outer is a scalar ("3"), so ExistingKeys = [].
			name: "NestIntoDeepest_BlankLineAtCol6",
			line: 7, char: 6,
			wantParent:       "/spec/outer",
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

// TestPositionAt_ClosingSiblings is the regression test for the closing-siblings
// bug: when a later sibling key at a shallower column closes an ancestor's scope,
// positionAt must not treat that ancestor as still-open. This exercises the
// indent-stack algorithm that replaced the old pointer-ancestry walk.
//
// Fixture (0-based line numbers, 1-based yamldoc columns):
//
//	line 0: spec:                   col 1
//	line 1:   outer:                col 3
//	line 2:     arr:                col 5
//	line 3:       - g: 1            arr-item col 7, g col 9
//	line 4:         k: 2            k col 9
//	line 5:     after: 3            after col 5  (closes arr and its children)
//	line 6:   next:                 next col 3   (closes outer and its children)
//	line 7: (blank — the test cursor target)
//
// Stack at line 7: [/spec(1), /spec/next(3)].
func TestPositionAt_ClosingSiblings(t *testing.T) {
	yamlFixture := "spec:\n  outer:\n    arr:\n      - g: 1\n        k: 2\n    after: 3\n  next:\n"

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
			// col 4 (LSP) = col 5 (yamldoc).
			// Stack: [/spec(1), /spec/next(3)].
			// Deepest entry with col <= 5: /spec/next at col 3. col 3 < 5 → nest into
			// /spec/next. Without the fix, the old walk would find /spec/outer/after
			// at col 5 as the deepest key and return /spec/outer (closed by /spec/next).
			name: "NestIntoNext_NotIntoClosedAfter",
			line: 7, char: 4,
			wantParent:       "/spec/next",
			wantIsKey:        true,
			wantExistingKeys: []string{},
		},
		{
			// col 2 (LSP) = col 3 (yamldoc).
			// Stack: [/spec(1), /spec/next(3)].
			// Deepest entry with col <= 3: /spec/next at col 3. col 3 == 3 → sibling
			// of /spec/next. Parent = /spec. ExistingKeys at /spec = ["next","outer"].
			name: "SiblingOfNext_NotInsideClosedOuter",
			line: 7, char: 2,
			wantParent:       "/spec",
			wantIsKey:        true,
			wantExistingKeys: []string{"next", "outer"},
		},
		{
			// col 0 (LSP) = col 1 (yamldoc).
			// Stack: [/spec(1), /spec/next(3)].
			// Deepest entry with col <= 1: /spec at col 1. col 1 == 1 → sibling of
			// /spec. Parent = "". ExistingKeys at root = ["spec"].
			name: "OutdentToRoot",
			line: 7, char: 0,
			wantParent:       "",
			wantIsKey:        true,
			wantExistingKeys: []string{"spec"},
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

// TestWordRangeAt covers all rows of the wordRangeAt behaviour spec from the
// plan. Word characters are [A-Za-z0-9_-].
func TestWordRangeAt(t *testing.T) {
	tests := []struct {
		name    string
		text    string
		line    uint32
		char    uint32
		wantSL  uint32 // want start line
		wantSC  uint32 // want start char
		wantEL  uint32 // want end line
		wantEC  uint32 // want end char
	}{
		{
			name:   "EmptyLine_char0",
			text:   "",
			line:   0, char: 0,
			wantSL: 0, wantSC: 0, wantEL: 0, wantEC: 0,
		},
		{
			name:   "WhitespaceAtStartOfIndentedLine",
			text:   "apiVersion: x\n  replicas: 1\n",
			line:   1, char: 0,
			wantSL: 1, wantSC: 0, wantEL: 1, wantEC: 0,
		},
		{
			name:   "InsideWord_replicas_char3",
			text:   "apiVersion: x\n  replicas: 1\n",
			line:   1, char: 5,
			wantSL: 1, wantSC: 2, wantEL: 1, wantEC: 10,
		},
		{
			name:   "EndOfWord_replicas",
			text:   "apiVersion: x\n  replicas: 1\n",
			line:   1, char: 10,
			wantSL: 1, wantSC: 2, wantEL: 1, wantEC: 10,
		},
		{
			name:   "StartOfWord_replicas",
			text:   "apiVersion: x\n  replicas: 1\n",
			line:   1, char: 2,
			wantSL: 1, wantSC: 2, wantEL: 1, wantEC: 10,
		},
		{
			name:   "AfterFieldColon_whitespace",
			text:   "  replicas: ",
			line:   0, char: 11,
			wantSL: 0, wantSC: 11, wantEL: 0, wantEC: 11,
		},
		{
			name:   "InsideEnumValue_disabled",
			text:   "  mode: disabled",
			line:   0, char: 10,
			wantSL: 0, wantSC: 8, wantEL: 0, wantEC: 16,
		},
		{
			name:   "PastPunctuation_colon",
			text:   "spec:",
			line:   0, char: 4,
			wantSL: 0, wantSC: 0, wantEL: 0, wantEC: 4,
		},
		{
			name:   "LineOutOfBounds",
			text:   "line0\n",
			line:   5, char: 0,
			wantSL: 5, wantSC: 0, wantEL: 5, wantEC: 0,
		},
		{
			name:   "CharPastLineLength",
			text:   "abc",
			line:   0, char: 100,
			wantSL: 0, wantSC: 0, wantEL: 0, wantEC: 3,
		},
		{
			name:   "WordWithUnderscoreAndDash",
			text:   "  my_field-name: 1",
			line:   0, char: 5,
			wantSL: 0, wantSC: 2, wantEL: 0, wantEC: 15,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pos := lspprot.Position{Line: tc.line, Character: tc.char}
			got := wordRangeAt(tc.text, pos)
			if got.Start.Line != tc.wantSL || got.Start.Character != tc.wantSC {
				t.Errorf("Start = {%d,%d}, want {%d,%d}",
					got.Start.Line, got.Start.Character, tc.wantSL, tc.wantSC)
			}
			if got.End.Line != tc.wantEL || got.End.Character != tc.wantEC {
				t.Errorf("End = {%d,%d}, want {%d,%d}",
					got.End.Line, got.End.Character, tc.wantEL, tc.wantEC)
			}
		})
	}
}

// --------------------------------------------------------------------------
// Task 3: completionItemsForProperties — effectiveProperties parity
// --------------------------------------------------------------------------

// TestCompletionItemsForProperties_ViaRef verifies that completion items are
// built from properties reached through a $ref, not only direct Properties.
func TestCompletionItemsForProperties_ViaRef(t *testing.T) {
	raw := `{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"$defs": {
			"Inner": {
				"type": "object",
				"properties": {
					"alpha": {"type": "string"},
					"beta":  {"type": "integer"}
				}
			}
		},
		"type": "object",
		"properties": {
			"spec": {"$ref": "#/$defs/Inner"}
		}
	}`
	root := buildSchema(t, raw)
	// Get the spec sub-schema (which is a $ref to Inner).
	specSch := schemaAtPointer(root, "/spec")
	if specSch == nil {
		t.Fatal("could not navigate to /spec")
	}

	items := completionItemsForProperties(specSch, nil, lspprot.Range{})
	labels := make(map[string]bool, len(items))
	for _, it := range items {
		labels[it.Label] = true
	}
	if !labels["alpha"] {
		t.Errorf("completionItemsForProperties via $ref missing 'alpha'; got: %v", labels)
	}
	if !labels["beta"] {
		t.Errorf("completionItemsForProperties via $ref missing 'beta'; got: %v", labels)
	}
}

// TestCompletionItemsForProperties_ViaAllOf verifies that completion items
// include properties from allOf sub-schemas.
func TestCompletionItemsForProperties_ViaAllOf(t *testing.T) {
	raw := `{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"type": "object",
		"allOf": [
			{"properties": {"base": {"type": "string"}}},
			{"properties": {"extra": {"type": "integer"}}}
		]
	}`
	sch := buildSchema(t, raw)

	items := completionItemsForProperties(sch, nil, lspprot.Range{})
	labels := make(map[string]bool, len(items))
	for _, it := range items {
		labels[it.Label] = true
	}
	if !labels["base"] {
		t.Errorf("completionItemsForProperties via allOf missing 'base'; got: %v", labels)
	}
	if !labels["extra"] {
		t.Errorf("completionItemsForProperties via allOf missing 'extra'; got: %v", labels)
	}
}

// --------------------------------------------------------------------------
// Task 1: schemaAtPointer — array segment navigation
// --------------------------------------------------------------------------

// TestSchemaAtPointer_ArrayItems2020Object verifies that a numeric pointer
// segment resolves through items (2020-12 Items keyword) when the parent schema
// is an array whose items describe an object with named properties.
func TestSchemaAtPointer_ArrayItems2020Object(t *testing.T) {
	raw := `{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"type": "array",
		"items": {"type": "object", "properties": {"name": {"type": "string"}}}
	}`
	sch := buildSchema(t, raw)

	// /0/name should resolve to the string schema for "name".
	got := schemaAtPointer(sch, "/0/name")
	if got == nil {
		t.Fatal("schemaAtPointer(/0/name) returned nil, want string schema")
	}
	if detail := schemaDetail(got); detail != "string" {
		t.Errorf("schemaDetail = %q, want \"string\"", detail)
	}
}

// TestSchemaAtPointer_PrefixItems verifies that a numeric pointer segment
// resolves through PrefixItems when the index is within bounds.
func TestSchemaAtPointer_PrefixItems(t *testing.T) {
	raw := `{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"type": "array",
		"prefixItems": [
			{"type": "string"},
			{"type": "integer"}
		]
	}`
	sch := buildSchema(t, raw)

	// /1 should resolve to the integer schema (PrefixItems[1]).
	got := schemaAtPointer(sch, "/1")
	if got == nil {
		t.Fatal("schemaAtPointer(/1) returned nil, want integer schema")
	}
	if detail := schemaDetail(got); detail != "integer" {
		t.Errorf("schemaDetail = %q, want \"integer\"", detail)
	}
}

// TestSchemaAtPointer_ArrayOfScalars verifies that /0 on an array of strings
// resolves to the items scalar schema.
func TestSchemaAtPointer_ArrayOfScalars(t *testing.T) {
	raw := `{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"type": "array",
		"items": {"type": "string"}
	}`
	sch := buildSchema(t, raw)

	got := schemaAtPointer(sch, "/0")
	if got == nil {
		t.Fatal("schemaAtPointer(/0) returned nil, want string schema")
	}
	if detail := schemaDetail(got); detail != "string" {
		t.Errorf("schemaDetail = %q, want \"string\"", detail)
	}
}

// TestSchemaAtPointer_NumericPropertyWins verifies that a schema with an
// explicit property named "0" resolves through Properties rather than items.
func TestSchemaAtPointer_NumericPropertyWins(t *testing.T) {
	raw := `{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"type": "object",
		"properties": {
			"0": {"type": "string", "description": "prop zero"}
		}
	}`
	sch := buildSchema(t, raw)

	got := schemaAtPointer(sch, "/0")
	if got == nil {
		t.Fatal("schemaAtPointer(/0) returned nil, want string schema from properties")
	}
	if got.Description != "prop zero" {
		t.Errorf("Description = %q, want \"prop zero\" (Properties should win over Items)", got.Description)
	}
}

// --------------------------------------------------------------------------
// Task 2: schemaAtPointer — $ref and allOf navigation
// --------------------------------------------------------------------------

// TestSchemaAtPointer_FollowsRef verifies that a properties lookup follows
// $ref indirection to reach nested properties.
func TestSchemaAtPointer_FollowsRef(t *testing.T) {
	raw := `{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"$defs": {
			"Spec": {
				"type": "object",
				"properties": {
					"name": {"type": "string"}
				}
			}
		},
		"type": "object",
		"properties": {
			"spec": {"$ref": "#/$defs/Spec"}
		}
	}`
	sch := buildSchema(t, raw)

	// /spec/name should resolve through $ref to the string schema.
	got := schemaAtPointer(sch, "/spec/name")
	if got == nil {
		t.Fatal("schemaAtPointer(/spec/name) returned nil, want string schema")
	}
	if detail := schemaDetail(got); detail != "string" {
		t.Errorf("schemaDetail = %q, want \"string\"", detail)
	}
}

// TestSchemaAtPointer_AllOfMerge verifies that a properties lookup merges
// allOf subschemas to find properties declared in a sibling subschema.
func TestSchemaAtPointer_AllOfMerge(t *testing.T) {
	raw := `{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"$defs": {
			"Base": {
				"type": "object",
				"properties": {
					"base": {"type": "string"}
				}
			}
		},
		"type": "object",
		"properties": {
			"spec": {
				"allOf": [
					{"$ref": "#/$defs/Base"},
					{"type": "object", "properties": {"extra": {"type": "integer"}}}
				]
			}
		}
	}`
	sch := buildSchema(t, raw)

	// /spec/extra should resolve through allOf to the integer schema.
	got := schemaAtPointer(sch, "/spec/extra")
	if got == nil {
		t.Fatal("schemaAtPointer(/spec/extra) returned nil, want integer schema")
	}
	if detail := schemaDetail(got); detail != "integer" {
		t.Errorf("schemaDetail = %q, want \"integer\"", detail)
	}

	// /spec/base should also resolve (inherited from the $ref base).
	got2 := schemaAtPointer(sch, "/spec/base")
	if got2 == nil {
		t.Fatal("schemaAtPointer(/spec/base) returned nil, want string schema")
	}
	if detail := schemaDetail(got2); detail != "string" {
		t.Errorf("schemaDetail = %q, want \"string\"", detail)
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
		name             string
		doc              yamldoc.Document
		line, char       int
		wantPointer      string
		wantParent       string
		wantIsKey        bool
		wantExistingKeys []string
	}{
		{
			name: "EmptyKind_JustPastColon",
			doc:  docsEmpty[0],
			line: 1, char: 5,
			wantPointer:      "/kind",
			wantParent:       "",
			wantIsKey:        false,
			wantExistingKeys: []string{"apiVersion", "kind"},
		},
		{
			name: "EmptyKind_OneCharPastColon",
			doc:  docsEmpty[0],
			line: 1, char: 6,
			wantPointer:      "/kind",
			wantParent:       "",
			wantIsKey:        false,
			wantExistingKeys: []string{"apiVersion", "kind"},
		},
		{
			name: "PopulatedKind_InsideValue",
			doc:  docsPopulated[0],
			line: 1, char: 7,
			wantPointer:      "/kind",
			wantParent:       "",
			wantIsKey:        false,
			wantExistingKeys: []string{"apiVersion", "kind"},
		},
		{
			name: "PopulatedKind_InsideKey",
			doc:  docsPopulated[0],
			line: 1, char: 0,
			wantPointer:      "/kind",
			wantParent:       "",
			wantIsKey:        true,
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

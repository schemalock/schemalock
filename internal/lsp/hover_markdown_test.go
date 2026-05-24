package lsp

import (
	"strings"
	"testing"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

// buildSchemaWithFormat compiles a JSON Schema with format assertion enabled,
// so that sch.Format is populated for known format names.
func buildSchemaWithFormat(t *testing.T, raw string) *jsonschema.Schema {
	t.Helper()
	doc, err := jsonschema.UnmarshalJSON(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("UnmarshalJSON: %v", err)
	}
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	c.AssertFormat()
	if err := c.AddResource("test://schema", doc); err != nil {
		t.Fatalf("AddResource: %v", err)
	}
	sch, err := c.Compile("test://schema")
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	return sch
}

// TestHoverMarkdown covers all rendering paths of hoverMarkdown.
// Each case checks substrings (wantContains / wantNotContain) so that minor
// spacing tweaks in the renderer do not break the suite. Full equality
// (wantExact) is used sparingly, only for trivial cases.
func TestHoverMarkdown(t *testing.T) {
	tests := []struct {
		name           string
		schema         string // JSON Schema literal; empty → nil schema
		fieldName      string
		parentRequired []string // keys in the parent's required list
		kind           string
		apiVersion     string
		wantContains   []string // substrings that must appear
		wantNotContain []string // substrings that must NOT appear
		wantExact      string   // if non-empty, full equality check
	}{
		{
			name:      "nil schema",
			wantExact: "",
		},
		{
			name:      "all-empty schema — no field name, no annotations",
			schema:    `{}`,
			fieldName: "",
			wantExact: "",
		},
		{
			name:      "bare typed field — signature only",
			schema:    `{"type": "string"}`,
			fieldName: "myField",
			wantContains: []string{
				"```yaml",
				"myField: string",
				"```",
			},
			wantNotContain: []string{"**Constraints**", "---"},
		},
		{
			name:           "required field — signature contains # required comment",
			schema:         `{"type": "string"}`,
			fieldName:      "name",
			parentRequired: []string{"name"},
			wantContains:   []string{"name: string", "# required"},
		},
		{
			name:           "non-required field — no # required comment",
			schema:         `{"type": "string"}`,
			fieldName:      "name",
			parentRequired: []string{},
			wantNotContain: []string{"# required"},
		},
		{
			name:      "description appears after signature",
			schema:    `{"type": "object", "description": "Standard object metadata."}`,
			fieldName: "metadata",
			wantContains: []string{
				"```yaml",
				"metadata: object",
				"---",
				"Standard object metadata.",
			},
		},
		{
			name:      "deprecated field — callout at top",
			schema:    `{"type": "string", "deprecated": true}`,
			fieldName: "oldField",
			wantContains: []string{
				"> ⚠️ **Deprecated**",
				"oldField: string",
			},
		},
		{
			name: "numeric constraints — minimum, maximum, exclusiveMinimum, exclusiveMaximum",
			schema: `{
				"type": "number",
				"minimum": 1,
				"maximum": 100,
				"exclusiveMinimum": 0,
				"exclusiveMaximum": 101
			}`,
			fieldName: "count",
			wantContains: []string{
				"**Constraints**",
				"minimum: `1`",
				"maximum: `100`",
				"exclusive minimum: `> 0`",
				"exclusive maximum: `< 101`",
			},
		},
		{
			name: "integer default",
			schema: `{
				"type": "integer",
				"default": 3
			}`,
			fieldName: "replicas",
			wantContains: []string{
				"default: `3`",
			},
			wantNotContain: []string{`default: "3"`, "default: `3.`"},
		},
		{
			name: "string default — no surrounding quotes",
			schema: `{
				"type": "string",
				"default": "30d"
			}`,
			fieldName: "retention",
			wantContains: []string{
				"default: `30d`",
			},
			wantNotContain: []string{`default: "30d"`, `default: ` + "`" + `"30d"` + "`"},
		},
		{
			name: "pattern and minLength and maxLength",
			schema: `{
				"type": "string",
				"minLength": 3,
				"maxLength": 64,
				"pattern": "^[a-z]+"
			}`,
			fieldName: "slug",
			wantContains: []string{
				"min length: `3`",
				"max length: `64`",
				"pattern: `",
				"[a-z]+",
			},
		},
		// format constraint is tested separately (TestHoverMarkdown_Format) because
		// Draft 2020-12 only populates sch.Format when format assertion is enabled.
		{
			name: "enum short — all values, no ellipsis",
			schema: `{
				"type": "string",
				"enum": ["A", "B", "C"]
			}`,
			fieldName: "mode",
			wantContains: []string{
				"allowed values: `A`, `B`, `C`",
			},
			wantNotContain: []string{"…"},
		},
		{
			name: "enum long — capped at 6 with ellipsis",
			schema: `{
				"type": "string",
				"enum": ["A", "B", "C", "D", "E", "F", "G"]
			}`,
			fieldName: "mode",
			wantContains: []string{
				"`A`", "`B`", "`C`", "`D`", "`E`", "`F`",
				", …",
			},
			wantNotContain: []string{"`G`"},
		},
		{
			name: "examples capped at 3 with ellipsis",
			schema: `{
				"type": "string",
				"examples": ["a", "b", "c", "d", "e"]
			}`,
			fieldName: "val",
			wantContains: []string{
				"examples: `a`, `b`, `c`, …",
			},
			wantNotContain: []string{"`d`", "`e`"},
		},
		{
			name: "footer — kind and apiVersion",
			schema: `{
				"type": "object",
				"description": "Some object."
			}`,
			fieldName:  "spec",
			kind:       "VMCluster",
			apiVersion: "operator.victoriametrics.com/v1beta1",
			wantContains: []string{
				"*VMCluster — operator.victoriametrics.com/v1beta1*",
			},
		},
		{
			name:      "footer — kind only",
			schema:    `{"type": "object"}`,
			fieldName: "spec",
			kind:      "MyKind",
			wantContains: []string{
				"*MyKind*",
			},
			wantNotContain: []string{"—"},
		},
		{
			name:       "footer — apiVersion only",
			schema:     `{"type": "object"}`,
			fieldName:  "spec",
			apiVersion: "apps/v1",
			wantContains: []string{
				"*apps/v1*",
			},
		},
		{
			name:      "no footer when both kind and apiVersion empty",
			schema:    `{"type": "string", "description": "A field."}`,
			fieldName: "field",
			wantContains: []string{
				"A field.",
			},
			wantNotContain: []string{"*"},
		},
		{
			name:      "big rat integer — renders as integer not fraction",
			schema:    `{"type": "number", "minimum": 3}`,
			fieldName: "count",
			wantContains: []string{
				"minimum: `3`",
			},
			wantNotContain: []string{"3/1", "3."},
		},
		{
			name:      "big rat fraction — renders with decimal notation",
			schema:    `{"type": "number", "minimum": 0.5}`,
			fieldName: "ratio",
			wantContains: []string{
				"minimum: `0.5`",
			},
			wantNotContain: []string{"1/2"},
		},
		{
			name:      "no type — typeDetail falls back to any",
			schema:    `{"description": "Untyped."}`,
			fieldName: "x",
			wantContains: []string{
				"x: any",
				"Untyped.",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var sch *jsonschema.Schema
			if tc.schema != "" {
				sch = buildSchema(t, tc.schema)
			}
			req := make(map[string]bool, len(tc.parentRequired))
			for _, r := range tc.parentRequired {
				req[r] = true
			}
			got := hoverMarkdown(sch, tc.fieldName, req, tc.kind, tc.apiVersion)
			for _, want := range tc.wantContains {
				if !strings.Contains(got, want) {
					t.Errorf("hover output missing %q\nfull output:\n%s", want, got)
				}
			}
			for _, ban := range tc.wantNotContain {
				if strings.Contains(got, ban) {
					t.Errorf("hover output unexpectedly contains %q\nfull output:\n%s", ban, got)
				}
			}
			if tc.wantExact != "" && got != tc.wantExact {
				t.Errorf("hover output mismatch:\ngot:\n%s\nwant:\n%s", got, tc.wantExact)
			}
			if tc.wantExact == "" && tc.schema == "" && tc.fieldName == "" && got != "" {
				// nil schema + empty fieldName must return "".
				t.Errorf("expected empty string for nil schema, got: %q", got)
			}
		})
	}
}

// TestHoverMarkdown_Format verifies that the format constraint is rendered when
// the schema is compiled with format assertion enabled (Draft 2020-12 only
// populates sch.Format when AssertFormat() is called on the compiler).
func TestHoverMarkdown_Format(t *testing.T) {
	sch := buildSchemaWithFormat(t, `{"type": "string", "format": "date-time"}`)
	got := hoverMarkdown(sch, "createdAt", nil, "", "")
	if !strings.Contains(got, "format: `date-time`") {
		t.Errorf("hover output missing format constraint\nfull output:\n%s", got)
	}
}

// TestLastPointerSegment covers lastPointerSegment across common pointer forms.
func TestLastPointerSegment(t *testing.T) {
	tests := []struct {
		ptr  string
		want string
	}{
		{ptr: "", want: ""},
		{ptr: "/foo", want: "foo"},
		{ptr: "/foo/bar", want: "bar"},
		{ptr: "/foo/bar/baz", want: "baz"},
		{ptr: "/a~1b", want: "a/b"},   // RFC 6901 ~1 → /
		{ptr: "/a~0b", want: "a~b"},   // RFC 6901 ~0 → ~
		{ptr: "/x/a~1b/c", want: "c"}, // ~1 in middle segment, last is plain
	}
	for _, tc := range tests {
		t.Run(tc.ptr, func(t *testing.T) {
			got := lastPointerSegment(tc.ptr)
			if got != tc.want {
				t.Errorf("lastPointerSegment(%q) = %q, want %q", tc.ptr, got, tc.want)
			}
		})
	}
}

// TestRequiredSet covers requiredSet across nil, empty, and populated schemas.
func TestRequiredSet(t *testing.T) {
	tests := []struct {
		name   string
		schema string // JSON literal; "" → nil
		want   map[string]bool
	}{
		{
			name: "nil schema",
			want: map[string]bool{},
		},
		{
			name:   "no required field",
			schema: `{"type": "object", "properties": {"a": {}}}`,
			want:   map[string]bool{},
		},
		{
			name:   "single required",
			schema: `{"type": "object", "required": ["name"]}`,
			want:   map[string]bool{"name": true},
		},
		{
			name:   "multiple required",
			schema: `{"type": "object", "required": ["a", "b", "c"]}`,
			want:   map[string]bool{"a": true, "b": true, "c": true},
		},
		{
			name:   "required does not include non-required keys",
			schema: `{"type": "object", "required": ["x"], "properties": {"x": {}, "y": {}}}`,
			want:   map[string]bool{"x": true},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var sch *jsonschema.Schema
			if tc.schema != "" {
				sch = buildSchema(t, tc.schema)
			}
			got := requiredSet(sch)
			// Check every expected key is present.
			for k, v := range tc.want {
				if got[k] != v {
					t.Errorf("requiredSet[%q] = %v, want %v", k, got[k], v)
				}
			}
			// Check no unexpected keys.
			for k := range got {
				if !tc.want[k] {
					t.Errorf("requiredSet has unexpected key %q", k)
				}
			}
		})
	}
}

package lsp

import (
	"encoding/json"
	"fmt"
	"math/big"
	"strings"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

// hoverMarkdown renders an LSP hover body for a single schema node.
//
// Layout (sections separated by horizontal rules; empty sections elided):
//
//	[deprecated callout, if Deprecated]
//
//	```yaml
//	<fieldName>: <typeDetail>   # required
//	```
//
//	**Constraints**
//	- default: `…`
//	- minimum: `…`
//	- …
//
//	---
//
//	<sch.Description>
//
//	---
//
//	*<kindLabel> — <apiVersion>*
//
// fieldName is the local key name being hovered (last pointer segment, unescaped).
// parentRequired holds the parent schema's Required set so the signature can
// append the "# required" YAML comment.
// kindLabel is something like "VMCluster" and apiVersion like
// "operator.victoriametrics.com/v1beta1"; either may be empty.
func hoverMarkdown(sch *jsonschema.Schema, fieldName string, parentRequired map[string]bool, kindLabel, apiVersion string) string {
	if sch == nil {
		return ""
	}

	var b strings.Builder

	// 1. Deprecated callout (top, so users see it first).
	if sch.Deprecated {
		b.WriteString("> ⚠️ **Deprecated**\n\n")
	}

	// 2. Signature fence — always include when fieldName is non-empty.
	if fieldName != "" {
		typeDetail := schemaDetail(sch)
		if typeDetail == "" {
			typeDetail = "any"
		}
		b.WriteString("```yaml\n")
		b.WriteString(fieldName)
		b.WriteString(": ")
		b.WriteString(typeDetail)
		if parentRequired[fieldName] {
			b.WriteString("   # required")
		}
		b.WriteString("\n```\n")
	}

	// 3. Constraints block.
	constraints := collectConstraints(sch)
	if len(constraints) > 0 {
		b.WriteString("\n**Constraints**\n\n")
		for _, c := range constraints {
			b.WriteString("- ")
			b.WriteString(c)
			b.WriteString("\n")
		}
	}

	// 4. Description.
	if desc := strings.TrimSpace(sch.Description); desc != "" {
		if b.Len() > 0 {
			b.WriteString("\n---\n\n")
		}
		b.WriteString(desc)
		b.WriteString("\n")
	}

	// 5. Footer (kind — apiVersion) if either is set.
	if kindLabel != "" || apiVersion != "" {
		if b.Len() > 0 {
			b.WriteString("\n---\n\n")
		}
		b.WriteString("*")
		switch {
		case kindLabel != "" && apiVersion != "":
			b.WriteString(kindLabel)
			b.WriteString(" — ")
			b.WriteString(apiVersion)
		case kindLabel != "":
			b.WriteString(kindLabel)
		default:
			b.WriteString(apiVersion)
		}
		b.WriteString("*\n")
	}

	return b.String()
}

// collectConstraints returns the markdown bullet bodies (no leading dash)
// for each constraint a hover popup should surface. Empty when none apply.
func collectConstraints(sch *jsonschema.Schema) []string {
	if sch == nil {
		return nil
	}
	var out []string

	if sch.Default != nil {
		if v, ok := marshalForDisplay(*sch.Default); ok {
			out = append(out, "default: `"+v+"`")
		}
	}

	// Numeric.
	if sch.Minimum != nil {
		out = append(out, "minimum: `"+ratString(sch.Minimum)+"`")
	}
	if sch.ExclusiveMinimum != nil {
		out = append(out, "exclusive minimum: `> "+ratString(sch.ExclusiveMinimum)+"`")
	}
	if sch.Maximum != nil {
		out = append(out, "maximum: `"+ratString(sch.Maximum)+"`")
	}
	if sch.ExclusiveMaximum != nil {
		out = append(out, "exclusive maximum: `< "+ratString(sch.ExclusiveMaximum)+"`")
	}

	// String.
	if sch.MinLength != nil {
		out = append(out, fmt.Sprintf("min length: `%d`", *sch.MinLength))
	}
	if sch.MaxLength != nil {
		out = append(out, fmt.Sprintf("max length: `%d`", *sch.MaxLength))
	}
	if sch.Pattern != nil {
		out = append(out, "pattern: `"+sch.Pattern.String()+"`")
	}
	// Format has a Name field; use it directly to avoid printing the Validate
	// func pointer via %v.
	if sch.Format != nil {
		out = append(out, "format: `"+sch.Format.Name+"`")
	}

	// Enum: cap at 6 with "…" suffix so very large enums (e.g. RetentionPeriod)
	// stay readable.
	if sch.Enum != nil && len(sch.Enum.Values) > 0 {
		const maxEnum = 6
		parts := make([]string, 0, maxEnum)
		for i, v := range sch.Enum.Values {
			if i >= maxEnum {
				break
			}
			if s, ok := marshalForDisplay(v); ok {
				parts = append(parts, "`"+s+"`")
			}
		}
		line := "allowed values: " + strings.Join(parts, ", ")
		if len(sch.Enum.Values) > maxEnum {
			line += ", …"
		}
		out = append(out, line)
	}

	// Examples: cap at 3.
	if len(sch.Examples) > 0 {
		const maxEx = 3
		parts := make([]string, 0, maxEx)
		for i, v := range sch.Examples {
			if i >= maxEx {
				break
			}
			if s, ok := marshalForDisplay(v); ok {
				parts = append(parts, "`"+s+"`")
			}
		}
		line := "examples: " + strings.Join(parts, ", ")
		if len(sch.Examples) > maxEx {
			line += ", …"
		}
		out = append(out, line)
	}

	return out
}

// ratString renders a *big.Rat compactly: integers as "3", non-integers up to
// 6 fractional digits trimmed of trailing zeros. Never panics on nil.
func ratString(r *big.Rat) string {
	if r == nil {
		return ""
	}
	if r.IsInt() {
		return r.Num().String()
	}
	s := r.FloatString(6)
	s = strings.TrimRight(s, "0")
	s = strings.TrimRight(s, ".")
	return s
}

// marshalForDisplay returns a display string for v: strings are returned as-is
// (without surrounding JSON quotes) so backtick rendering reads cleanly.
// Other values are JSON-marshalled. Returns ("", false) when marshalling fails.
func marshalForDisplay(v any) (string, bool) {
	if s, ok := v.(string); ok {
		return s, true
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", false
	}
	return string(b), true
}

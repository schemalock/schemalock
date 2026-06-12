package validator

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"

	"github.com/schemalock/schemalock/internal/yamldoc"
)

// escapePointerSegment applies RFC 6901 escaping to a single JSON Pointer
// segment: "~" → "~0", "/" → "~1". Order matters: "~" must be replaced before
// "/".
func escapePointerSegment(seg string) string {
	seg = strings.ReplaceAll(seg, "~", "~0")
	seg = strings.ReplaceAll(seg, "/", "~1")
	return seg
}

// Position is a 1-based source location, mirroring yamldoc.Position.
// It is re-declared here so that callers can import only internal/validator
// without importing internal/yamldoc.
type Position = yamldoc.Position

// Range is a half-open source range from Start to End (both 1-based).
type Range struct {
	// Start is the beginning of the range (inclusive).
	Start Position
	// End is the end of the range (inclusive, same line for PoC).
	End Position
}

// Diagnostic is the LSP-shaped output of a single validation failure.
// Positions come from the yamldoc.PositionMap; unmapped pointers fall back to
// line 1, column 1.
type Diagnostic struct {
	// Range is the source range of the offending key.
	Range Range
	// Severity is always 1 (Error) for PoC.
	Severity int
	// Message is the human-readable error description.
	Message string
	// Source identifies the producer of this diagnostic.
	Source string
}

// Validate runs compiled against the body of doc and returns one Diagnostic
// per leaf validation error. The diagnostics are sorted deterministically by
// (line, column, message).
//
// doc.Body must be a JSON-compatible value (map[string]any, []any, scalars).
// If the body is nil, Validate returns nil.
func Validate(compiled *jsonschema.Schema, doc yamldoc.Document) []Diagnostic {
	if doc.Body == nil {
		return nil
	}

	// The jsonschema library validates "any" values. Convert doc.Body to the
	// JSON-round-tripped form so that types match the schema's expectations
	// (e.g. numbers are float64 after JSON decode).
	v, err := jsonRoundTrip(doc.Body)
	if err != nil {
		// Should not happen for a document that was already YAML-parsed;
		// surface as a single internal diagnostic.
		return []Diagnostic{{
			Range:    rangeAt(Position{Line: 1, Column: 1}),
			Severity: 1,
			Message:  fmt.Sprintf("internal: could not normalise document for validation: %s", err),
			Source:   "schemalock",
		}}
	}

	err = compiled.Validate(v)
	if err == nil {
		return nil
	}

	verr, ok := err.(*jsonschema.ValidationError)
	if !ok {
		return []Diagnostic{{
			Range:    rangeAt(Position{Line: 1, Column: 1}),
			Severity: 1,
			Message:  err.Error(),
			Source:   "schemalock",
		}}
	}

	var diags []Diagnostic
	collectDiagnostics(verr, doc.Nodes, &diags)

	// Sort deterministically: (line, column, message).
	sort.Slice(diags, func(i, j int) bool {
		a, b := diags[i], diags[j]
		if a.Range.Start.Line != b.Range.Start.Line {
			return a.Range.Start.Line < b.Range.Start.Line
		}
		if a.Range.Start.Column != b.Range.Start.Column {
			return a.Range.Start.Column < b.Range.Start.Column
		}
		return a.Message < b.Message
	})
	return diags
}

// collectDiagnostics performs a depth-first walk of the ValidationError tree,
// collecting one Diagnostic per leaf error (errors with no Causes).
//
// For additionalProperties leaf errors, the library reports a single error at
// the parent object with the list of unknown property names. This function fans
// out into one diagnostic per unknown property, each anchored at the child
// pointer "<parent>/<name>" when present in doc.Nodes. If the child pointer is
// absent (e.g. the unknown field is on the final line with no trailing newline
// and the parser did not record it), the diagnostic falls back to the parent's
// position so the diagnostic is never silently lost.
func collectDiagnostics(e *jsonschema.ValidationError, nodes yamldoc.PositionMap, out *[]Diagnostic) {
	if len(e.Causes) == 0 {
		// Leaf error — check for additionalProperties first.
		if ap, ok := e.ErrorKind.(*kind.AdditionalProperties); ok && len(ap.Properties) > 0 {
			parentPtr := instanceLocationToPointer(e.InstanceLocation)
			// Sort names for deterministic output before the outer sort runs.
			names := append([]string(nil), ap.Properties...)
			sort.Strings(names)
			for _, name := range names {
				childPtr := parentPtr + "/" + escapePointerSegment(name)
				pos, hit := nodes[childPtr]
				if !hit {
					pos = lookupPosition(parentPtr, nodes)
				}
				*out = append(*out, Diagnostic{
					Range:    rangeAt(pos),
					Severity: 1,
					Message:  fmt.Sprintf("unknown field '%s' (not in schema; disable with schemalock.strict=false)", name),
					Source:   "schemalock",
				})
			}
			return
		}
		// Non-AP leaf: emit a single diagnostic at the instance location.
		ptr := instanceLocationToPointer(e.InstanceLocation)
		pos := lookupPosition(ptr, nodes)
		*out = append(*out, Diagnostic{
			Range:    rangeAt(pos),
			Severity: 1,
			Message:  e.Error(),
			Source:   "schemalock",
		})
		return
	}
	for _, cause := range e.Causes {
		collectDiagnostics(cause, nodes, out)
	}
}

// instanceLocationToPointer converts InstanceLocation (a []string of path
// segments) to a JSON Pointer string (e.g. "/spec/replicas").
func instanceLocationToPointer(loc []string) string {
	if len(loc) == 0 {
		return ""
	}
	var sb strings.Builder
	for _, seg := range loc {
		sb.WriteByte('/')
		sb.WriteString(escapePointerSegment(seg))
	}
	return sb.String()
}

// lookupPosition looks up pointer in nodes and returns its Position. Falls
// back to {Line:1, Column:1} if the pointer is not found.
func lookupPosition(pointer string, nodes yamldoc.PositionMap) Position {
	if pos, ok := nodes[pointer]; ok {
		return pos
	}
	return Position{Line: 1, Column: 1}
}

// rangeAt returns a Range spanning exactly one character at pos (PoC; richer
// ranges are out of scope for M7).
func rangeAt(pos Position) Range {
	return Range{Start: pos, End: pos}
}

// jsonRoundTrip converts v through JSON encoding/decoding so that the type
// set matches what jsonschema/v6 expects (all numbers become float64, etc.).
func jsonRoundTrip(v any) (any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}

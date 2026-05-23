package lsp

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
	lsp "go.lsp.dev/protocol"
	"github.com/schemalock/app/internal/yamldoc"
)

// cursorContext describes what the cursor is positioned at within a document.
type cursorContext struct {
	// Pointer is the JSON Pointer of the deepest node that contains the cursor
	// position. For a key position, this is the pointer of the key itself.
	// For a value position, this is the pointer of the containing key.
	Pointer string

	// ParentPointer is the JSON Pointer of the parent object. Used to enumerate
	// sibling properties for completion at a key position.
	ParentPointer string

	// IsKeyPosition is true when the cursor is on or within the key token of a
	// mapping entry (property name). The schema at ParentPointer's properties
	// map should be used for completion.
	IsKeyPosition bool

	// ExistingKeys holds the property names already present in the parent object
	// at the cursor's level. Used to filter completion results.
	ExistingKeys []string
}

// positionAt resolves the JSON Pointer and cursor context for an LSP position
// within a parsed YAML document.
//
// Strategy:
//  1. Convert LSP 0-based position to 1-based yamldoc coordinates.
//  2. Scan the PositionMap for any key on the exact cursor line.
//     - If found, compare the cursor character against the key's column to
//     determine whether the cursor is on the key (IsKeyPosition=true) or
//     past the ":" separator (value position, IsKeyPosition=false).
//  3. If no key is on the cursor line, build the indent stack of
//     currently-open keys at cursor.line (forward scan, popping any
//     stack top whose column >= the incoming key's column).
//  4. Classify the cursor column against the stack:
//     - if the deepest open key K has K.col == cursor.col → sibling of K
//       (parent of K's children);
//     - if K.col < cursor.col → nest into K (offer K's children);
//     - if no open key has K.col <= cursor.col → root completion.
func positionAt(pos lsp.Position, doc yamldoc.Document) cursorContext {
	// LSP is 0-based; yamldoc is 1-based.
	targetLine := int(pos.Line) + 1
	// Convert cursor character to 1-based.
	targetCol := int(pos.Character) + 1

	// Collect all (pointer, Position) pairs.
	type entry struct {
		pointer string
		srcPos  yamldoc.Position
		depth   int
	}
	entries := make([]entry, 0, len(doc.Nodes))
	for ptr, p := range doc.Nodes {
		entries = append(entries, entry{
			pointer: ptr,
			srcPos:  p,
			depth:   strings.Count(ptr, "/"),
		})
	}

	// Sort: primary = line ascending; secondary = depth descending so the
	// deepest (most specific) pointer wins when multiple keys share a line
	// (which shouldn't happen in well-formed YAML, but be defensive).
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].srcPos.Line != entries[j].srcPos.Line {
			return entries[i].srcPos.Line < entries[j].srcPos.Line
		}
		return entries[i].depth > entries[j].depth
	})

	// First pass: look for a key on the exact cursor line.
	for _, e := range entries {
		if e.srcPos.Line != targetLine {
			continue
		}
		// Use the raw (escaped) final pointer segment for the column boundary so
		// that keys containing "~" or "/" (escaped as "~0"/"~1") are measured
		// correctly — the column end must reflect the actual characters on
		// the wire, not the unescaped name.
		rawSeg := e.pointer[strings.LastIndex(e.pointer, "/")+1:]
		keyEndCol := e.srcPos.Column + len(rawSeg) // column after the last char of the key

		// If the cursor is at or within the key token → key-name completion.
		// If the cursor is past keyEndCol (i.e. past the key and the ":" separator)
		// → value completion.
		if targetCol <= keyEndCol {
			parent := parentPointer(e.pointer)
			return cursorContext{
				Pointer:       e.pointer,
				ParentPointer: parent,
				IsKeyPosition: true,
				ExistingKeys:  existingKeysAt(parent, doc),
			}
		}
		// Cursor is past the key on the same line → value position for this field.
		parent := parentPointer(e.pointer)
		return cursorContext{
			Pointer:       e.pointer,
			ParentPointer: parent,
			IsKeyPosition: false,
			ExistingKeys:  existingKeysAt(parent, doc),
		}
	}

	// Second pass: cursor is inside a multi-line block value.
	// Build the indent stack of currently-open keys at cursor.line by
	// walking keys above the cursor in line order. A key at column <= the
	// top of the stack closes the top's scope (standard indentation-stack
	// rule). This correctly handles closing siblings: a key that closes a
	// deeper scope is visible here, unlike the old pointer-chain walk which
	// had no visibility into predecessor relationships.
	type openKey struct {
		pointer string
		column  int
	}
	// entries is sorted by line ascending; the depth-desc tiebreaker is
	// harmless because two keys never share a line in well-formed YAML.
	var stack []openKey
	for _, e := range entries {
		if e.srcPos.Line >= targetLine {
			break // entries is sorted; no more candidates
		}
		for len(stack) > 0 && stack[len(stack)-1].column >= e.srcPos.Column {
			stack = stack[:len(stack)-1]
		}
		stack = append(stack, openKey{pointer: e.pointer, column: e.srcPos.Column})
	}

	if len(stack) == 0 {
		// No keys precede the cursor — root completion.
		return cursorContext{
			ParentPointer: "",
			IsKeyPosition: true,
			ExistingKeys:  existingKeysAt("", doc),
		}
	}

	// Find the deepest open key with column <= cursor.col.
	idx := -1
	for i := len(stack) - 1; i >= 0; i-- {
		if stack[i].column <= targetCol {
			idx = i
			break
		}
	}
	if idx == -1 {
		// Cursor is outdented past every open key — root completion.
		return cursorContext{
			ParentPointer: "",
			IsKeyPosition: true,
			ExistingKeys:  existingKeysAt("", doc),
		}
	}

	k := stack[idx]
	if k.column == targetCol {
		// Cursor is at the same column as k → sibling of k.
		// Offer k's parent's properties (siblings of k).
		parent := parentPointer(k.pointer)
		return cursorContext{
			ParentPointer: parent,
			IsKeyPosition: true,
			ExistingKeys:  existingKeysAt(parent, doc),
		}
	}
	// k.column < targetCol → cursor is nested inside k.
	// Offer k's children.
	return cursorContext{
		ParentPointer: k.pointer,
		IsKeyPosition: true,
		ExistingKeys:  existingKeysAt(k.pointer, doc),
	}
}

// parentPointer returns the JSON Pointer one level up from ptr.
// For "/a/b/c" it returns "/a/b". For "/a" it returns "".
func parentPointer(ptr string) string {
	idx := strings.LastIndex(ptr, "/")
	if idx <= 0 {
		return ""
	}
	return ptr[:idx]
}

// lastPointerSegment returns the final segment of a JSON Pointer, unescaped
// per RFC 6901. Returns "" for the root pointer "".
func lastPointerSegment(ptr string) string {
	if ptr == "" {
		return ""
	}
	idx := strings.LastIndex(ptr, "/")
	if idx < 0 {
		return unescapePointerSegment(ptr)
	}
	return unescapePointerSegment(ptr[idx+1:])
}

// requiredSet returns the set of required property names for an object schema.
// Returns an empty (non-nil) map when sch is nil or has no required fields, so
// callers can do parentRequired[name] without nil checks.
func requiredSet(sch *jsonschema.Schema) map[string]bool {
	out := make(map[string]bool)
	if sch == nil {
		return out
	}
	for _, r := range sch.Required {
		out[r] = true
	}
	return out
}

// existingKeysAt returns the property names present at parentPointer in doc.
// Used to filter out already-present properties from completion results.
func existingKeysAt(parentPtr string, doc yamldoc.Document) []string {
	seen := make(map[string]bool)
	for ptr := range doc.Nodes {
		if parentPtr == "" {
			// Root level: collect top-level keys (exactly one "/" segment).
			if strings.Count(ptr, "/") == 1 {
				seg := ptr[1:] // strip leading "/"
				if !strings.Contains(seg, "/") {
					seen[seg] = true
				}
			}
		} else {
			prefix := parentPtr + "/"
			if strings.HasPrefix(ptr, prefix) {
				rest := ptr[len(prefix):]
				// Only immediate children (no further "/" in rest).
				if !strings.Contains(rest, "/") {
					seen[rest] = true
				}
			}
		}
	}
	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// schemaAtPointer walks the compiled JSON Schema following the given JSON
// Pointer path and returns the sub-schema at that location.
//
// Only "properties" navigation is supported. Sequence indices and
// patternProperties are not resolved in the PoC. Returns nil if the pointer
// cannot be followed within the schema.
func schemaAtPointer(root *jsonschema.Schema, pointer string) *jsonschema.Schema {
	if pointer == "" {
		return root
	}

	segments := strings.Split(strings.TrimPrefix(pointer, "/"), "/")
	cur := root
	for _, seg := range segments {
		if seg == "" {
			continue
		}
		// Unescape RFC 6901 segments.
		seg = unescapePointerSegment(seg)

		if cur.Properties != nil {
			if child, ok := cur.Properties[seg]; ok {
				cur = child
				continue
			}
		}
		// Could not navigate; schema does not describe this path.
		return nil
	}
	return cur
}

// schemaPropertiesAt returns the sub-schema for the parent of pointer, i.e.
// the object schema whose properties should be offered as completion items.
func schemaPropertiesAt(root *jsonschema.Schema, parentPointer string) *jsonschema.Schema {
	return schemaAtPointer(root, parentPointer)
}

// unescapePointerSegment reverses RFC 6901 escaping: "~1" → "/", "~0" → "~".
func unescapePointerSegment(s string) string {
	s = strings.ReplaceAll(s, "~1", "/")
	s = strings.ReplaceAll(s, "~0", "~")
	return s
}

// completionItemsForProperties builds completion items from a schema's
// Properties map, excluding keys already present at the cursor position.
func completionItemsForProperties(sch *jsonschema.Schema, existingKeys []string) []lsp.CompletionItem {
	if sch == nil || sch.Properties == nil {
		return nil
	}

	existing := make(map[string]bool, len(existingKeys))
	for _, k := range existingKeys {
		existing[k] = true
	}

	// Build a set of required property names so they can be ranked first.
	reqSet := requiredSet(sch)

	items := make([]lsp.CompletionItem, 0, len(sch.Properties))
	for name, propSchema := range sch.Properties {
		if existing[name] {
			continue
		}
		item := lsp.CompletionItem{
			Label: name,
			Kind:  completionKindField,
		}
		if propSchema != nil {
			// Wrap description in MarkupContent so editors render it as Markdown.
			if propSchema.Description != "" {
				item.Documentation = lsp.MarkupContent{
					Kind:  lsp.Markdown,
					Value: propSchema.Description,
				}
			}
			// Tag deprecated properties so editors render them with strikethrough.
			if propSchema.Deprecated {
				item.Tags = append(item.Tags, lsp.CompletionItemTagDeprecated)
			}
		}
		// SortText puts required properties before optional ones. Within each
		// group the label provides alphabetical secondary ordering.
		if reqSet[name] {
			item.SortText = "0_" + name
		} else {
			item.SortText = "1_" + name
		}
		// Detail shows the property type or enum summary.
		item.Detail = schemaDetail(propSchema)
		// Insert `name: ` so the YAML remains parseable after acceptance.
		// For object/array properties, use a snippet that opens a nested
		// block; VS Code auto-indents the second line to the cursor's
		// current indent, and the literal two spaces add the child indent.
		switch propertyShape(propSchema) {
		case shapeObject, shapeArray:
			item.InsertText = name + ":\n  $0"
			item.InsertTextFormat = lsp.InsertTextFormatSnippet
		default:
			item.InsertText = name + ": "
		}
		items = append(items, item)
	}

	// Deterministic order: primary by SortText (required-first), secondary by
	// Label (alphabetical within each group).
	sort.Slice(items, func(i, j int) bool {
		if items[i].SortText != items[j].SortText {
			return items[i].SortText < items[j].SortText
		}
		return items[i].Label < items[j].Label
	})

	// Preselect the first required-but-missing property (in final sort order)
	// so the editor's cursor lands on the highest-priority missing field.
	for i := range items {
		if reqSet[items[i].Label] {
			items[i].Preselect = true
			break
		}
	}

	return items
}

// propertyShape classifies a sub-schema for completion insert-text purposes.
// Only the categories that change behaviour are distinguished; everything
// else (string, number, bool, missing, unknown) collapses to shapeScalar.
type propShape int

const (
	shapeScalar propShape = iota
	shapeObject
	shapeArray
)

func propertyShape(sch *jsonschema.Schema) propShape {
	if sch == nil || sch.Types == nil {
		return shapeScalar
	}
	for _, t := range sch.Types.ToStrings() {
		switch t {
		case "object":
			return shapeObject
		case "array":
			return shapeArray
		}
	}
	return shapeScalar
}

// completionItemsForEnum builds completion items from a schema's Enum values.
// parentDetail is the Detail string of the parent field (e.g. "string") and is
// forwarded to each item so the editor can display the owning type.
func completionItemsForEnum(sch *jsonschema.Schema, parentDetail string) []lsp.CompletionItem {
	if sch == nil || sch.Enum == nil {
		return nil
	}

	items := make([]lsp.CompletionItem, 0, len(sch.Enum.Values))
	for i, v := range sch.Enum.Values {
		label := fmt.Sprintf("%v", v)
		// For string enum values, use the string directly without quotes.
		if s, ok := v.(string); ok {
			label = s
		} else {
			// For non-string values marshal to JSON for a canonical representation.
			if b, err := json.Marshal(v); err == nil {
				label = string(b)
			}
		}
		// SortText preserves schema declaration order, which is meaningful for
		// JSON Schema enums (first value is often the default/most common).
		items = append(items, lsp.CompletionItem{
			Label:    label,
			Kind:     completionKindEnumMember,
			SortText: fmt.Sprintf("%04d_%s", i, label),
			Detail:   parentDetail,
		})
	}
	// Declaration order is preserved via SortText; no secondary alphabetical
	// sort so that the schema author's intended ordering is honoured.
	return items
}

// schemaDetail returns a short human-readable type summary for a schema node,
// suitable for the Detail field of a CompletionItem.
//
// Rules (in priority order):
//  1. nil schema → ""
//  2. Enum values present → "enum: A | B | C" (capped at 3, "| …" if more)
//  3. Multiple types → "string | null"
//  4. Single type → "string"
func schemaDetail(sch *jsonschema.Schema) string {
	if sch == nil {
		return ""
	}

	// Enum form takes priority over plain type.
	if sch.Enum != nil && len(sch.Enum.Values) > 0 {
		const maxValues = 3
		parts := make([]string, 0, min(len(sch.Enum.Values), maxValues))
		for i, v := range sch.Enum.Values {
			if i >= maxValues {
				break
			}
			if s, ok := v.(string); ok {
				parts = append(parts, s)
			} else if b, err := json.Marshal(v); err == nil {
				parts = append(parts, string(b))
			} else {
				parts = append(parts, fmt.Sprintf("%v", v))
			}
		}
		result := "enum: " + strings.Join(parts, " | ")
		if len(sch.Enum.Values) > maxValues {
			result += " | …"
		}
		return result
	}

	if sch.Types == nil {
		return ""
	}
	types := sch.Types.ToStrings()
	if len(types) == 0 {
		return ""
	}
	return strings.Join(types, " | ")
}

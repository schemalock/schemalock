package lsp

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/schemalock/app/internal/yamldoc"
	lsp "go.lsp.dev/protocol"
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
//     (parent of K's children);
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

// requiredSet returns the set of required property names for an object schema,
// including required fields harvested from $ref and allOf sub-schemas.
// Returns an empty (non-nil) map when sch is nil or has no required fields, so
// callers can do parentRequired[name] without nil checks.
func requiredSet(sch *jsonschema.Schema) map[string]bool {
	out := effectiveRequired(sch)
	if out == nil {
		return make(map[string]bool)
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

// effectiveProperties returns the merged properties map for a schema,
// following $ref and allOf entries recursively. depth limits recursion to
// prevent cycles (the jsonschema/v6 compiler already prevents infinite refs at
// compile time, but the guard adds defensive depth-bounding).
//
// Merge semantics: later entries in AllOf override earlier ones (last-write
// wins), which matches the JSON Schema merge convention used for completion.
// Direct Properties on the schema itself take the highest priority and are
// applied last.
func effectiveProperties(sch *jsonschema.Schema, depth int) map[string]*jsonschema.Schema {
	const maxDepth = 16
	if sch == nil || depth > maxDepth {
		return nil
	}

	out := make(map[string]*jsonschema.Schema)

	// Harvest from Ref first (lowest priority, overridden by allOf and direct).
	if sch.Ref != nil {
		for k, v := range effectiveProperties(sch.Ref, depth+1) {
			out[k] = v
		}
	}

	// Harvest from AllOf entries (each entry may itself have $ref/allOf).
	for _, sub := range sch.AllOf {
		for k, v := range effectiveProperties(sub, depth+1) {
			out[k] = v
		}
	}

	// Direct Properties on this schema win over everything.
	for k, v := range sch.Properties {
		out[k] = v
	}

	if len(out) == 0 {
		return nil
	}
	return out
}

// effectiveRequired returns the union of required property names from the
// schema itself and from any $ref / allOf sub-schemas, up to depth maxDepth.
func effectiveRequired(sch *jsonschema.Schema) map[string]bool {
	out := make(map[string]bool)
	collectRequired(sch, out, 0)
	return out
}

func collectRequired(sch *jsonschema.Schema, out map[string]bool, depth int) {
	const maxDepth = 16
	if sch == nil || depth > maxDepth {
		return
	}
	for _, r := range sch.Required {
		out[r] = true
	}
	if sch.Ref != nil {
		collectRequired(sch.Ref, out, depth+1)
	}
	for _, sub := range sch.AllOf {
		collectRequired(sub, out, depth+1)
	}
}

// schemaAtPointer walks the compiled JSON Schema following the given JSON
// Pointer path and returns the sub-schema at that location.
//
// Supported navigation: object properties (by name), array items by numeric
// index (tries PrefixItems first, then Items2020, then legacy Items *Schema),
// $ref indirection, and allOf composition. patternProperties are not resolved.
// Returns nil if the pointer cannot be followed within the schema.
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

		// Properties lookup first (numeric property names win over array index).
		props := effectiveProperties(cur, 0)
		if child, ok := props[seg]; ok {
			cur = child
			continue
		}

		// Try array index navigation when the segment is a non-negative integer.
		if idx, ok := parseArrayIndex(seg); ok {
			if next := arrayItemSchema(cur, idx); next != nil {
				cur = next
				continue
			}
		}

		// Could not navigate; schema does not describe this path.
		return nil
	}
	return cur
}

// parseArrayIndex returns (n, true) if s is a valid non-negative base-10
// integer with no leading zeros (except "0" itself). Returns (0, false)
// otherwise.
func parseArrayIndex(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	// Reject leading zeros (e.g. "01") — not valid JSON Pointer array indices.
	if len(s) > 1 && s[0] == '0' {
		return 0, false
	}
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
	}
	return n, true
}

// arrayItemSchema returns the schema for the item at position idx within an
// array schema. It tries (in order):
//  1. PrefixItems[idx] when idx is in bounds.
//  2. Items2020 (the 2020-12 "items" keyword for additional items).
//  3. Legacy Items asserted as *jsonschema.Schema (draft-07 "items" object form).
//
// Returns nil when none of these apply (e.g. no items defined, or sch is not
// an array schema). Tuple-form Items ([]*Schema, draft-04) is deliberately
// skipped — no landed CRD uses it.
func arrayItemSchema(sch *jsonschema.Schema, idx int) *jsonschema.Schema {
	if sch == nil {
		return nil
	}
	// 1. PrefixItems (2020-12).
	if idx < len(sch.PrefixItems) {
		return sch.PrefixItems[idx]
	}
	// 2. Items2020 (additional items after prefixItems).
	if sch.Items2020 != nil {
		return sch.Items2020
	}
	// 3. Legacy Items as *jsonschema.Schema (draft-07 object form).
	// The jsonschema/v6 library stores this as an `any` field; assert to *Schema.
	if s, ok := sch.Items.(*jsonschema.Schema); ok {
		return s
	}
	// TODO: tuple-form Items ([]*jsonschema.Schema, draft-04) is not handled;
	// no landed CRD uses it. Drop through and return nil.
	return nil
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

// wordRangeAt returns an lsp.Range covering the contiguous run of word
// characters around pos.Character on pos.Line. A word character is
// [A-Za-z0-9_-]. On a position not adjacent to a word character (whitespace,
// punctuation, line edge), the returned range is zero-width at pos.
//
// pos uses 0-based LSP line/character coordinates (UTF-16 in the wire spec;
// the project treats text as Go runes for the PoC, matching the existing
// positionAt convention — keys are ASCII for K8s schemas).
func wordRangeAt(text string, pos lsp.Position) lsp.Range {
	lines := strings.Split(text, "\n")
	if int(pos.Line) >= len(lines) {
		return lsp.Range{Start: pos, End: pos}
	}
	line := lines[pos.Line]
	col := min(int(pos.Character), len(line))
	isWord := func(b byte) bool {
		return (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z') ||
			(b >= '0' && b <= '9') || b == '_' || b == '-'
	}
	start := col
	for start > 0 && isWord(line[start-1]) {
		start--
	}
	end := col
	for end < len(line) && isWord(line[end]) {
		end++
	}
	return lsp.Range{
		Start: lsp.Position{Line: pos.Line, Character: uint32(start)},
		End:   lsp.Position{Line: pos.Line, Character: uint32(end)},
	}
}

// completionItemsForProperties builds completion items from a schema's
// effective properties map (direct Properties plus those reachable via $ref and
// allOf), excluding keys already present at the cursor position.
// wordRange is attached to each item as a TextEdit so VS Code derives the
// filter prefix and replace target from the word at cursor.
func completionItemsForProperties(sch *jsonschema.Schema, existingKeys []string, wordRange lsp.Range) []lsp.CompletionItem {
	props := effectiveProperties(sch, 0)
	if sch == nil || props == nil {
		return nil
	}

	existing := make(map[string]bool, len(existingKeys))
	for _, k := range existingKeys {
		existing[k] = true
	}

	// Build a set of required property names so they can be ranked first.
	reqSet := effectiveRequired(sch)

	items := make([]lsp.CompletionItem, 0, len(props))
	for name, propSchema := range props {
		if existing[name] {
			continue
		}
		item := lsp.CompletionItem{
			Label: name,
			Kind:  completionKindClass,
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
		// TextEdit replaces the word at cursor with the property name so VS Code
		// uses the word range as both the filter prefix and the replace target.
		// InsertText is not set — when TextEdit is present it takes precedence
		// (LSP §3.17 CompletionItem.textEdit) and keeping both invites drift.
		// For object/array properties a snippet opens a nested block.
		switch propertyShape(propSchema) {
		case shapeObject, shapeArray:
			item.TextEdit = &lsp.TextEdit{Range: wordRange, NewText: name + ":\n  $0"}
			item.InsertTextFormat = lsp.InsertTextFormatSnippet
		default:
			item.TextEdit = &lsp.TextEdit{Range: wordRange, NewText: name + ": "}
		}
		// FilterText ensures VS Code's client-side filter matches the bare
		// property name regardless of the TextEdit NewText snippet shape.
		item.FilterText = name
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
	if sch == nil {
		return shapeScalar
	}
	if sch.Types != nil {
		for _, t := range sch.Types.ToStrings() {
			switch t {
			case "object":
				return shapeObject
			case "array":
				return shapeArray
			}
		}
	}
	// If the schema has no declared type but has effective properties (via $ref
	// or allOf), treat it as an object so value-position completion works.
	if effectiveProperties(sch, 0) != nil {
		return shapeObject
	}
	return shapeScalar
}

// completionItemsForEnum builds completion items from a schema's Enum values.
// parentDetail is the Detail string of the parent field (e.g. "string") and is
// forwarded to each item so the editor can display the owning type.
// wordRange is attached to each item as a TextEdit (see completionItemsForProperties).
func completionItemsForEnum(sch *jsonschema.Schema, parentDetail string, wordRange lsp.Range) []lsp.CompletionItem {
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
		// FilterText ensures VS Code's client-side filter matches the label.
		// TextEdit replaces the word at cursor (see completionItemsForProperties).
		items = append(items, lsp.CompletionItem{
			Label:      label,
			Kind:       completionKindEnum,
			SortText:   fmt.Sprintf("%04d_%s", i, label),
			Detail:     parentDetail,
			FilterText: label,
			TextEdit:   &lsp.TextEdit{Range: wordRange, NewText: label},
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

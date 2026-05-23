package validator

import (
	"encoding/json"
	"fmt"
)

// WrapStrict injects "additionalProperties": false at every JSON Schema object
// node that has a "properties" key but does not already declare
// "additionalProperties". This closes schema objects against unknown fields
// without altering nodes that explicitly set additionalProperties (true, false,
// or a sub-schema), thereby preserving author intent for annotations and
// preserve-unknown-fields regions.
//
// If schemaBytes is not a JSON object at the top level (e.g. a boolean schema),
// the bytes are returned unchanged with no error.
//
// Parse or marshal failures are wrapped with [ErrStrictRewrite].
func WrapStrict(schemaBytes []byte) ([]byte, error) {
	var root any
	if err := json.Unmarshal(schemaBytes, &root); err != nil {
		return nil, fmt.Errorf("%w: unmarshal: %w", ErrStrictRewrite, err)
	}

	obj, ok := root.(map[string]any)
	if !ok {
		// Boolean schema or non-object: return unchanged.
		return schemaBytes, nil
	}

	// Collect the top-level $defs / definitions maps once so that $ref lookups
	// can dereference them without climbing back up the tree.
	defs := make(map[string]any)
	if d, ok := obj["$defs"].(map[string]any); ok {
		for k, v := range d {
			defs[k] = v
		}
	}
	if d, ok := obj["definitions"].(map[string]any); ok {
		for k, v := range d {
			defs[k] = v
		}
	}

	walkStrict(root, defs, make(map[string]struct{}))

	out, err := json.Marshal(root)
	if err != nil {
		return nil, fmt.Errorf("%w: marshal: %w", ErrStrictRewrite, err)
	}
	return out, nil
}

// walkStrict recursively injects additionalProperties:false into every
// map[string]any node that has "properties" but no "additionalProperties".
// defs is the merged top-level $defs/definitions map (for $ref resolution).
// visited tracks $ref targets already being processed to break cycles.
func walkStrict(node any, defs map[string]any, visited map[string]struct{}) {
	obj, ok := node.(map[string]any)
	if !ok {
		return
	}

	// If this node has "properties" and no "additionalProperties", inject false.
	if _, hasProps := obj["properties"].(map[string]any); hasProps {
		if _, hasAP := obj["additionalProperties"]; !hasAP {
			obj["additionalProperties"] = false
		}
	}

	// Recurse into sub-schema bearing keys.

	// properties.*
	if props, ok := obj["properties"].(map[string]any); ok {
		for _, v := range props {
			walkStrict(v, defs, visited)
		}
	}

	// patternProperties.*
	if pp, ok := obj["patternProperties"].(map[string]any); ok {
		for _, v := range pp {
			walkStrict(v, defs, visited)
		}
	}

	// additionalProperties — recurse only when it is itself a sub-schema object.
	if ap, ok := obj["additionalProperties"].(map[string]any); ok {
		walkStrict(ap, defs, visited)
	}

	// items — either a single sub-schema or a slice of sub-schemas (tuple form).
	switch items := obj["items"].(type) {
	case map[string]any:
		walkStrict(items, defs, visited)
	case []any:
		for _, it := range items {
			walkStrict(it, defs, visited)
		}
	}

	// allOf, anyOf, oneOf — slices of sub-schemas.
	for _, key := range []string{"allOf", "anyOf", "oneOf"} {
		if arr, ok := obj[key].([]any); ok {
			for _, elem := range arr {
				walkStrict(elem, defs, visited)
			}
		}
	}

	// if, then, else, not — single sub-schema each.
	for _, key := range []string{"if", "then", "else", "not"} {
		if sub, ok := obj[key]; ok {
			walkStrict(sub, defs, visited)
		}
	}

	// $defs.* and definitions.*
	for _, key := range []string{"$defs", "definitions"} {
		if d, ok := obj[key].(map[string]any); ok {
			for _, v := range d {
				walkStrict(v, defs, visited)
			}
		}
	}

	// $ref — dereference local #/$defs/<name> and #/definitions/<name> refs.
	// Modify the canonical node in defs (not the reference site).
	if ref, ok := obj["$ref"].(string); ok {
		name, isDef := localDefName(ref)
		if isDef {
			if _, already := visited[name]; !already {
				visited[name] = struct{}{}
				if target, exists := defs[name]; exists {
					walkStrict(target, defs, visited)
				}
			}
		}
		// External refs or other fragment forms: leave alone.
	}
}

// localDefName returns the definition name and true if ref is a local
// JSON Pointer of the form "#/$defs/<name>" or "#/definitions/<name>".
// Any other ref form returns ("", false).
func localDefName(ref string) (string, bool) {
	for _, prefix := range []string{"#/$defs/", "#/definitions/"} {
		if len(ref) > len(prefix) && ref[:len(prefix)] == prefix {
			name := ref[len(prefix):]
			// Only handle simple names (no nested pointers).
			for _, c := range name {
				if c == '/' {
					return "", false
				}
			}
			return name, true
		}
	}
	return "", false
}

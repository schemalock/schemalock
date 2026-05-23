// Package yamldoc parses multi-document YAML streams and exposes both the
// decoded document bodies and a position map for looking up the source
// line/column of any key by its JSON Pointer.
package yamldoc

import (
	"fmt"
	"strings"

	goyaml "github.com/goccy/go-yaml"
	"github.com/goccy/go-yaml/ast"
	"github.com/goccy/go-yaml/parser"
)

// Position is a 1-based source line/column pair.
type Position struct {
	// Line is the 1-based line number within the source document.
	Line int
	// Column is the 1-based column number within the source document.
	Column int
}

// PositionMap maps a JSON Pointer (e.g. "/spec/replicas") to the source
// Position of that key's token. Build with walkNode during parsing.
type PositionMap map[string]Position

// Document is one YAML document in a multi-document stream (separated by ---).
type Document struct {
	// Index is the 0-based index of this document within the source stream.
	Index int
	// StartLine is the 1-based line at which this document begins.
	StartLine int
	// APIVersion is the value of the top-level "apiVersion" key, or "" if
	// missing.
	APIVersion string
	// Kind is the value of the top-level "kind" key, or "" if missing.
	Kind string
	// Body is the decoded JSON-compatible value of this document's root node.
	// Maps are represented as map[string]any; sequences as []any.
	Body map[string]any
	// Nodes maps JSON Pointers to source positions of the corresponding key
	// tokens. Use Lookup to retrieve a position by pointer.
	Nodes PositionMap
}

// Lookup returns the Position for the given JSON Pointer within this document.
// Returns ErrPointerNotFound if the pointer is not in the map.
func (d *Document) Lookup(pointer string) (Position, error) {
	pos, ok := d.Nodes[pointer]
	if !ok {
		return Position{}, fmt.Errorf("%w: %s", ErrPointerNotFound, pointer)
	}
	return pos, nil
}

// Parse reads src as a YAML byte stream and returns one Document per
// non-empty document. Documents are 0-indexed. If a document cannot be
// parsed its error is wrapped with the document index.
func Parse(src []byte) ([]Document, error) {
	// Parse the whole stream into an AST File so we can walk tokens for
	// position information.
	file, err := parser.ParseBytes(src, 0)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrMalformedYAML, err)
	}

	var docs []Document
	for i, astDoc := range file.Docs {
		doc, err := buildDocument(i, astDoc)
		if err != nil {
			return nil, fmt.Errorf("document %d: %w", i, err)
		}
		docs = append(docs, doc)
	}
	return docs, nil
}

// buildDocument converts one ast.DocumentNode into a Document.
func buildDocument(index int, astDoc *ast.DocumentNode) (Document, error) {
	// Determine the start line. Prefer the --- separator token; fall back
	// to the body's first token.
	startLine := 1
	if astDoc.Start != nil && astDoc.Start.Position != nil {
		startLine = astDoc.Start.Position.Line
	} else if astDoc.Body != nil {
		if tok := astDoc.Body.GetToken(); tok != nil && tok.Position != nil {
			startLine = tok.Position.Line
		}
	}

	// Decode the document body into a map[string]any via goyaml.
	// We marshal the individual AST document back to bytes and unmarshal
	// into any so that the type conversion rules (int64, float64, bool,
	// string, nil) match standard JSON conventions.
	var body map[string]any
	if astDoc.Body != nil {
		bodyBytes, err := astDoc.Body.MarshalYAML()
		if err != nil {
			return Document{}, fmt.Errorf("%w: marshalling body: %s", ErrMalformedYAML, err)
		}
		if err := goyaml.Unmarshal(bodyBytes, &body); err != nil {
			return Document{}, fmt.Errorf("%w: unmarshalling body: %s", ErrMalformedYAML, err)
		}
	}

	// Build the position map by walking the AST.
	nodes := make(PositionMap)
	if astDoc.Body != nil {
		walkNode(astDoc.Body, "", nodes)
	}

	// Extract apiVersion and kind from the decoded map.
	apiVersion, _ := body["apiVersion"].(string)
	kind, _ := body["kind"].(string)

	return Document{
		Index:      index,
		StartLine:  startLine,
		APIVersion: apiVersion,
		Kind:       kind,
		Body:       body,
		Nodes:      nodes,
	}, nil
}

// walkNode recursively walks an AST node rooted at node, recording the
// source position of each map key under its JSON Pointer path into pm.
// pointer is the current JSON Pointer prefix (empty for the root).
func walkNode(node ast.Node, pointer string, pm PositionMap) {
	if node == nil {
		return
	}

	switch n := node.(type) {
	case *ast.MappingValueNode:
		// A single key: value pair (used when the doc body is a single
		// mapping value rather than a MappingNode).
		keyStr := mappingKeyString(n.Key)
		childPointer := pointer + "/" + escapePointerSegment(keyStr)
		if tok := n.Key.GetToken(); tok != nil && tok.Position != nil {
			pm[childPointer] = Position{
				Line:   tok.Position.Line,
				Column: tok.Position.Column,
			}
		}
		walkNode(n.Value, childPointer, pm)

	case *ast.MappingNode:
		for _, mv := range n.Values {
			walkNode(mv, pointer, pm)
		}

	case *ast.SequenceNode:
		// Use Entries when available (each Entry.Start is the `-` token).
		if len(n.Entries) > 0 {
			for i, entry := range n.Entries {
				childPointer := fmt.Sprintf("%s/%d", pointer, i)
				if entry.Start != nil && entry.Start.Position != nil {
					pm[childPointer] = Position{
						Line:   entry.Start.Position.Line,
						Column: entry.Start.Position.Column,
					}
				}
				walkNode(entry.Value, childPointer, pm)
			}
		} else {
			// Fallback for flow-style sequences where Entries may be empty.
			for i, elem := range n.Values {
				childPointer := fmt.Sprintf("%s/%d", pointer, i)
				if tok := elem.GetToken(); tok != nil && tok.Position != nil {
					pm[childPointer] = Position{
						Line:   tok.Position.Line,
						Column: tok.Position.Column,
					}
				}
				walkNode(elem, childPointer, pm)
			}
		}

	default:
		// Scalars, null, bool, etc. — no children to record.
	}
}

// mappingKeyString returns the string representation of a map key node.
func mappingKeyString(key ast.MapKeyNode) string {
	if key == nil {
		return ""
	}
	return strings.TrimSpace(key.String())
}

// escapePointerSegment escapes a JSON Pointer segment per RFC 6901:
// '~' → "~0", '/' → "~1".
func escapePointerSegment(s string) string {
	s = strings.ReplaceAll(s, "~", "~0")
	s = strings.ReplaceAll(s, "/", "~1")
	return s
}

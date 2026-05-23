// Package validator wraps github.com/santhosh-tekuri/jsonschema/v6 to provide
// a compile-once-validate-many cache keyed by schema integrity string.
package validator

import (
	"bytes"
	"fmt"
	"sync"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

// Compiler caches compiled JSON Schema 2020-12 objects keyed by their SRI
// integrity string (sha256-<base64>). The same schema bytes will only be
// compiled once per Compiler instance; subsequent Compile calls with the same
// integrity return the cached *jsonschema.Schema pointer.
//
// Compiler is safe for concurrent use.
type Compiler struct {
	mu    sync.Mutex
	cache map[string]*jsonschema.Schema // integrity → compiled schema
	draft *jsonschema.Compiler          // underlying compiler, draft fixed to 2020-12
}

// NewCompiler returns a new Compiler configured for JSON Schema draft 2020-12.
func NewCompiler() *Compiler {
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	return &Compiler{
		cache: make(map[string]*jsonschema.Schema),
		draft: c,
	}
}

// LookupCached is a read-only cache probe used by the completion and hover
// handlers. It acquires the same mutex as Compile and checks whether the schema
// for the given integrity string has already been compiled and cached.
//
// Returns (schema, true) on a cache hit; (nil, false) on a miss.
// It never calls into jsonschema.Compile or reads any bytes — callers that need
// to compile a schema for the first time must use Compile.
func (c *Compiler) LookupCached(integrity string) (*jsonschema.Schema, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	sch, ok := c.cache[integrity]
	return sch, ok
}

// Compile returns the compiled JSON Schema for the given schema bytes. The
// integrity parameter (SRI hash of the bytes, e.g. "sha256-abc…") is used as
// the cache key. If the same integrity has been compiled before, the cached
// *jsonschema.Schema is returned without recompiling.
//
// schemaBytes must be a valid JSON document (JSON Schema 2020-12).
// Returns ErrSchemaCompile on parse or compilation failure.
func (c *Compiler) Compile(integrity string, schemaBytes []byte) (*jsonschema.Schema, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if sch, ok := c.cache[integrity]; ok {
		return sch, nil
	}

	// Parse the raw schema bytes into a JSON value that the compiler understands.
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schemaBytes))
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrSchemaCompile, err)
	}

	// Use the integrity as a synthetic URL to identify this resource.
	// The URL need not be resolvable — it is used only as an identifier
	// within the in-memory compiler.
	syntheticURL := "schemalock://cache/" + integrity

	if err := c.draft.AddResource(syntheticURL, doc); err != nil {
		// ResourceExistsError is returned if we re-add the same URL after a
		// cache miss (should not happen, but guard defensively).
		return nil, fmt.Errorf("%w: adding resource: %s", ErrSchemaCompile, err)
	}

	sch, err := c.draft.Compile(syntheticURL)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrSchemaCompile, err)
	}

	c.cache[integrity] = sch
	return sch, nil
}

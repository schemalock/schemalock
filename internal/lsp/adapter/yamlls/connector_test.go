package yamlls_test

import (
	"testing"

	"github.com/schemalock/app/internal/lsp/adapter/yamlls"
)

// TestEmptyConnector_NoPathReturnsNoOp verifies that NewConnector with an empty
// Path returns an empty Connector whose Call* methods are all safe no-ops.
func TestEmptyConnector_NoPathReturnsNoOp(t *testing.T) {
	t.Parallel()
	c := yamlls.NewConnector(t.Context(), yamlls.Config{Path: ""})
	if c == nil {
		t.Fatal("NewConnector returned nil; want non-nil empty *Connector")
	}
}

// TestEmptyConnector_DocumentSyncNoOps exercises the fan-out document sync
// methods on an empty connector and asserts they do not panic.
func TestEmptyConnector_DocumentSyncNoOps(t *testing.T) {
	t.Parallel()
	c := yamlls.NewConnector(t.Context(), yamlls.Config{Path: ""})

	// None of these should panic or return an error.
	c.DocumentDidOpen(nil)
	c.DocumentDidChange(nil)
	c.DocumentDidClose(nil)
}

// TestEmptyConnector_CallMethodsReturnNil verifies that all Call* methods on an
// empty connector return (nil, nil) without panicking.
func TestEmptyConnector_CallMethodsReturnNil(t *testing.T) {
	t.Parallel()
	c := yamlls.NewConnector(t.Context(), yamlls.Config{Path: ""})

	hover, err := c.CallHover(t.Context(), nil)
	if hover != nil || err != nil {
		t.Errorf("CallHover on empty connector: got (%v, %v), want (nil, nil)", hover, err)
	}

	completion, err := c.CallCompletion(t.Context(), nil)
	if completion != nil || err != nil {
		t.Errorf("CallCompletion on empty connector: got (%v, %v), want (nil, nil)", completion, err)
	}

	def, err := c.CallDefinition(t.Context(), nil)
	if def != nil || err != nil {
		t.Errorf("CallDefinition on empty connector: got (%v, %v), want (nil, nil)", def, err)
	}

	refs, err := c.CallReferences(t.Context(), nil)
	if refs != nil || err != nil {
		t.Errorf("CallReferences on empty connector: got (%v, %v), want (nil, nil)", refs, err)
	}

	syms, err := c.CallDocumentSymbol(t.Context(), nil)
	if syms != nil || err != nil {
		t.Errorf("CallDocumentSymbol on empty connector: got (%v, %v), want (nil, nil)", syms, err)
	}
}

// TestEmptyConnector_ShutdownIdempotent verifies that Shutdown on an empty
// connector is a no-op that returns nil.
func TestEmptyConnector_ShutdownIdempotent(t *testing.T) {
	t.Parallel()
	c := yamlls.NewConnector(t.Context(), yamlls.Config{Path: ""})
	if err := c.Shutdown(t.Context()); err != nil {
		t.Errorf("Shutdown on empty connector returned error: %v", err)
	}
	// Second call must also be safe.
	if err := c.Shutdown(t.Context()); err != nil {
		t.Errorf("second Shutdown on empty connector returned error: %v", err)
	}
}

// TestEmptyConnector_CallInitializeNoop verifies that CallInitialize on an
// empty connector returns nil (no crash).
func TestEmptyConnector_CallInitializeNoop(t *testing.T) {
	t.Parallel()
	c := yamlls.NewConnector(t.Context(), yamlls.Config{Path: ""})
	if err := c.CallInitialize(t.Context(), nil); err != nil {
		t.Errorf("CallInitialize on empty connector returned error: %v", err)
	}
}

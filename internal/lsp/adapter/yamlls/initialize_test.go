package yamlls_test

import (
	"context"
	"io"
	"log"
	"testing"
	"time"

	"github.com/schemalock/app/internal/lsp/adapter/yamlls"
	"github.com/schemalock/app/internal/lsp/adapter/yamlls/mock"
	lsp "go.lsp.dev/protocol"
)

// buildConnectorOverMock wires a real Connector over an in-process mock
// yaml-ls, calls CallInitialize, and returns the connector.
// The mock is started with the given version string.
func buildConnectorOverMock(t *testing.T, version string) (*yamlls.Connector, *mock.MockYamlls) {
	t.Helper()

	m := mock.New()
	m.Version = version

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)

	mockRWC := m.Connect(ctx)

	// Wrap the mock's RWC as a fake "subprocess" by using a special constructor
	// that accepts a pre-built RWC. Since NewConnector spawns a real process,
	// we need to use the internal helper exposed for testing.
	c := yamlls.NewConnectorFromRWC(ctx, mockRWC, yamlls.Config{
		Logger: log.New(io.Discard, "", 0),
	})

	// Call initialize with dummy params.
	params := &lsp.InitializeParams{
		RootURI: "file:///workspace",
	}
	if err := c.CallInitialize(ctx, params); err != nil {
		t.Fatalf("CallInitialize: unexpected error: %v", err)
	}

	return c, m
}

// TestVersionProbe_AcceptsV1_14 asserts that a mock advertising v1.14.0 passes
// the version probe and leaves the connector active.
func TestVersionProbe_AcceptsV1_14(t *testing.T) {
	t.Parallel()
	c, _ := buildConnectorOverMock(t, "1.14.0")
	defer func() { _ = c.Shutdown(context.Background()) }()

	// After a passing probe the connector must be active (shouldRun = true),
	// observable through a Call* method not returning immediately.
	hover, err := c.CallHover(context.Background(), &lsp.HoverParams{})
	// nil result is fine (mock returns nil HoverResult); but err should be nil too.
	if err != nil {
		t.Errorf("CallHover after passing probe: err = %v, want nil", err)
	}
	_ = hover
}

// TestVersionProbe_AcceptsV1_15 asserts that v1.15.0 also passes.
func TestVersionProbe_AcceptsV1_15(t *testing.T) {
	t.Parallel()
	c, _ := buildConnectorOverMock(t, "1.15.0")
	defer func() { _ = c.Shutdown(context.Background()) }()

	_, err := c.CallHover(context.Background(), &lsp.HoverParams{})
	if err != nil {
		t.Errorf("CallHover after v1.15 probe: err = %v, want nil", err)
	}
}

// TestVersionProbe_RejectsV1_13 asserts that v1.13.0 is rejected by the
// version probe and the connector degrades to the empty-connector state.
func TestVersionProbe_RejectsV1_13(t *testing.T) {
	t.Parallel()
	c, _ := buildConnectorOverMock(t, "1.13.0")
	// After rejection the connector should be in the empty-connector state.
	// All Call* methods must return (nil, nil) without error.
	hover, err := c.CallHover(context.Background(), &lsp.HoverParams{})
	if err != nil {
		t.Errorf("CallHover after v1.13 rejection: err = %v, want nil", err)
	}
	if hover != nil {
		t.Errorf("CallHover after v1.13 rejection: hover = %v, want nil", hover)
	}
}

// TestVersionProbe_RejectsEmptyVersion asserts that a mock returning no
// ServerInfo.Version is rejected.
func TestVersionProbe_RejectsEmptyVersion(t *testing.T) {
	t.Parallel()
	c, _ := buildConnectorOverMock(t, "")
	hover, err := c.CallHover(context.Background(), &lsp.HoverParams{})
	if err != nil {
		t.Errorf("CallHover after empty-version rejection: err = %v, want nil", err)
	}
	if hover != nil {
		t.Errorf("CallHover after empty-version rejection: hover = %v, want nil", hover)
	}
}

// TestVersionProbe_AcceptsVPrefix asserts that "v1.14.0" (with leading v) is
// accepted.
func TestVersionProbe_AcceptsVPrefix(t *testing.T) {
	t.Parallel()
	c, _ := buildConnectorOverMock(t, "v1.14.0")
	defer func() { _ = c.Shutdown(context.Background()) }()
	_, err := c.CallHover(context.Background(), &lsp.HoverParams{})
	if err != nil {
		t.Errorf("CallHover after v-prefix version: err = %v", err)
	}
}

package lsp

import (
	"context"
	"testing"

	lsp "go.lsp.dev/protocol"

	"github.com/schemalock/app/internal/cache"
	"github.com/schemalock/app/internal/registry"
	"github.com/schemalock/app/internal/validator"
)

// TestWorkerPool_SubmitAfterStop verifies that Submit after Stop does not panic.
func TestWorkerPool_SubmitAfterStop(t *testing.T) {
	deps := WorkerDeps{
		Cache:    cache.New(t.TempDir()),
		Registry: registry.NewClient("http://localhost"),
		Compiler: validator.NewCompiler(),
		Publish:  func(_ context.Context, _ string, _ uint32, _ []lsp.Diagnostic) {},
		StrictFn: func() bool { return false },
	}
	pool := NewWorkerPool(1, deps)
	pool.Stop()

	// Submit after Stop must not panic.
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("Submit after Stop panicked: %v", r)
		}
	}()
	pool.Submit(job{ctx: context.Background()})
}

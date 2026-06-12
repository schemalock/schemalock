package lsp

import (
	"context"
	"sync"
	"testing"

	lsp "go.lsp.dev/protocol"

	"github.com/schemalock/schemalock/internal/cache"
	"github.com/schemalock/schemalock/internal/registry"
	"github.com/schemalock/schemalock/internal/validator"
)

func newTestDeps(t *testing.T) WorkerDeps {
	t.Helper()
	return WorkerDeps{
		Cache:    cache.New(t.TempDir()),
		Registry: registry.NewClient("http://localhost"),
		Compiler: validator.NewCompiler(),
		Publish:  func(_ context.Context, _ string, _ uint32, _ []lsp.Diagnostic) {},
		StrictFn: func() bool { return false },
	}
}

// TestWorkerPool_SubmitAfterStop verifies that Submit after Stop does not panic.
func TestWorkerPool_SubmitAfterStop(t *testing.T) {
	pool := NewWorkerPool(1, newTestDeps(t))
	pool.Stop()

	defer func() {
		if r := recover(); r != nil {
			t.Errorf("Submit after Stop panicked: %v", r)
		}
	}()
	pool.Submit(job{ctx: context.Background()})
}

// TestWorkerPool_SubmitConcurrentWithStop verifies that goroutines calling
// Submit concurrently with Stop neither panic nor deadlock.
func TestWorkerPool_SubmitConcurrentWithStop(t *testing.T) {
	pool := NewWorkerPool(2, newTestDeps(t))

	const goroutines = 50
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for range goroutines {
		go func() {
			defer wg.Done()
			pool.Submit(job{ctx: context.Background()})
		}()
	}
	pool.Stop()
	wg.Wait()
}

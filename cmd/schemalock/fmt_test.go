package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestRunFmt_canonicalizesRoot(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "schemalock.yaml")
	input := `version: 1
ecosystems:
  kubernetes:
    - operator.victoriametrics.com@0.70.0
    - cert-manager.io@v1.16.1
  github-actions:
    - actions/checkout@v4.1.2
`
	if err := os.WriteFile(path, []byte(input), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if err := runFmt(context.Background(), []string{"--file", path}, &stdout, &stderr); err != nil {
		t.Fatalf("runFmt: %v (stderr=%s)", err, stderr.String())
	}

	got, _ := os.ReadFile(path)
	want := `version: 1
ecosystems:
  github-actions:
    - actions/checkout@v4.1.2
  kubernetes:
    - cert-manager.io@v1.16.1
    - operator.victoriametrics.com@0.70.0
`
	if string(got) != want {
		t.Errorf("file mismatch.\nGot:\n%s\nWant:\n%s", got, want)
	}
}

func TestRunFmt_canonicalizesOverlay(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "schemalock.yaml")
	input := `ecosystems:
  kubernetes:
    - operator.victoriametrics.com@0.69.0
`
	if err := os.WriteFile(path, []byte(input), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if err := runFmt(context.Background(), []string{"--file", path}, &stdout, &stderr); err != nil {
		t.Fatalf("runFmt: %v", err)
	}

	got, _ := os.ReadFile(path)
	if string(got) != input {
		t.Errorf("overlay should be unchanged.\nGot:\n%s\nWant:\n%s", got, input)
	}
}

func TestRunFmt_missingFileIsUsageError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := runFmt(context.Background(), []string{"--file", "/nonexistent/schemalock.yaml"}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected usage error, got nil")
	}
}

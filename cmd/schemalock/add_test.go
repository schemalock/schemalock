package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func startVersionsCDN(t *testing.T, versions map[string][]string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	for group, vs := range versions {
		vs := vs
		mux.HandleFunc("/kubernetes/"+group+"/versions.json",
			func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte("["))
				for i, v := range vs {
					if i > 0 {
						_, _ = w.Write([]byte(","))
					}
					_, _ = w.Write([]byte("\"" + v + "\""))
				}
				_, _ = w.Write([]byte("]"))
			})
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestRunAdd_appendsNewSpec_intoNearestExisting(t *testing.T) {
	cdn := startVersionsCDN(t, map[string][]string{
		"operator.victoriametrics.com": {"0.70.0"},
	})

	root := t.TempDir()
	rootIntent := filepath.Join(root, "schemalock.yaml")
	if err := os.WriteFile(rootIntent, []byte(`version: 1
ecosystems:
  kubernetes:
    - cert-manager.io@v1.16.1
`), 0o644); err != nil {
		t.Fatal(err)
	}

	deep := filepath.Join(root, "teamA", "k8s")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	err := runAdd(context.Background(),
		[]string{"--cwd-for-test", deep, "--registry", cdn.URL, "operator.victoriametrics.com@0.70.0"},
		&stdout, &stderr)
	if err != nil {
		t.Fatalf("runAdd: %v (stderr=%s)", err, stderr.String())
	}

	got, _ := os.ReadFile(rootIntent)
	if !strings.Contains(string(got), "operator.victoriametrics.com@0.70.0") {
		t.Errorf("new spec not in root intent file.\nFile:\n%s", got)
	}
}

func TestRunAdd_replacesExistingNameVersion(t *testing.T) {
	cdn := startVersionsCDN(t, map[string][]string{
		"operator.victoriametrics.com": {"0.70.0"},
	})
	root := t.TempDir()
	rootIntent := filepath.Join(root, "schemalock.yaml")
	if err := os.WriteFile(rootIntent, []byte(`version: 1
ecosystems:
  kubernetes:
    - operator.victoriametrics.com@0.69.0
`), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if err := runAdd(context.Background(),
		[]string{"--cwd-for-test", root, "--registry", cdn.URL, "operator.victoriametrics.com@0.70.0"},
		&stdout, &stderr); err != nil {
		t.Fatalf("runAdd: %v", err)
	}
	got, _ := os.ReadFile(rootIntent)
	if strings.Contains(string(got), "0.69.0") {
		t.Errorf("old version not removed.\nFile:\n%s", got)
	}
	if !strings.Contains(string(got), "0.70.0") {
		t.Errorf("new version not present.\nFile:\n%s", got)
	}
}

func TestRunAdd_duplicateIsNoop(t *testing.T) {
	cdn := startVersionsCDN(t, map[string][]string{
		"operator.victoriametrics.com": {"0.70.0"},
	})
	root := t.TempDir()
	rootIntent := filepath.Join(root, "schemalock.yaml")
	if err := os.WriteFile(rootIntent, []byte(`version: 1
ecosystems:
  kubernetes:
    - operator.victoriametrics.com@0.70.0
`), 0o644); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(rootIntent)

	var stdout, stderr bytes.Buffer
	if err := runAdd(context.Background(),
		[]string{"--cwd-for-test", root, "--registry", cdn.URL, "operator.victoriametrics.com@0.70.0"},
		&stdout, &stderr); err != nil {
		t.Fatalf("runAdd: %v", err)
	}
	after, _ := os.ReadFile(rootIntent)
	if string(before) != string(after) {
		t.Errorf("file changed for duplicate.\nBefore:\n%s\nAfter:\n%s", before, after)
	}
}

func TestRunAdd_explicitFileTarget(t *testing.T) {
	root := t.TempDir()
	rootIntent := filepath.Join(root, "schemalock.yaml")
	if err := os.WriteFile(rootIntent, []byte(`version: 1
ecosystems:
  kubernetes: []
`), 0o644); err != nil {
		t.Fatal(err)
	}
	overlayPath := filepath.Join(root, "teamA", "schemalock.yaml")
	if err := os.MkdirAll(filepath.Dir(overlayPath), 0o755); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	err := runAdd(context.Background(),
		[]string{"--file", overlayPath, "--no-validate", "operator.victoriametrics.com@0.69.0"},
		&stdout, &stderr)
	if err != nil {
		t.Fatalf("runAdd: %v", err)
	}
	got, err := os.ReadFile(overlayPath)
	if err != nil {
		t.Fatalf("overlay file not created: %v", err)
	}
	if !strings.Contains(string(got), "0.69.0") {
		t.Errorf("overlay file missing new spec:\n%s", got)
	}
	if strings.Contains(string(got), "version:") {
		t.Errorf("overlay file unexpectedly has version field:\n%s", got)
	}
}

func TestRunAdd_versionNotPublishedIsUsageError(t *testing.T) {
	cdn := startVersionsCDN(t, map[string][]string{
		"operator.victoriametrics.com": {"0.69.0"},
	})
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "schemalock.yaml"), []byte(`version: 1
ecosystems:
  kubernetes: []
`), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	err := runAdd(context.Background(),
		[]string{"--cwd-for-test", root, "--registry", cdn.URL, "operator.victoriametrics.com@0.70.0"},
		&stdout, &stderr)
	if err == nil {
		t.Fatal("expected usage error for unpublished version")
	}
}

func TestRunAdd_malformedSpecIsUsageError(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "schemalock.yaml"), []byte(`version: 1
ecosystems:
  kubernetes: []
`), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	err := runAdd(context.Background(),
		[]string{"--cwd-for-test", root, "--no-validate", "no-at-sign"},
		&stdout, &stderr)
	if err == nil {
		t.Fatal("expected usage error for malformed spec")
	}
}

package main

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/schemalock/app/internal/registry"
)

// startSchemaCDN serves versions.json + manifest.json + per-kind schemas for
// the operator.victoriametrics.com group at versions 0.70.0 and 0.69.0.
// Schema is permissive (`type: object`) — every object validates.
func startSchemaCDN(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	// Schema requires top-level "metadata" object and declares the known
	// top-level fields (apiVersion, kind, metadata, spec) so that strict-mode
	// (additionalProperties: false) does not reject them. Unknown fields such
	// as test-injected typos will still be rejected by WrapStrict.
	permissive := []byte(`{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","required":["metadata"],"properties":{"apiVersion":{"type":"string"},"kind":{"type":"string"},"metadata":{"type":"object"},"spec":{"type":"object"}}}`)
	integrity := registry.ComputeIntegrity(permissive)
	size := len(permissive)

	mux.HandleFunc("/kubernetes/operator.victoriametrics.com/versions.json",
		func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`["0.70.0","0.69.0"]`))
		})
	for _, v := range []string{"0.70.0", "0.69.0"} {
		mux.HandleFunc("/kubernetes/operator.victoriametrics.com/"+v+"/manifest.json",
			func(w http.ResponseWriter, r *http.Request) {
				body := fmt.Sprintf(`{"version":1,"kinds":{"VMCluster":{"integrity":%q,"size":%d}}}`,
					integrity, size)
				_, _ = w.Write([]byte(body))
			})
		mux.HandleFunc("/kubernetes/operator.victoriametrics.com/"+v+"/VMCluster.json",
			func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write(permissive)
			})
	}

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestRunVerify_validatesAllManifestsUnderPath(t *testing.T) {
	srv := startSchemaCDN(t)
	root := copyFixture(t, "testdata/repo_hierarchy")

	var stdout, stderr bytes.Buffer
	err := runVerify(context.Background(),
		[]string{"--path", root, "--registry", srv.URL, "--cache-dir", t.TempDir()},
		&stdout, &stderr)
	if err != nil {
		t.Fatalf("runVerify: %v (stderr=%s; stdout=%s)", err, stderr.String(), stdout.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "manifests/vm.yaml") {
		t.Errorf("missing root-pin file in output: %s", out)
	}
	if !strings.Contains(out, "teamA/vm.yaml") {
		t.Errorf("missing overlay-pin file in output: %s", out)
	}
}

func TestRunVerify_failsOnInvalidManifest(t *testing.T) {
	srv := startSchemaCDN(t)
	root := copyFixture(t, "testdata/repo_hierarchy")

	// Manifest missing required `metadata` field — schema rejects it.
	if err := os.WriteFile(filepath.Join(root, "teamA", "vm.yaml"),
		[]byte(`apiVersion: operator.victoriametrics.com/v1beta1
kind: VMCluster
spec:
  retentionPeriod: "1d"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	err := runVerify(context.Background(),
		[]string{"--path", root, "--registry", srv.URL, "--cache-dir", t.TempDir()},
		&stdout, &stderr)
	if err == nil {
		t.Fatalf("runVerify should fail; stdout=%s", stdout.String())
	}
}

func TestRunVerify_unpinnedKindIsWarningNotError(t *testing.T) {
	srv := startSchemaCDN(t)
	// A directory with no schemalock.yaml anywhere in its parent chain (use
	// a t.TempDir below /tmp so the walker walks to / and finds nothing).
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "vm.yaml"),
		[]byte(`apiVersion: operator.victoriametrics.com/v1beta1
kind: VMCluster
metadata: { name: x }
`), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	err := runVerify(context.Background(),
		[]string{"--path", outside, "--registry", srv.URL, "--cache-dir", t.TempDir()},
		&stdout, &stderr)
	if err != nil {
		t.Errorf("unpinned manifest should warn, not error: %v (stdout=%s)", err, stdout.String())
	}
	if !strings.Contains(stdout.String(), "warn") {
		t.Errorf("expected 'warn' in output for unpinned manifest; stdout=%s", stdout.String())
	}
}

func TestRunVerify_strictPinnedFailsOnUnpinned(t *testing.T) {
	srv := startSchemaCDN(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "vm.yaml"),
		[]byte(`apiVersion: operator.victoriametrics.com/v1beta1
kind: VMCluster
metadata: { name: x }
`), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	err := runVerify(context.Background(),
		[]string{"--path", outside, "--registry", srv.URL, "--strict-pinned", "--cache-dir", t.TempDir()},
		&stdout, &stderr)
	if err == nil {
		t.Fatal("--strict-pinned should fail on an unpinned manifest")
	}
}

func TestRunVerify_strictMode(t *testing.T) {
	// Manifest contains an unknown field at the top level. The CDN
	// schema served by startSchemaCDN already has properties.metadata,
	// so an unknown sibling of "metadata" trips additionalProperties.
	manifest := []byte(`apiVersion: operator.victoriametrics.com/v1beta1
kind: VMCluster
metadata: { name: x }
unknownTypoField: oops
`)

	cases := []struct {
		name      string
		extraArgs []string
		wantErr   bool
	}{
		{name: "default rejects unknown field", extraArgs: nil, wantErr: true},
		{name: "--no-strict accepts unknown field", extraArgs: []string{"--no-strict"}, wantErr: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := startSchemaCDN(t)
			root := copyFixture(t, "testdata/repo_hierarchy")
			if err := os.WriteFile(filepath.Join(root, "teamA", "vm.yaml"), manifest, 0o644); err != nil {
				t.Fatal(err)
			}

			args := []string{"--path", root, "--registry", srv.URL, "--cache-dir", t.TempDir()}
			args = append(args, tc.extraArgs...)

			var stdout, stderr bytes.Buffer
			err := runVerify(context.Background(), args, &stdout, &stderr)
			if tc.wantErr && err == nil {
				t.Fatalf("expected verify to fail; stdout=%s", stdout.String())
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("expected verify to pass; err=%v stdout=%s", err, stdout.String())
			}
		})
	}
}

// copyFixture copies a directory tree from testdata to a temp dir.
func copyFixture(t *testing.T, srcRel string) string {
	t.Helper()
	dst := t.TempDir()
	src, err := filepath.Abs(srcRel)
	if err != nil {
		t.Fatal(err)
	}
	if err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o644)
	}); err != nil {
		t.Fatal(err)
	}
	return dst
}

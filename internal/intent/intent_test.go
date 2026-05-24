package intent

import (
	"errors"
	"testing"
)

func TestDecodeRoot_minimalValid(t *testing.T) {
	src := []byte(`version: 1
ecosystems:
  kubernetes:
    - operator.victoriametrics.com@0.70.0
    - cert-manager.io@v1.16.1
`)
	got, err := DecodeRoot(src)
	if err != nil {
		t.Fatalf("DecodeRoot: %v", err)
	}
	if got.Version != 1 {
		t.Errorf("Version = %d, want 1", got.Version)
	}
	if len(got.Ecosystems["kubernetes"]) != 2 {
		t.Errorf("kubernetes ecosystem = %v, want 2 specs", got.Ecosystems["kubernetes"])
	}
	if got.Root {
		t.Errorf("Root = true unexpectedly (no field set)")
	}
}

func TestDecodeOverlay_acceptsEcosystemsOnly(t *testing.T) {
	src := []byte(`ecosystems:
  kubernetes:
    - operator.victoriametrics.com@0.69.0
`)
	got, err := DecodeOverlay(src)
	if err != nil {
		t.Fatalf("DecodeOverlay: %v", err)
	}
	if len(got.Ecosystems["kubernetes"]) != 1 {
		t.Errorf("kubernetes = %v, want 1 spec", got.Ecosystems["kubernetes"])
	}
}

func TestDecodeOverlay_rejectsVersionField(t *testing.T) {
	src := []byte(`version: 1
ecosystems:
  kubernetes:
    - operator.victoriametrics.com@0.69.0
`)
	_, err := DecodeOverlay(src)
	if err == nil {
		t.Fatal("DecodeOverlay accepted version field in overlay; want ErrMalformedIntent")
	}
	if !errors.Is(err, ErrMalformedIntent) {
		t.Errorf("err = %v, want ErrMalformedIntent", err)
	}
}

func TestDecodeOverlay_rejectsRootField(t *testing.T) {
	src := []byte(`root: true
ecosystems:
  kubernetes: []
`)
	_, err := DecodeOverlay(src)
	if err == nil {
		t.Fatal("DecodeOverlay accepted root field in overlay; want ErrMalformedIntent")
	}
}

func TestEncodeIntent_canonicalizesRoot(t *testing.T) {
	in := IntentFile{
		Version: 1,
		Ecosystems: map[string][]string{
			"kubernetes": {
				"operator.victoriametrics.com@0.70.0",
				"cert-manager.io@v1.16.1",
			},
			"github-actions": {
				"actions/checkout@v4.1.2",
			},
		},
	}
	got, err := EncodeIntent(in)
	if err != nil {
		t.Fatalf("EncodeIntent: %v", err)
	}
	want := `version: 1
ecosystems:
  github-actions:
    - actions/checkout@v4.1.2
  kubernetes:
    - cert-manager.io@v1.16.1
    - operator.victoriametrics.com@0.70.0
`
	if string(got) != want {
		t.Errorf("EncodeIntent mismatch.\nGot:\n%s\nWant:\n%s", got, want)
	}
}

func TestEncodeIntent_overlay(t *testing.T) {
	in := IntentFile{
		Ecosystems: map[string][]string{
			"kubernetes": {"operator.victoriametrics.com@0.69.0"},
		},
	}
	got, err := EncodeIntent(in)
	if err != nil {
		t.Fatalf("EncodeIntent: %v", err)
	}
	want := `ecosystems:
  kubernetes:
    - operator.victoriametrics.com@0.69.0
`
	if string(got) != want {
		t.Errorf("EncodeIntent overlay mismatch.\nGot:\n%s\nWant:\n%s", got, want)
	}
}

func TestEncodeIntent_rootWithFlag(t *testing.T) {
	in := IntentFile{
		Version: 1,
		Root:    true,
		Ecosystems: map[string][]string{
			"kubernetes": {"argoproj.io@v2.10.0"},
		},
	}
	got, err := EncodeIntent(in)
	if err != nil {
		t.Fatalf("EncodeIntent: %v", err)
	}
	want := `version: 1
root: true
ecosystems:
  kubernetes:
    - argoproj.io@v2.10.0
`
	if string(got) != want {
		t.Errorf("EncodeIntent root+flag mismatch.\nGot:\n%s\nWant:\n%s", got, want)
	}
}

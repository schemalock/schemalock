package intent

import (
	"reflect"
	"testing"
)

func TestMerge_singleRoot(t *testing.T) {
	chain := []LoadedFile{
		{Path: "/repo/schemalock.yaml", File: IntentFile{
			Version: 1,
			Ecosystems: map[string][]string{
				"kubernetes": {
					"cert-manager.io@v1.16.1",
					"operator.victoriametrics.com@0.70.0",
				},
			},
		}},
	}
	got, err := Merge(chain)
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	want := IntentSet{
		"kubernetes": {
			"cert-manager.io":              "v1.16.1",
			"operator.victoriametrics.com": "0.70.0",
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Merge mismatch.\nGot:  %v\nWant: %v", got, want)
	}
}

func TestMerge_overlayAddsAndOverrides(t *testing.T) {
	chain := []LoadedFile{
		{Path: "/repo/schemalock.yaml", File: IntentFile{
			Version: 1,
			Ecosystems: map[string][]string{
				"kubernetes": {
					"cert-manager.io@v1.16.1",
					"operator.victoriametrics.com@0.70.0",
				},
			},
		}},
		{Path: "/repo/teamA/schemalock.yaml", File: IntentFile{
			Ecosystems: map[string][]string{
				"kubernetes": {
					"operator.victoriametrics.com@0.69.0", // overrides
					"external-secrets.io@0.10.0",          // adds
				},
			},
		}},
	}
	got, err := Merge(chain)
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	want := IntentSet{
		"kubernetes": {
			"cert-manager.io":              "v1.16.1",
			"operator.victoriametrics.com": "0.69.0", // overlay wins
			"external-secrets.io":          "0.10.0",
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Merge mismatch.\nGot:  %v\nWant: %v", got, want)
	}
}

func TestMerge_emptyChain(t *testing.T) {
	got, err := Merge(nil)
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("empty chain yielded %v, want empty IntentSet", got)
	}
}

func TestMerge_rejectsDuplicateNameInSameFile(t *testing.T) {
	chain := []LoadedFile{
		{Path: "/repo/schemalock.yaml", File: IntentFile{
			Version: 1,
			Ecosystems: map[string][]string{
				"kubernetes": {
					"foo@1.0.0",
					"foo@2.0.0", // duplicate name in same file
				},
			},
		}},
	}
	_, err := Merge(chain)
	if err == nil {
		t.Fatal("expected error on duplicate name within a single file")
	}
}

func TestMerge_malformedSpecIsError(t *testing.T) {
	chain := []LoadedFile{
		{Path: "/repo/schemalock.yaml", File: IntentFile{
			Version: 1,
			Ecosystems: map[string][]string{
				"kubernetes": {"no-at-sign-here"},
			},
		}},
	}
	_, err := Merge(chain)
	if err == nil {
		t.Fatal("expected error on malformed spec")
	}
}

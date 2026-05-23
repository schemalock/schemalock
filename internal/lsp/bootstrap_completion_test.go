package lsp

import (
	"fmt"
	"testing"

	lspprot "go.lsp.dev/protocol"
	"github.com/schemalock/app/internal/lockfile"
	"github.com/schemalock/app/internal/yamldoc"
)

// bootstrapFixtureLockFile builds a LockFile with two groups, each having two
// kinds, for use in bootstrap completion unit tests.
//
//	operator.victoriametrics.com → VMAgent, VMCluster
//	cert-manager.io              → Certificate, ClusterIssuer
func bootstrapFixtureLockFile() lockfile.LockFile {
	return lockfile.LockFile{
		Version:     1,
		Registry:    "https://cdn.schemalock.dev",
		GeneratedAt: "2026-01-01T00:00:00Z",
		Entries: []lockfile.LockEntry{
			{
				ID:   "kubernetes/operator.victoriametrics.com@0.70.0",
				Base: "kubernetes/operator.victoriametrics.com/0.70.0/",
				Schemas: map[string]lockfile.SchemaEntry{
					"VMCluster": {Integrity: "sha256-aaa", Size: 100},
					"VMAgent":   {Integrity: "sha256-bbb", Size: 200},
				},
			},
			{
				ID:   "kubernetes/cert-manager.io@1.12.0",
				Base: "kubernetes/cert-manager.io/1.12.0/",
				Schemas: map[string]lockfile.SchemaEntry{
					"Certificate":   {Integrity: "sha256-ccc", Size: 300},
					"ClusterIssuer": {Integrity: "sha256-ddd", Size: 400},
				},
			},
		},
	}
}

// makeDoc is a small helper that builds a yamldoc.Document with just
// APIVersion and Kind set (no Nodes map needed for bootstrap tests that only
// check doc.APIVersion / doc.Kind / cursorCtx).
func makeDoc(apiVersion, kind string) yamldoc.Document {
	return yamldoc.Document{
		APIVersion: apiVersion,
		Kind:       kind,
	}
}

// TestBootstrapKindCompletions is a table-driven test for bootstrapKindCompletions.
// The helper is unexported, so this test lives in the `lsp` (white-box) package.
func TestBootstrapKindCompletions(t *testing.T) {
	lf := bootstrapFixtureLockFile()
	r, err := NewSchemaResolver(lf)
	if err != nil {
		t.Fatalf("NewSchemaResolver: %v", err)
	}

	// Standard trigger context: cursor at value of root-level kind.
	kindValueCtx := cursorContext{
		Pointer:       "/kind",
		ParentPointer: "",
		IsKeyPosition: false,
		ExistingKeys:  []string{"apiVersion", "kind"},
	}
	// Key-position variant (cursor on the "kind" key itself, not its value).
	kindKeyCtx := cursorContext{
		Pointer:       "/kind",
		ParentPointer: "",
		IsKeyPosition: true,
		ExistingKeys:  []string{"apiVersion", "kind"},
	}
	// Cursor not at kind at all.
	otherCtx := cursorContext{
		Pointer:       "/spec/replicas",
		ParentPointer: "/spec",
		IsKeyPosition: false,
	}

	tests := []struct {
		name      string
		doc       yamldoc.Document
		cursorCtx cursorContext
		// wantNil: if true, expect nil return.
		// If false, validate the returned slice.
		wantNil        bool
		wantLabelOrder []string // expected sorted kind labels when not nil
		wantDetail     string
	}{
		{
			name:           "HappyPath_VMOperator",
			doc:            makeDoc("operator.victoriametrics.com/v1beta1", ""),
			cursorCtx:      kindValueCtx,
			wantNil:        false,
			wantLabelOrder: []string{"VMAgent", "VMCluster"}, // KindsForGroup sorts alphabetically
			wantDetail:     "Kind from operator.victoriametrics.com",
		},
		{
			name:           "HappyPath_CertManager",
			doc:            makeDoc("cert-manager.io/v1", ""),
			cursorCtx:      kindValueCtx,
			wantNil:        false,
			wantLabelOrder: []string{"Certificate", "ClusterIssuer"},
			wantDetail:     "Kind from cert-manager.io",
		},
		{
			name:      "UnknownGroup",
			doc:       makeDoc("unknown.example.com/v1", ""),
			cursorCtx: kindValueCtx,
			wantNil:   true,
		},
		{
			name:      "CoreType_NoSlash",
			doc:       makeDoc("v1", ""),
			cursorCtx: kindValueCtx,
			wantNil:   true,
		},
		{
			name:      "NotAtKindPosition",
			doc:       makeDoc("operator.victoriametrics.com/v1beta1", ""),
			cursorCtx: otherCtx,
			wantNil:   true,
		},
		{
			name:      "KindKeyPosition",
			doc:       makeDoc("operator.victoriametrics.com/v1beta1", ""),
			cursorCtx: kindKeyCtx,
			wantNil:   true,
		},
		{
			name:      "KindAlreadySet",
			doc:       makeDoc("operator.victoriametrics.com/v1beta1", "VMCluster"),
			cursorCtx: kindValueCtx,
			wantNil:   true,
		},
		{
			name:      "APIVersionEmpty",
			doc:       makeDoc("", ""),
			cursorCtx: kindValueCtx,
			wantNil:   true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := bootstrapKindCompletions(tc.doc, tc.cursorCtx, r)

			if tc.wantNil {
				if got != nil {
					t.Errorf("expected nil, got %v", got)
				}
				return
			}

			if got == nil {
				t.Fatal("expected non-nil slice, got nil")
			}

			// Verify label order matches sorted kinds.
			if len(got) != len(tc.wantLabelOrder) {
				t.Fatalf("len(items) = %d, want %d; items: %v",
					len(got), len(tc.wantLabelOrder), got)
			}

			for i, item := range got {
				wantLabel := tc.wantLabelOrder[i]

				if item.Label != wantLabel {
					t.Errorf("items[%d].Label = %q, want %q", i, item.Label, wantLabel)
				}
				if item.InsertText != wantLabel {
					t.Errorf("items[%d].InsertText = %q, want %q", i, item.InsertText, wantLabel)
				}
				if item.Kind != completionKindEnumMember {
					t.Errorf("items[%d].Kind = %v, want completionKindEnumMember (%v)",
						i, item.Kind, completionKindEnumMember)
				}
				wantSortText := fmt.Sprintf("%04d_%s", i, wantLabel)
				if item.SortText != wantSortText {
					t.Errorf("items[%d].SortText = %q, want %q", i, item.SortText, wantSortText)
				}
				if item.Detail != tc.wantDetail {
					t.Errorf("items[%d].Detail = %q, want %q", i, item.Detail, tc.wantDetail)
				}
				mc, ok := item.Documentation.(lspprot.MarkupContent)
				if !ok {
					t.Errorf("items[%d].Documentation type = %T, want lsp.MarkupContent", i, item.Documentation)
				} else {
					if mc.Kind != lspprot.Markdown {
						t.Errorf("items[%d].Documentation.Kind = %q, want markdown", i, mc.Kind)
					}
					if mc.Value != "_Resolved from schemalock.lock_" {
						t.Errorf("items[%d].Documentation.Value = %q, want \"_Resolved from schemalock.lock_\"",
							i, mc.Value)
					}
				}
			}
		})
	}
}

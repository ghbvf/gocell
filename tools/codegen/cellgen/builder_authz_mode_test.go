package cellgen

import (
	"strings"
	"testing"

	"github.com/ghbvf/gocell/framework/kernel/metadata"
)

// authModeProject builds a minimal project with one demo/alpha slice serving a single
// http contract whose HTTP block + id are caller-controlled, for the #2020 cellgen
// completeness gate (validateHTTPAuthModeCompleteness).
func authModeProject(id string, lifecycle string, codegen bool, h *metadata.HTTPTransportMeta) *metadata.ProjectMeta {
	return &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{},
		Slices: map[string]*metadata.SliceMeta{
			"demo/alpha": {
				ID:            "alpha",
				BelongsToCell: "demo",
				ContractUsages: []metadata.ContractUsage{
					{Contract: id, Role: roleServe, Field: "h"},
				},
			},
		},
		Contracts: map[string]*metadata.ContractMeta{
			id: {
				ID:        id,
				Kind:      "http",
				Lifecycle: lifecycle,
				Codegen:   codegen,
				Endpoints: metadata.EndpointsMeta{HTTP: h},
			},
		},
	}
}

func TestValidateHTTPAuthModeCompleteness(t *testing.T) {
	t.Parallel()
	ledgered := ""
	if ids := metadata.HTTPAuthModeLedgerIDs(); len(ids) > 0 {
		ledgered = ids[0]
	}

	cases := []struct {
		name      string
		id        string
		lifecycle string
		codegen   bool
		h         *metadata.HTTPTransportMeta
		wantErr   string // substring; "" = expect no error
	}{
		{"modeless rejected", "http.demo.modeless.v1", "active", true, &metadata.HTTPTransportMeta{}, "declares no AuthZ mode"},
		{"abac permission ok", "http.demo.abac.v1", "active", true, &metadata.HTTPTransportMeta{Permission: "config:read"}, ""},
		{
			"opt-out public with reason ok", "http.demo.pub.v1", "active", true,
			&metadata.HTTPTransportMeta{Auth: metadata.HTTPAuthMeta{Public: true, Reason: "login entrypoint"}}, "",
		},
		{
			"opt-out public missing reason", "http.demo.pubnoreason.v1", "active", true,
			&metadata.HTTPTransportMeta{Auth: metadata.HTTPAuthMeta{Public: true}},
			"must declare a non-empty endpoints.http.auth.reason",
		},
		{"draft modeless skipped", "http.demo.draft.v1", "draft", true, &metadata.HTTPTransportMeta{}, ""},
		{"non-codegen modeless skipped", "http.demo.nocodegen.v1", "active", false, &metadata.HTTPTransportMeta{}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := authModeProject(tc.id, tc.lifecycle, tc.codegen, tc.h)
			err := validateHTTPAuthModeCompleteness(p, "demo")
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("expected no error, got: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %q, want substring %q", err.Error(), tc.wantErr)
			}
		})
	}

	t.Run("modeless ledgered exempt", func(t *testing.T) {
		if ledgered == "" {
			t.Skip("ledger drained — exemption path removed at #2020 endgame")
		}
		p := authModeProject(ledgered, "active", true, &metadata.HTTPTransportMeta{})
		if err := validateHTTPAuthModeCompleteness(p, "demo"); err != nil {
			t.Fatalf("a ledgered modeless contract must pass the gate, got: %v", err)
		}
	})
}

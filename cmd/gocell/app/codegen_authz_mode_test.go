package app

import (
	"strings"
	"testing"

	"github.com/ghbvf/gocell/framework/kernel/metadata"
)

// TestValidateProjectHTTPAuthModes covers the #2020 comprehensive generate-time gate:
// it must flag any active codegen HTTP contract that violates the mandatory-mode rule —
// crucially including contracts NOT served by a cell slice (the gap cellgen's serve-scan
// alone would miss) — and aggregate all offenders in one run.
func TestValidateProjectHTTPAuthModes(t *testing.T) {
	mk := func(id string, h *metadata.HTTPTransportMeta) *metadata.ContractMeta {
		return &metadata.ContractMeta{
			ID: id, Kind: "http", Lifecycle: "active", Codegen: true,
			Endpoints: metadata.EndpointsMeta{HTTP: h},
		}
	}

	t.Run("clean project passes", func(t *testing.T) {
		p := &metadata.ProjectMeta{Contracts: map[string]*metadata.ContractMeta{
			"http.ok.v1": mk("http.ok.v1", &metadata.HTTPTransportMeta{Permission: "config:read"}),
		}}
		if err := validateProjectHTTPAuthModes(p); err != nil {
			t.Fatalf("clean project must pass, got: %v", err)
		}
	})

	t.Run("modeless contract fails even with no cell/slice (comprehensive scope)", func(t *testing.T) {
		p := &metadata.ProjectMeta{Contracts: map[string]*metadata.ContractMeta{
			"http.bad.v1": mk("http.bad.v1", &metadata.HTTPTransportMeta{}),
		}}
		err := validateProjectHTTPAuthModes(p)
		if err == nil || !strings.Contains(err.Error(), "http.bad.v1") {
			t.Fatalf("modeless contract must fail naming the offender, got: %v", err)
		}
	})

	t.Run("aggregates every violation", func(t *testing.T) {
		p := &metadata.ProjectMeta{Contracts: map[string]*metadata.ContractMeta{
			"http.a.v1": mk("http.a.v1", &metadata.HTTPTransportMeta{}),
			"http.b.v1": mk("http.b.v1", &metadata.HTTPTransportMeta{Auth: metadata.HTTPAuthMeta{Public: true}}),
		}}
		err := validateProjectHTTPAuthModes(p)
		if err == nil || !strings.Contains(err.Error(), "http.a.v1") || !strings.Contains(err.Error(), "http.b.v1") {
			t.Fatalf("must aggregate both offenders, got: %v", err)
		}
	})
}

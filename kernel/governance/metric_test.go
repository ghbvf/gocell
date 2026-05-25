package governance

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/metadata"
)

// mkDeprecatedContract builds a deprecated contract for metric tests.
func mkDeprecatedContract(id, at string) *metadata.ContractMeta {
	return &metadata.ContractMeta{
		ID: id, Kind: "event", Lifecycle: "deprecated", DeprecatedAt: at,
		File: "contracts/event/" + id + "/contract.yaml",
	}
}

// TestFMT23PerFindingMetric verifies that validateContractDeprecatedCleanup01
// sets a non-nil Metric (negative days-remaining) on the stale-contract warning
// finding and leaves Metric nil on missing-deprecatedAt / malformed-date errors.
//
// Per ADR §M3 P-C3: metric is per-finding — only the detect that emits a
// finding with an orderable distance sets ValidationResult.Metric on that
// specific finding.
func TestFMT23PerFindingMetric(t *testing.T) {
	t.Parallel()

	t.Run("stale contract → warning finding has non-nil negative Metric", func(t *testing.T) {
		// 2000-01-01 is robustly far past the 90-day grace window regardless of run date.
		v := NewValidator(&metadata.ProjectMeta{
			Contracts: map[string]*metadata.ContractMeta{
				"old": mkDeprecatedContract("old", "2000-01-01"),
			},
		}, "", clock.Real())
		results := v.validateContractDeprecatedCleanup01()
		require.Len(t, results, 1)
		assert.Equal(t, SeverityWarning, results[0].Severity)
		assert.Equal(t, codeFMT23, results[0].Code)
		require.NotNil(t, results[0].Metric, "stale-contract warning must carry a non-nil per-finding Metric")
		assert.Negative(t, *results[0].Metric, "days-remaining for an overdue contract must be negative")
	})

	t.Run("missing deprecatedAt → error finding has nil Metric", func(t *testing.T) {
		v := NewValidator(&metadata.ProjectMeta{
			Contracts: map[string]*metadata.ContractMeta{
				"nodate": {
					ID: "nodate", Kind: "event", Lifecycle: "deprecated",
					File: "contracts/event/nodate/contract.yaml",
				},
			},
		}, "", clock.Real())
		results := v.validateContractDeprecatedCleanup01()
		require.Len(t, results, 1)
		assert.Equal(t, SeverityError, results[0].Severity)
		assert.Nil(t, results[0].Metric, "missing-deprecatedAt error must carry nil Metric")
	})

	t.Run("malformed date → error finding has nil Metric", func(t *testing.T) {
		v := NewValidator(&metadata.ProjectMeta{
			Contracts: map[string]*metadata.ContractMeta{
				"bad": mkDeprecatedContract("bad", "not-a-date"),
			},
		}, "", clock.Real())
		results := v.validateContractDeprecatedCleanup01()
		require.Len(t, results, 1)
		assert.Equal(t, SeverityError, results[0].Severity)
		assert.Nil(t, results[0].Metric, "malformed-date error must carry nil Metric")
	})

	t.Run("within grace window → no finding emitted", func(t *testing.T) {
		// 1 day ago is far inside the 90-day grace window (89-day margin keeps
		// this robust against wall-clock edges).
		recent := time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02")
		v := NewValidator(&metadata.ProjectMeta{
			Contracts: map[string]*metadata.ContractMeta{
				"recent": mkDeprecatedContract("recent", recent),
			},
		}, "", clock.Real())
		results := v.validateContractDeprecatedCleanup01()
		assert.Empty(t, results, "a contract inside the grace window must emit no finding")
	})

	t.Run("mixed contracts → stale has Metric, error has nil, within-grace has none", func(t *testing.T) {
		recent := time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02")
		v := NewValidator(&metadata.ProjectMeta{
			Contracts: map[string]*metadata.ContractMeta{
				"old": mkDeprecatedContract("old", "2000-01-01"),
				"nodate": {
					ID: "nodate", Kind: "event", Lifecycle: "deprecated",
					File: "contracts/event/nodate/contract.yaml",
				},
				"recent": mkDeprecatedContract("recent", recent),
			},
		}, "", clock.Real())
		results := v.validateContractDeprecatedCleanup01()
		// Expect exactly 2 findings: one stale warning (old) + one missing-date error (nodate).
		require.Len(t, results, 2)

		var staleFound, missingFound bool
		for _, r := range results {
			switch r.Severity {
			case SeverityWarning:
				staleFound = true
				require.NotNil(t, r.Metric, "stale finding must have Metric")
				assert.Negative(t, *r.Metric)
			case SeverityError:
				missingFound = true
				assert.Nil(t, r.Metric, "error finding must have nil Metric")
			}
		}
		assert.True(t, staleFound, "expected a stale warning finding")
		assert.True(t, missingFound, "expected a missing-date error finding")
	})
}

// TestADV05FindingsCarryNilMetric verifies that ADV-05 findings carry nil Metric.
// Dead-event count is a repository aggregate (derivable as len(ADV-05 findings)),
// not a per-finding orderable distance; per ADR §M3 P-C3, ADV-05 carries no Metric.
func TestADV05FindingsCarryNilMetric(t *testing.T) {
	t.Parallel()
	v := NewValidator(&metadata.ProjectMeta{
		Contracts: map[string]*metadata.ContractMeta{
			"dead1": {
				ID:        "dead1",
				Kind:      "event",
				Lifecycle: "active",
				File:      "contracts/event/dead1/contract.yaml",
			},
			"dead2": {
				ID:        "dead2",
				Kind:      "event",
				Lifecycle: "active",
				Endpoints: metadata.EndpointsMeta{Subscribers: []string{}},
				File:      "contracts/event/dead2/contract.yaml",
			},
		},
	}, "", clock.Real())
	results := v.validateADV05()
	require.Len(t, results, 2)
	for _, r := range results {
		assert.Nil(t, r.Metric, "ADV-05 findings must carry nil Metric")
	}
}

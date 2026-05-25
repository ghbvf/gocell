package governance

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/metadata"
)

// metricValidator builds a Validator over the given contracts for metric tests.
func metricValidator(contracts map[string]*metadata.ContractMeta) *Validator {
	return NewValidator(&metadata.ProjectMeta{Contracts: contracts}, "", clock.Real())
}

// mkEventContract builds an event contract for metric tests (named to avoid
// colliding with the eventContract helper in rules_misc_consistency_test.go).
func mkEventContract(id, lifecycle string, subscribers []string) *metadata.ContractMeta {
	return &metadata.ContractMeta{
		ID: id, Kind: "event", Lifecycle: lifecycle,
		Endpoints: metadata.EndpointsMeta{Subscribers: subscribers},
		File:      "contracts/event/" + id + "/contract.yaml",
	}
}

// TestADV05DeadEventCount is the ADV-05 count-distance metric exemplar.
func TestADV05DeadEventCount(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		contracts map[string]*metadata.ContractMeta
		wantVal   float64
		wantOK    bool
	}{
		{
			name:      "no contracts → not applicable",
			contracts: map[string]*metadata.ContractMeta{},
			wantOK:    false,
		},
		{
			name: "active event with subscribers → no dead events",
			contracts: map[string]*metadata.ContractMeta{
				"e1": mkEventContract("e1", "active", []string{"cellA"}),
			},
			wantOK: false,
		},
		{
			name: "two dead events counted; wired + draft + http excluded",
			contracts: map[string]*metadata.ContractMeta{
				"dead1": mkEventContract("dead1", "active", nil),
				"dead2": mkEventContract("dead2", "active", []string{}),
				"wired": mkEventContract("wired", "active", []string{"cellA"}),
				"draft": mkEventContract("draft", "draft", nil),
				"http":  {ID: "http", Kind: "http", Lifecycle: "active"},
			},
			wantVal: 2,
			wantOK:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := metricValidator(tt.contracts).adv05DeadEventCount()
			assert.Equal(t, tt.wantOK, ok)
			if tt.wantOK {
				assert.Equal(t, tt.wantVal, got)
			}
		})
	}
}

// TestFMT23DeprecationDaysRemaining is the FMT-23 time-distance metric exemplar.
// Real clock is used; an ancient deprecatedAt is stably far past the 90-day
// grace window so "days remaining" is robustly negative regardless of run date.
func TestFMT23DeprecationDaysRemaining(t *testing.T) {
	t.Parallel()
	deprecated := func(id, at string) *metadata.ContractMeta {
		return &metadata.ContractMeta{
			ID: id, Kind: "event", Lifecycle: "deprecated", DeprecatedAt: at,
			File: "contracts/event/" + id + "/contract.yaml",
		}
	}

	t.Run("no deprecated contract with valid date → not applicable", func(t *testing.T) {
		v := metricValidator(map[string]*metadata.ContractMeta{
			"active":  mkEventContract("active", "active", []string{"cellA"}),
			"nodate":  {ID: "nodate", Kind: "event", Lifecycle: "deprecated"},
			"baddate": deprecated("baddate", "not-a-date"),
		})
		_, ok := v.fmt23DeprecationDaysRemaining()
		assert.False(t, ok)
	})

	t.Run("ancient deprecation → overdue, days remaining strongly negative", func(t *testing.T) {
		v := metricValidator(map[string]*metadata.ContractMeta{
			"old": deprecated("old", "2000-01-01"),
		})
		got, ok := v.fmt23DeprecationDaysRemaining()
		assert.True(t, ok)
		assert.Negative(t, got)
	})

	t.Run("worst (oldest) offender wins the min", func(t *testing.T) {
		v := metricValidator(map[string]*metadata.ContractMeta{
			"old":   deprecated("old", "2000-01-01"),
			"older": deprecated("older", "1990-01-01"),
		})
		got, ok := v.fmt23DeprecationDaysRemaining()
		assert.True(t, ok)
		// 1990 is more overdue than 2000 → smaller (more negative) remaining.
		assert.Negative(t, got)
		assert.Less(t, got, -10000.0)
	})
}

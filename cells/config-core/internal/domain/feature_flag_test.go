package domain

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFeatureFlag_Evaluate_Boolean(t *testing.T) {
	tests := []struct {
		name    string
		enabled bool
		want    bool
	}{
		{
			name:    "enabled boolean flag returns true",
			enabled: true,
			want:    true,
		},
		{
			name:    "disabled boolean flag returns false",
			enabled: false,
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			flag := &FeatureFlag{
				ID:      "f-1",
				Key:     "dark-mode",
				Type:    FlagBoolean,
				Enabled: tt.enabled,
			}
			// Boolean flags ignore subject entirely.
			assert.Equal(t, tt.want, flag.Evaluate("any-subject"))
			assert.Equal(t, tt.want, flag.Evaluate("another-subject"))
		})
	}
}

func TestFeatureFlag_Evaluate_Percentage(t *testing.T) {
	tests := []struct {
		name              string
		enabled           bool
		rolloutPercentage int
		wantMinHits       int // out of 1000 subjects
		wantMaxHits       int
	}{
		{
			name:              "0% rollout hits nobody",
			enabled:           true,
			rolloutPercentage: 0,
			wantMinHits:       0,
			wantMaxHits:       0,
		},
		{
			name:              "100% rollout hits everyone",
			enabled:           true,
			rolloutPercentage: 100,
			wantMinHits:       1000,
			wantMaxHits:       1000,
		},
		{
			name:              "50% rollout hits roughly half",
			enabled:           true,
			rolloutPercentage: 50,
			wantMinHits:       400, // generous bounds for deterministic hash
			wantMaxHits:       600,
		},
		{
			name:              "10% rollout hits roughly 10%",
			enabled:           true,
			rolloutPercentage: 10,
			wantMinHits:       60,
			wantMaxHits:       140,
		},
		{
			name:              "disabled percentage flag hits nobody",
			enabled:           false,
			rolloutPercentage: 50,
			wantMinHits:       0,
			wantMaxHits:       0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			flag := &FeatureFlag{
				ID:                "f-2",
				Key:               "new-ui",
				Type:              FlagPercentage,
				Enabled:           tt.enabled,
				RolloutPercentage: tt.rolloutPercentage,
			}

			hits := 0
			total := 1000
			for i := 0; i < total; i++ {
				if flag.Evaluate(fmt.Sprintf("user-%d", i)) {
					hits++
				}
			}

			assert.GreaterOrEqual(t, hits, tt.wantMinHits,
				"expected at least %d hits, got %d", tt.wantMinHits, hits)
			assert.LessOrEqual(t, hits, tt.wantMaxHits,
				"expected at most %d hits, got %d", tt.wantMaxHits, hits)
		})
	}
}

func TestFeatureFlag_Evaluate_Percentage_Deterministic(t *testing.T) {
	flag := &FeatureFlag{
		ID:                "f-3",
		Key:               "sticky-test",
		Type:              FlagPercentage,
		Enabled:           true,
		RolloutPercentage: 50,
	}

	// Same subject must always get the same result (sticky assignment).
	for _, subject := range []string{"alice", "bob", "charlie"} {
		first := flag.Evaluate(subject)
		for i := 0; i < 10; i++ {
			assert.Equal(t, first, flag.Evaluate(subject),
				"evaluate must be deterministic for subject %q", subject)
		}
	}
}

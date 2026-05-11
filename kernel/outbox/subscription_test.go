package outbox

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSubscription_Validate(t *testing.T) {
	// validBase is the minimum complete Subscription — every Validate negative
	// case mutates one field. Post-K#07 CellID is required, so it is part of
	// the base literal here.
	validBase := Subscription{
		Topic:             "session.created.v1",
		ConsumerGroup:     "cg-audit",
		CellID:            "auditcore",
		ContractID:        "event.session.created.v1",
		ContractKind:      "event",
		ContractTransport: "amqp",
	}

	tests := []struct {
		name        string
		mutate      func(*Subscription)
		wantErr     bool
		errContains string
	}{
		{
			name:        "topic empty",
			mutate:      func(s *Subscription) { s.Topic = "" },
			wantErr:     true,
			errContains: "Topic",
		},
		{
			name:        "consumerGroup empty",
			mutate:      func(s *Subscription) { s.ConsumerGroup = "" },
			wantErr:     true,
			errContains: "ConsumerGroup",
		},
		{
			name:        "cellID empty",
			mutate:      func(s *Subscription) { s.CellID = "" },
			wantErr:     true,
			errContains: "CellID",
		},
		{
			name:        "contractID empty",
			mutate:      func(s *Subscription) { s.ContractID = "" },
			wantErr:     true,
			errContains: "ContractID",
		},
		{
			name:        "contractKind empty",
			mutate:      func(s *Subscription) { s.ContractKind = "" },
			wantErr:     true,
			errContains: "ContractKind",
		},
		{
			name:        "contractTransport empty",
			mutate:      func(s *Subscription) { s.ContractTransport = "" },
			wantErr:     true,
			errContains: "ContractTransport",
		},
		{
			name:    "valid",
			mutate:  func(*Subscription) {},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sub := validBase
			tt.mutate(&sub)
			err := sub.Validate()
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestSubscription_IdempotencyNamespace(t *testing.T) {
	sub := Subscription{Topic: "t", ConsumerGroup: "cg-auditcore"}
	assert.Equal(t, "cg-auditcore", sub.IdempotencyNamespace())
}

func TestSubscription_ObservabilityID_ReturnsCellID(t *testing.T) {
	sub := Subscription{Topic: "t", ConsumerGroup: "cg-audit", CellID: "auditcore"}
	assert.Equal(t, "auditcore", sub.ObservabilityID())
}

// TestSubscription_ObservabilityID_NoFallbackOnEmpty pins K#07's HARD contract:
// ObservabilityID has no fallback to ConsumerGroup. When CellID is empty
// (which should never happen for a Validate-passed Subscription, because
// Validate rejects empty CellID), ObservabilityID returns "" — the absence is
// observable and silently substituting ConsumerGroup would mask a codegen
// defect. See ADR 202605111000-adr-subscription-cellid-mandatory.md.
func TestSubscription_ObservabilityID_NoFallbackOnEmpty(t *testing.T) {
	sub := Subscription{Topic: "t", ConsumerGroup: "cg-audit"}
	assert.Equal(t, "", sub.ObservabilityID())
}

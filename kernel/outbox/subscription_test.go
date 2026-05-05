package outbox

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSubscription_Validate(t *testing.T) {
	tests := []struct {
		name        string
		sub         Subscription
		wantErr     bool
		errContains string
	}{
		{
			name:        "topic empty",
			sub:         Subscription{Topic: "", ConsumerGroup: "cg-audit"},
			wantErr:     true,
			errContains: "Topic",
		},
		{
			name:        "consumerGroup empty",
			sub:         Subscription{Topic: "session.created.v1", ConsumerGroup: ""},
			wantErr:     true,
			errContains: "ConsumerGroup",
		},
		{
			name:        "both empty",
			sub:         Subscription{},
			wantErr:     true,
			errContains: "Topic",
		},
		{
			name: "contractID empty",
			sub: Subscription{
				Topic: "session.created.v1", ConsumerGroup: "cg-audit",
				ContractKind: "event", ContractTransport: "amqp",
			},
			wantErr:     true,
			errContains: "ContractID",
		},
		{
			name: "contractKind empty",
			sub: Subscription{
				Topic: "session.created.v1", ConsumerGroup: "cg-audit",
				ContractID: "event.session.created.v1", ContractTransport: "amqp",
			},
			wantErr:     true,
			errContains: "ContractKind",
		},
		{
			name: "contractTransport empty",
			sub: Subscription{
				Topic: "session.created.v1", ConsumerGroup: "cg-audit",
				ContractID: "event.session.created.v1", ContractKind: "event",
			},
			wantErr:     true,
			errContains: "ContractTransport",
		},
		{
			name: "valid",
			sub: Subscription{
				Topic: "session.created.v1", ConsumerGroup: "cg-audit",
				ContractID: "event.session.created.v1", ContractKind: "event", ContractTransport: "amqp",
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.sub.Validate()
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

func TestSubscription_ObservabilityID_UsesCellID(t *testing.T) {
	sub := Subscription{Topic: "t", ConsumerGroup: "cg-audit", CellID: "auditcore"}
	assert.Equal(t, "auditcore", sub.ObservabilityID())
}

func TestSubscription_ObservabilityID_FallsBackToConsumerGroup(t *testing.T) {
	sub := Subscription{Topic: "t", ConsumerGroup: "cg-audit"}
	assert.Equal(t, "cg-audit", sub.ObservabilityID())
}

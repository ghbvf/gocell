package eventreceive

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/webhook"
)

// newTestService builds a Service with a discard logger so unit tests stay quiet.
func newTestService(t *testing.T) *Service {
	t.Helper()
	return NewService(WithLogger(slog.New(slog.DiscardHandler)))
}

func newDelivery(t *testing.T, payload string) webhook.Delivery {
	t.Helper()
	did, err := webhook.NewDeliveryID("delivery-001")
	require.NoError(t, err)
	sid, err := webhook.NewSourceID("demosource")
	require.NoError(t, err)
	return webhook.Delivery{
		DeliveryID: did,
		SourceID:   sid,
		Payload:    []byte(payload),
	}
}

func TestService_HandleEvent(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		payload string
		wantErr bool
	}{
		{name: "valid full payload", payload: `{"eventId":"evt-1","type":"order.created","data":{"orderId":"o-9"}}`},
		{name: "valid minimal payload", payload: `{"eventId":"evt-2","type":"ping"}`},
		{name: "valid ignores unknown extra fields", payload: `{"eventId":"evt-3","type":"x","extra":true}`},
		{name: "malformed json rejected", payload: `{not json`, wantErr: true},
		{name: "json array (not object) rejected", payload: `[1,2,3]`, wantErr: true},
		{name: "empty body rejected", payload: ``, wantErr: true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			svc := newTestService(t)
			err := svc.HandleEvent(context.Background(), newDelivery(t, tt.payload))
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}

// TestNewService_DefaultLogger verifies the no-option constructor falls back to a
// usable logger (slog.Default) rather than a nil one that would panic on use.
func TestNewService_DefaultLogger(t *testing.T) {
	t.Parallel()
	svc := NewService()
	require.NotNil(t, svc)
	require.NotNil(t, svc.logger)
	// A nil WithLogger arg is ignored (keeps the default).
	svc2 := NewService(WithLogger(nil))
	require.NotNil(t, svc2.logger)
}

package sagalog

import (
	"log/slog"
	"testing"

	"github.com/ghbvf/gocell/pkg/idutil"
)

// TestInstanceFields_CarriesInstanceAndLeaseID is the regression lock for the
// #1266 invariant "every per-instance saga log carries lease_id". It asserts
// the carrier always emits instance_id + lease_id (set to the positional args)
// and appends caller extras in order. Together with the archtest
// SAGA-SLOG-INSTANCE-FIELDS-CALLER-01 (which forces every site through this
// carrier) it replaces the 24 brittle per-site assertions.
func TestInstanceFields_CarriesInstanceAndLeaseID(t *testing.T) {
	t.Parallel()

	const (
		instanceID idutil.SafeID = "inst-123"
		leaseID    idutil.SafeID = "lease-abc"
	)

	tests := []struct {
		name  string
		extra []slog.Attr
		// wantKeys is the full expected key sequence (mandatory pair first).
		wantKeys []string
	}{
		{
			name:     "no extras",
			extra:    nil,
			wantKeys: []string{"instance_id", "lease_id"},
		},
		{
			name: "extras appended after mandatory pair, in order",
			extra: []slog.Attr{
				slog.String("definition_id", "def-1"),
				slog.String("reason", "stale_lease"),
			},
			wantKeys: []string{"instance_id", "lease_id", "definition_id", "reason"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := InstanceFields(instanceID, leaseID, tt.extra...)

			if len(got) != len(tt.wantKeys) {
				t.Fatalf("attr count = %d, want %d (%v)", len(got), len(tt.wantKeys), tt.wantKeys)
			}
			gotKeys := make([]string, len(got))
			for i, a := range got {
				gotKeys[i] = a.Key
			}
			for i, want := range tt.wantKeys {
				if gotKeys[i] != want {
					t.Errorf("attr[%d].Key = %q, want %q (full: %v)", i, gotKeys[i], want, gotKeys)
				}
			}

			// The two mandatory attrs must carry the positional arg values.
			if got[0].Key != "instance_id" || got[0].Value.String() != string(instanceID) {
				t.Errorf("instance_id attr = %v, want %q", got[0], string(instanceID))
			}
			if got[1].Key != "lease_id" || got[1].Value.String() != string(leaseID) {
				t.Errorf("lease_id attr = %v, want %q", got[1], string(leaseID))
			}
		})
	}
}

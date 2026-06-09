package sagalog

import (
	"log/slog"
	"testing"

	"github.com/ghbvf/gocell/pkg/idutil"
)

type instanceFieldsCase struct {
	name       string
	instanceID idutil.SafeID
	leaseID    idutil.SafeID
	extra      []slog.Attr
	// wantKeys is the full expected key sequence (mandatory pair first).
	wantKeys []string
}

// TestInstanceFields_CarriesInstanceAndLeaseID is the regression lock for the
// #1266 invariant "every per-instance saga log carries lease_id". It asserts
// the carrier always emits instance_id + lease_id (set to the positional args)
// and appends caller extras in order. Together with the archtest
// SAGA-SLOG-INSTANCE-FIELDS-CALLER-01 (which forces every site through this
// carrier) it replaces the 24 brittle per-site assertions.
func TestInstanceFields_CarriesInstanceAndLeaseID(t *testing.T) {
	t.Parallel()

	tests := []instanceFieldsCase{
		{
			name:       "no extras",
			instanceID: "inst-123",
			leaseID:    "lease-abc",
			extra:      nil,
			wantKeys:   []string{"instance_id", "lease_id"},
		},
		{
			name:       "extras appended after mandatory pair, in order",
			instanceID: "inst-123",
			leaseID:    "lease-abc",
			extra: []slog.Attr{
				slog.String("definition_id", "def-1"),
				slog.String("reason", "stale_lease"),
			},
			wantKeys: []string{"instance_id", "lease_id", "definition_id", "reason"},
		},
		{
			// Zero-value IDs: carrier does no validation, still emits both keys.
			// Documents that InstanceFields is a pure structural carrier.
			name:       "zero-value IDs still emit both keys",
			instanceID: "",
			leaseID:    "",
			extra:      nil,
			wantKeys:   []string{"instance_id", "lease_id"},
		},
		{
			// Duplicate key in extras: mandatory pair comes first, extras are
			// appended verbatim (no deduplication). Documents the semantics.
			name:       "duplicate lease_id in extras appended verbatim",
			instanceID: "inst-123",
			leaseID:    "lease-abc",
			extra:      []slog.Attr{slog.String("lease_id", "other")},
			wantKeys:   []string{"instance_id", "lease_id", "lease_id"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assertInstanceFields(t, tt)
		})
	}
}

func assertInstanceFields(t *testing.T, tt instanceFieldsCase) {
	t.Helper()

	got := InstanceFields(tt.instanceID, tt.leaseID, tt.extra...)

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
	if got[0].Key != "instance_id" || got[0].Value.String() != string(tt.instanceID) {
		t.Errorf("instance_id attr = %v, want %q", got[0], string(tt.instanceID))
	}
	if got[1].Key != "lease_id" || got[1].Value.String() != string(tt.leaseID) {
		t.Errorf("lease_id attr = %v, want %q", got[1], string(tt.leaseID))
	}
}

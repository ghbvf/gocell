package ledger_test

import (
	"strings"
	"testing"

	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/errcode/errcodetest"
	"github.com/ghbvf/gocell/framework/pkg/idutil"
	"github.com/ghbvf/gocell/framework/runtime/audit/ledger"
)

// TestValidateQueryFilters exercises the ledger-layer ValidateQueryFilters
// chokepoint directly (defense-in-depth unit test, #1742 / #2199).
// Handler tests cover the wire-boundary path; this test covers the store-layer
// path that non-handler callers (e.g. direct store access, cross-tenant backends)
// also traverse.
func TestValidateQueryFilters(t *testing.T) {
	// unsafeChar is outside the SafeID charset (newline, tabs, @ etc.).
	const unsafeChar = "@"
	// longID exceeds MaxMetadataIDLen.
	longID := strings.Repeat("a", idutil.MaxMetadataIDLen+1)
	// longEventType exceeds MaxMetadataIDLen.
	longEventType := strings.Repeat("e", idutil.MaxMetadataIDLen+1)
	// validDotEventType is a dotted label within the length cap — dots are valid
	// event-type separators and must NOT be rejected by the length-only check.
	validDotEventType := "event.type.v1"

	cases := []struct {
		name     string
		filters  ledger.AuditFilters
		wantErr  bool
		wantCode errcode.Code
	}{
		{
			name:    "all empty is valid (no filter)",
			filters: ledger.AuditFilters{},
			wantErr: false,
		},
		{
			name:    "valid non-empty actorId",
			filters: ledger.AuditFilters{ActorID: "actor-123"},
			wantErr: false,
		},
		{
			name:     "actorId with unsafe char rejected",
			filters:  ledger.AuditFilters{ActorID: "actor" + unsafeChar + "injection"},
			wantErr:  true,
			wantCode: errcode.ErrValidationFailed,
		},
		{
			name:     "subjectId with unsafe char rejected",
			filters:  ledger.AuditFilters{SubjectID: "subject" + unsafeChar + "x"},
			wantErr:  true,
			wantCode: errcode.ErrValidationFailed,
		},
		{
			name:     "traceId with unsafe char rejected",
			filters:  ledger.AuditFilters{TraceID: "trace" + unsafeChar + "x"},
			wantErr:  true,
			wantCode: errcode.ErrValidationFailed,
		},
		{
			name:     "actorId exceeding MaxMetadataIDLen rejected",
			filters:  ledger.AuditFilters{ActorID: longID},
			wantErr:  true,
			wantCode: errcode.ErrValidationFailed,
		},
		{
			name:     "subjectId exceeding MaxMetadataIDLen rejected",
			filters:  ledger.AuditFilters{SubjectID: longID},
			wantErr:  true,
			wantCode: errcode.ErrValidationFailed,
		},
		{
			name:     "traceId exceeding MaxMetadataIDLen rejected",
			filters:  ledger.AuditFilters{TraceID: longID},
			wantErr:  true,
			wantCode: errcode.ErrValidationFailed,
		},
		{
			name:     "eventType exceeding length cap rejected",
			filters:  ledger.AuditFilters{EventType: longEventType},
			wantErr:  true,
			wantCode: errcode.ErrValidationFailed,
		},
		{
			name:    "eventType with dots within cap is valid (dotted label)",
			filters: ledger.AuditFilters{EventType: validDotEventType},
			wantErr: false,
		},
		{
			name:    "eventType at exact MaxMetadataIDLen is valid",
			filters: ledger.AuditFilters{EventType: strings.Repeat("e", idutil.MaxMetadataIDLen)},
			wantErr: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ledger.ValidateQueryFilters(tc.filters)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ValidateQueryFilters(%+v): expected error, got nil", tc.filters)
				}
				errcodetest.AssertCode(t, err, tc.wantCode)
			} else if err != nil {
				t.Fatalf("ValidateQueryFilters(%+v): unexpected error: %v", tc.filters, err)
			}
		})
	}
}

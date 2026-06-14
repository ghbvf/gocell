package auditquery

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ghbvf/gocell/framework/pkg/tenant"
)

// TestAuditFieldMask pins the per-scope column-mask derivation (epic #1337 PR-12),
// including the fail-closed default: admin/super-admin (tenant/all) get the full
// view (empty mask), a non-admin user (self) masks the operator-diagnostic columns,
// a device additionally masks the subject-of-record, and ANY unenumerated scope
// fails closed to the most-restrictive mask so a future RowScope value can never
// silently widen visibility.
func TestAuditFieldMask(t *testing.T) {
	const (
		corr    = "correlationId"
		trace   = "traceId"
		subject = "subjectId"
	)
	cases := []struct {
		name  string
		scope tenant.RowScope
		want  []string
	}{
		{"self masks diagnostics", tenant.RowScopeSelf, []string{corr, trace}},
		{"device masks subject + diagnostics", tenant.RowScopeDevice, []string{subject, corr, trace}},
		{"tenant admin full view", tenant.RowScopeTenant, nil},
		{"super-admin all full view", tenant.RowScopeAll, nil},
		{"unknown scope fails closed to most restrictive", tenant.RowScope(0), []string{subject, corr, trace}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := auditFieldMask(tc.scope)
			if tc.want == nil {
				assert.True(t, got.IsZero(), "want identity (empty) mask, got %v", got.Fields)
				return
			}
			assert.ElementsMatch(t, tc.want, got.Fields)
		})
	}
}

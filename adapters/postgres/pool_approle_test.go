package postgres

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/healthz"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// TestAppRoleRestrictedResult covers the pure (DB-free) classifier that turns the
// two pg_roles boolean flags into a probe verdict: a serving role is acceptable
// ONLY when it is neither a superuser nor BYPASSRLS (either bypasses RLS, making
// FORCE ROW LEVEL SECURITY a runtime no-op — #1676 [F-B11]).
func TestAppRoleRestrictedResult(t *testing.T) {
	tests := []struct {
		name    string
		super   bool
		bypass  bool
		wantErr bool
	}{
		{"restricted role ok", false, false, false},
		{"superuser rejected", true, false, true},
		{"bypassrls rejected", false, true, true},
		{"superuser and bypassrls rejected", true, true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := appRoleRestrictedResult(tt.super, tt.bypass)
			if !tt.wantErr {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			var coded *errcode.Error
			require.True(t, errors.As(err, &coded), "expected *errcode.Error, got %T: %v", err, err)
			assert.Equal(t, ErrAdapterPGRoleBypassRLS, coded.Code)
		})
	}
}

// TestPool_RestrictedRoleProbeGating verifies that the restricted-role precondition
// probe is appended to Probes() ONLY when Config.RequireRestrictedRole is set. The
// generic pool (flag off — e.g. tools/pg-migrate admin pool) keeps exactly the two
// base probes; a serving pool (corebundle sets the flag) additionally exposes
// postgres_app_role_restricted_ready. No real DB is contacted — Probes() builds
// probe descriptors lazily and must not dereference inner.
func TestPool_RestrictedRoleProbeGating(t *testing.T) {
	probeNames := func(p *Pool) []healthz.ProbeName {
		var out []healthz.ProbeName
		for _, probe := range p.Probes() {
			out = append(out, probe.Name())
		}
		return out
	}

	off := &Pool{config: Config{}}
	assert.ElementsMatch(t,
		[]healthz.ProbeName{ProbeReady, ProbeIndexesValidReady},
		probeNames(off),
		"flag off: only the two base probes, no restricted-role probe")

	on := &Pool{config: Config{RequireRestrictedRole: true}}
	assert.ElementsMatch(t,
		[]healthz.ProbeName{ProbeReady, ProbeIndexesValidReady, ProbeAppRoleRestrictedReady},
		probeNames(on),
		"flag on: base probes plus postgres_app_role_restricted_ready")
}

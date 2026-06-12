//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/corecells/auditcore/slices/auditquery"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/cell/celltest"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/pkg/tenant"
	"github.com/ghbvf/gocell/runtime/audit/ledger"
	"github.com/ghbvf/gocell/runtime/audit/ledger/storetest"
	"github.com/ghbvf/gocell/runtime/auth"
)

// auditProjTenant is a canonical tenant UUID; the audit read path fail-closes on
// an empty principal tenant (epic #1337 PR-2a), so every caller carries it.
const auditProjTenant = "3f2504e0-4f89-41d3-9a0c-0305e82c3301"

// TestAuditQueryColumnMaskE2E is the cross-module e2e for epic #1337 PR-12 (T12.4):
// it drives the REAL published auditquery HTTP handler (corecells) — wired to a
// ledger store, the pkg/projection masking funnel, and runtime/auth row-visibility
// derivation, exactly as production composes it — and asserts that admin / non-admin
// user / device callers receive DIFFERENT visible columns from the SAME audit row.
//
// This is the integration-lane companion of the slice-level
// auditquery.TestHandleQuery_ColumnMaskMatrix: it crosses module boundaries
// (corecells handler + pkg/projection + runtime/auth + runtime/audit/ledger) through
// the contract's published Service/Handler API rather than slice-internal test
// helpers, proving the masking survives real route registration + auth mounting.
// Docker-free (mem ledger), matching the other auditcore integration tests
// (governance VERIFY-06).
func TestAuditQueryColumnMaskE2E(t *testing.T) {
	const (
		masked     = "<REDACTED>"
		selfActor  = "usr-self"
		deviceID   = "dev-7"
		subjectVal = "subject-of-record"
	)

	proto := storetest.NewTestProtocol(t)
	store, err := ledger.NewMemStore(proto, clock.Real())
	require.NoError(t, err)

	codec, err := query.NewCursorCodec([]byte("0123456789abcdef0123456789abcdef"))
	require.NoError(t, err)
	svc, err := auditquery.NewService(store, codec, slog.New(slog.DiscardHandler),
		outbox.DemoCellTxManager(), query.RunModeProd)
	require.NoError(t, err)

	h := auditquery.NewHandler(svc)
	mux := celltest.NewTestMux()
	mux.Route("/api/v1/audit", func(sub cell.RouteMux) {
		require.NoError(t, h.RegisterRoutes(sub))
	})

	// Seed one row per owner axis (owner column is actor_id: vis.Allows(ActorID)).
	// Identical sensitive column values isolate masking from data.
	base := time.Date(2026, 4, 1, 9, 0, 0, 0, time.UTC)
	seedCtx := tenant.WithScope(context.Background(), tenant.TenantID(auditProjTenant))
	seedRow := func(id, actor string) {
		require.NoError(t, store.Append(seedCtx, &ledger.Entry{
			ID: id, EventID: "evt-" + id, EventType: "event.test.v1",
			ActorID: actor, SubjectID: subjectVal, TenantID: auditProjTenant,
			CorrelationID: "corr-" + id, TraceID: "trace-" + id,
			Timestamp: base, OccurredAt: base, Payload: []byte("{}"),
		}))
	}
	seedRow("admin", "admin-actor")
	seedRow("self", selfActor)
	seedRow("dev", deviceID)

	principal := func(kind auth.PrincipalKind, subject string, roles ...string) *auth.Principal {
		return &auth.Principal{Kind: kind, Subject: subject, Roles: roles, TenantID: auditProjTenant, AuthMethod: "test"}
	}

	cases := []struct {
		name                             string
		principal                        *auth.Principal
		eventID                          string
		wantSubject, wantCorr, wantTrace string
	}{
		// admin (RowScopeTenant): full view — every column visible.
		{"admin_full_view", principal(auth.PrincipalUser, "admin-x", auth.RoleAdmin), "evt-admin", subjectVal, "corr-admin", "trace-admin"},
		// non-admin user (RowScopeSelf): operator-diagnostic columns masked.
		{"non_admin_self_masks_diagnostics", principal(auth.PrincipalUser, selfActor), "evt-self", subjectVal, masked, masked},
		// device (RowScopeDevice): additionally masks the human subject-of-record.
		{"device_masks_subject_and_diagnostics", principal(auth.PrincipalDevice, deviceID), "evt-dev", masked, masked, masked},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/entries", nil).
				WithContext(auth.WithPrincipal(context.Background(), tc.principal))
			mux.ServeHTTP(w, req)
			require.Equalf(t, http.StatusOK, w.Code, "body=%s", w.Body.String())

			var resp struct {
				Data []map[string]any `json:"data"`
			}
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))

			var row map[string]any
			for _, r := range resp.Data {
				if r["eventId"] == tc.eventID {
					row = r
					break
				}
			}
			require.NotNilf(t, row, "row %s not visible to %s; body=%s", tc.eventID, tc.name, w.Body.String())

			assert.Equal(t, tc.wantSubject, row["subjectId"], "subjectId")
			assert.Equal(t, tc.wantCorr, row["correlationId"], "correlationId")
			assert.Equal(t, tc.wantTrace, row["traceId"], "traceId")
			// tenantId is projected (PR-12) but in no per-scope mask: visible to all.
			assert.Equal(t, auditProjTenant, row["tenantId"], "tenantId visible (own tenant)")
		})
	}
}

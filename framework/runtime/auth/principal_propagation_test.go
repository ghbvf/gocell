package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/framework/kernel/outbox"
	"github.com/ghbvf/gocell/framework/pkg/ctxkeys"
	"github.com/ghbvf/gocell/framework/pkg/idutil"
)

// TestEncodePrincipalHeader verifies round-trip encoding of PrincipalMetadata.
func TestEncodePrincipalHeader(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		pm      outbox.PrincipalMetadata
		wantNil bool // expect empty string
	}{
		{
			name:    "zero value returns empty",
			pm:      outbox.PrincipalMetadata{},
			wantNil: true,
		},
		{
			name: "actor only",
			pm:   outbox.PrincipalMetadata{ActorID: "usr-abc"},
		},
		{
			name: "all fields",
			pm: outbox.PrincipalMetadata{
				ActorID:   "usr-actor",
				SubjectID: "usr-subject",
				SessionID: "sess-123",
				// TenantID deliberately empty (stripped before encode per spec)
			},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := encodePrincipalHeader(tc.pm)
			if tc.wantNil {
				assert.Empty(t, got, "zero PrincipalMetadata should encode to empty string")
				return
			}
			require.NotEmpty(t, got, "non-zero PrincipalMetadata should encode to non-empty string")

			// must be valid base64url (no padding)
			raw, err := base64.RawURLEncoding.DecodeString(got)
			require.NoError(t, err, "encoded value must be valid base64url")

			var decoded outbox.PrincipalMetadata
			require.NoError(t, json.Unmarshal(raw, &decoded))
			assert.Equal(t, tc.pm.ActorID, decoded.ActorID)
			assert.Equal(t, tc.pm.SubjectID, decoded.SubjectID)
			assert.Equal(t, tc.pm.SessionID, decoded.SessionID)
		})
	}
}

// TestRebuildPropagatedPrincipal_RoundTrip verifies actor/subject/session are
// rebuilt into ctx; tenant is NOT written (stays with X-Tenant-ID path).
func TestRebuildPropagatedPrincipal_RoundTrip(t *testing.T) {
	t.Parallel()

	pm := outbox.PrincipalMetadata{
		ActorID:   idutil.SafeID("usr-abc"),
		SubjectID: idutil.SafeID("usr-abc"),
		SessionID: idutil.SafeID("sess-xyz"),
		TenantID:  idutil.SafeID(""), // tenant should NOT be restored
	}
	header := encodePrincipalHeader(pm)
	require.NotEmpty(t, header)

	ctx := rebuildPropagatedPrincipal(context.Background(), header)

	actor, ok := ctxkeys.ActorIDFrom(ctx)
	require.True(t, ok)
	assert.Equal(t, string(pm.ActorID), actor)

	subject, ok := ctxkeys.SubjectIDFrom(ctx)
	require.True(t, ok)
	assert.Equal(t, string(pm.SubjectID), subject)

	session, ok := ctxkeys.SessionIDFrom(ctx)
	require.True(t, ok)
	assert.Equal(t, string(pm.SessionID), session)

	// Tenant must not be written by rebuildPropagatedPrincipal (tenant comes from X-Tenant-ID).
	_, tenantOk := ctxkeys.TenantIDFrom(ctx)
	assert.False(t, tenantOk, "rebuildPropagatedPrincipal must not write tenant ctxkey")
}

// TestRebuildPropagatedPrincipal_BusinessPrincipalWinsOverCallerCell verifies
// that when a JWT-derived actor is already set (from injectPrincipalCtxKeys),
// the rebuild clears it first and then restores the propagated actor — the
// propagated business principal wins over the service CallerCellID actor.
func TestRebuildPropagatedPrincipal_BusinessPrincipalWinsOverCallerCell(t *testing.T) {
	t.Parallel()

	// Simulate handleServiceToken: injectPrincipalCtxKeys stamps actor=CallerCellID.
	ctx := ctxkeys.WithActorID(context.Background(), "configcore") // service actor

	pm := outbox.PrincipalMetadata{
		ActorID:   "usr-real",
		SubjectID: "usr-real",
	}
	header := encodePrincipalHeader(pm)

	ctx = rebuildPropagatedPrincipal(ctx, header)

	actor, _ := ctxkeys.ActorIDFrom(ctx)
	assert.Equal(t, "usr-real", actor, "propagated actor must win over CallerCellID")
}

// TestRebuildPropagatedPrincipal_InvalidBase64_CtxUnchanged verifies that
// a malformed base64 principal header leaves the context unchanged (safe degradation).
func TestRebuildPropagatedPrincipal_InvalidBase64_CtxUnchanged(t *testing.T) {
	t.Parallel()

	ctx := ctxkeys.WithActorID(context.Background(), "original-actor")
	ctx2 := rebuildPropagatedPrincipal(ctx, "!!!not-base64!!!")

	actor, _ := ctxkeys.ActorIDFrom(ctx2)
	assert.Equal(t, "original-actor", actor, "invalid base64 must leave ctx unchanged")
}

// TestRebuildPropagatedPrincipal_InvalidJSON_CtxUnchanged verifies that
// base64-valid but JSON-invalid payload leaves the context unchanged.
func TestRebuildPropagatedPrincipal_InvalidJSON_CtxUnchanged(t *testing.T) {
	t.Parallel()

	ctx := ctxkeys.WithActorID(context.Background(), "original-actor")
	badHeader := base64.RawURLEncoding.EncodeToString([]byte("{not-json"))
	ctx2 := rebuildPropagatedPrincipal(ctx, badHeader)

	actor, _ := ctxkeys.ActorIDFrom(ctx2)
	assert.Equal(t, "original-actor", actor, "invalid JSON must leave ctx unchanged")
}

// TestRebuildPropagatedPrincipal_EmptyHeader_CtxUnchanged verifies that an
// empty header string leaves the context unchanged.
func TestRebuildPropagatedPrincipal_EmptyHeader_CtxUnchanged(t *testing.T) {
	t.Parallel()

	ctx := ctxkeys.WithActorID(context.Background(), "original-actor")
	ctx2 := rebuildPropagatedPrincipal(ctx, "")

	actor, _ := ctxkeys.ActorIDFrom(ctx2)
	assert.Equal(t, "original-actor", actor, "empty header must leave ctx unchanged")
}

// TestSignInternalRequest_WithPrincipal verifies that SignInternalRequest sets
// the X-Gocell-Principal header, X-Tenant-ID header, and Authorization header.
func TestSignInternalRequest_WithPrincipal(t *testing.T) {
	t.Parallel()

	ring := mustTestRing(t, testHMACKey, "")
	clk := clockmock.New(time.Now())

	pm := outbox.PrincipalMetadata{
		ActorID:   "usr-abc",
		SubjectID: "usr-abc",
		SessionID: "sess-xyz",
	}
	// Inject principal into ctx (as injectPrincipalCtxKeys would do).
	ctx := ctxkeys.WithActorID(context.Background(), string(pm.ActorID))
	ctx = ctxkeys.WithSubjectID(ctx, string(pm.SubjectID))
	ctx = ctxkeys.WithSessionID(ctx, string(pm.SessionID))

	req := httptest.NewRequest(http.MethodGet, "/internal/v1/config/mykey", nil)
	req = req.WithContext(ctx)

	err := SignInternalRequest(ctx, ring, "accesscore", req, "", clk)
	require.NoError(t, err)

	assert.NotEmpty(t, req.Header.Get("Authorization"), "Authorization header must be set")
	assert.True(t, strings.HasPrefix(req.Header.Get("Authorization"), "ServiceToken "),
		"Authorization header must start with ServiceToken")
	assert.NotEmpty(t, req.Header.Get(HeaderPrincipal), "X-Gocell-Principal must be set when ctx has actor")
}

// TestSignInternalRequest_EmptyPrincipal_NoPrincipalHeader verifies that when
// the ctx has no principal identity, X-Gocell-Principal is NOT set.
func TestSignInternalRequest_EmptyPrincipal_NoPrincipalHeader(t *testing.T) {
	t.Parallel()

	ring := mustTestRing(t, testHMACKey, "")
	clk := clockmock.New(time.Now())

	req := httptest.NewRequest(http.MethodGet, "/internal/v1/config/mykey", nil)
	err := SignInternalRequest(context.Background(), ring, "accesscore", req, "", clk)
	require.NoError(t, err)

	assert.NotEmpty(t, req.Header.Get("Authorization"))
	assert.Empty(t, req.Header.Get(HeaderPrincipal),
		"X-Gocell-Principal must not be set when ctx has no principal")
}

// TestSignInternalRequest_NilRing_Error verifies fail-fast on nil ring.
func TestSignInternalRequest_NilRing_Error(t *testing.T) {
	t.Parallel()

	clk := clockmock.New(time.Now())
	req := httptest.NewRequest(http.MethodGet, "/internal/v1/config/mykey", nil)
	err := SignInternalRequest(context.Background(), nil, "accesscore", req, "", clk)
	require.Error(t, err, "nil ring must return error")
}

// TestSignInternalRequest_EmptyCallerCell_Error verifies fail-fast on empty callerCell.
func TestSignInternalRequest_EmptyCallerCell_Error(t *testing.T) {
	t.Parallel()

	ring := mustTestRing(t, testHMACKey, "")
	clk := clockmock.New(time.Now())
	req := httptest.NewRequest(http.MethodGet, "/internal/v1/config/mykey", nil)
	err := SignInternalRequest(context.Background(), ring, "", req, "", clk)
	require.Error(t, err, "empty callerCell must return error")
}

// TestSignInternalRequest_NilReq_Error verifies fail-fast on nil req.
func TestSignInternalRequest_NilReq_Error(t *testing.T) {
	t.Parallel()

	ring := mustTestRing(t, testHMACKey, "")
	clk := clockmock.New(time.Now())
	err := SignInternalRequest(context.Background(), ring, "accesscore", nil, "", clk)
	require.Error(t, err, "nil req must return error")
}

// TestHandleServiceToken_PrincipalHeaderRebuild is an end-to-end test verifying
// that when a request carries X-Gocell-Principal, the service token middleware
// rebuilds the business principal (actor/subject/session) into ctx, overriding
// the CallerCellID-derived actor.
func TestHandleServiceToken_PrincipalHeaderRebuild(t *testing.T) {
	t.Parallel()

	ring := mustTestRing(t, testHMACKey, "")
	clk := clockmock.New(time.Now())

	pm := outbox.PrincipalMetadata{
		ActorID:   idutil.SafeID("usr-real"),
		SubjectID: idutil.SafeID("usr-real"),
		SessionID: idutil.SafeID("sess-abc"),
	}

	// Encode the principal header as SignInternalRequest would.
	ph := encodePrincipalHeader(pm)
	require.NotEmpty(t, ph)

	token := GenerateServiceToken(ring, "accesscore", http.MethodGet, "/internal/v1/resource", "", "", ph, clk.Now())
	require.NotEmpty(t, token)

	var capturedActor, capturedSubject, capturedSession string
	handler := ServiceTokenMiddleware(
		ring, clk,
		WithServiceTokenNonceStore(mustNewInMemoryNonceStore(t)),
	)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedActor, _ = ctxkeys.ActorIDFrom(r.Context())
		capturedSubject, _ = ctxkeys.SubjectIDFrom(r.Context())
		capturedSession, _ = ctxkeys.SessionIDFrom(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/internal/v1/resource", nil)
	req.Header.Set("Authorization", "ServiceToken "+token)
	req.Header.Set(HeaderPrincipal, ph)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "usr-real", capturedActor, "business actor must be propagated")
	assert.Equal(t, "usr-real", capturedSubject, "business subject must be propagated")
	assert.Equal(t, "sess-abc", capturedSession, "business session must be propagated")
}

// TestHandleServiceToken_TamperedPrincipalHeader_Rejected verifies that tampering
// with the X-Gocell-Principal header fails MAC verification.
func TestHandleServiceToken_TamperedPrincipalHeader_Rejected(t *testing.T) {
	t.Parallel()

	ring := mustTestRing(t, testHMACKey, "")
	clk := clockmock.New(time.Now())

	pm := outbox.PrincipalMetadata{ActorID: "usr-real"}
	ph := encodePrincipalHeader(pm)

	// Sign with the original principal header.
	token := GenerateServiceToken(ring, "accesscore", http.MethodGet, "/internal/v1/resource", "", "", ph, clk.Now())
	require.NotEmpty(t, token)

	// Tamper: replace the principal header with a different value.
	tamperedPM := outbox.PrincipalMetadata{ActorID: "evil-actor"}
	tamperedPH := encodePrincipalHeader(tamperedPM)

	handler := ServiceTokenMiddleware(
		ring, clk,
		WithServiceTokenNonceStore(mustNewInMemoryNonceStore(t)),
	)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler must not be called: tampered principal header must fail MAC")
	}))

	req := httptest.NewRequest(http.MethodGet, "/internal/v1/resource", nil)
	req.Header.Set("Authorization", "ServiceToken "+token)
	req.Header.Set(HeaderPrincipal, tamperedPH) // tampered
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code, "tampered principal header must be rejected")
}

// TestHandleServiceToken_StrippedPrincipalHeader_Rejected verifies that stripping
// the X-Gocell-Principal header (when token was signed with one) fails MAC verification.
func TestHandleServiceToken_StrippedPrincipalHeader_Rejected(t *testing.T) {
	t.Parallel()

	ring := mustTestRing(t, testHMACKey, "")
	clk := clockmock.New(time.Now())

	pm := outbox.PrincipalMetadata{ActorID: "usr-real"}
	ph := encodePrincipalHeader(pm)

	// Sign with principal header.
	token := GenerateServiceToken(ring, "accesscore", http.MethodGet, "/internal/v1/resource", "", "", ph, clk.Now())
	require.NotEmpty(t, token)

	handler := ServiceTokenMiddleware(
		ring, clk,
		WithServiceTokenNonceStore(mustNewInMemoryNonceStore(t)),
	)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler must not be called: stripped principal header must fail MAC")
	}))

	req := httptest.NewRequest(http.MethodGet, "/internal/v1/resource", nil)
	req.Header.Set("Authorization", "ServiceToken "+token)
	// X-Gocell-Principal intentionally omitted (stripped)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code, "stripped principal header must be rejected")
}

// TestHandleServiceToken_NoPrincipalHeader_ActorIsCallerCell verifies that when
// no principal header is present, the actor stays as CallerCellID (service identity).
func TestHandleServiceToken_NoPrincipalHeader_ActorIsCallerCell(t *testing.T) {
	t.Parallel()

	ring := mustTestRing(t, testHMACKey, "")
	clk := clockmock.New(time.Now())

	// Sign with no principal header ("").
	token := GenerateServiceToken(ring, "accesscore", http.MethodGet, "/internal/v1/resource", "", "", "", clk.Now())
	require.NotEmpty(t, token)

	var capturedActor string
	handler := ServiceTokenMiddleware(
		ring, clk,
		WithServiceTokenNonceStore(mustNewInMemoryNonceStore(t)),
	)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedActor, _ = ctxkeys.ActorIDFrom(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/internal/v1/resource", nil)
	req.Header.Set("Authorization", "ServiceToken "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "accesscore", capturedActor,
		"when no principal header, actor must be CallerCellID (service identity)")
}

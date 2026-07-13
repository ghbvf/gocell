package status

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ghbvf/gocell/framework/kernel/cell/celltest"
	"github.com/ghbvf/gocell/framework/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/framework/pkg/authz"
	"github.com/ghbvf/gocell/framework/runtime/auth"
	"github.com/ghbvf/gocell/framework/runtime/certlifecycle"
	statusv1 "github.com/ghbvf/gocell/generated/contracts/http/deviceidentity/status/v1"
)

var fixedNow = time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)

// inMemRepo is a simple in-memory repository for service tests (avoids importing
// the mem package from this same-package test, per go-standards §"mock placement").
type inMemRepo struct {
	records map[string]CertRecord
}

func newInMemRepo() *inMemRepo {
	return &inMemRepo{records: make(map[string]CertRecord)}
}

func (r *inMemRepo) ActiveByDeviceID(_ context.Context, deviceID string) (CertRecord, bool, error) {
	rec, ok := r.records[deviceID]
	return rec, ok, nil
}

func (r *inMemRepo) put(rec CertRecord) { r.records[rec.DeviceID] = rec }

// mockAuthorizer is a test-only auth.Authorizer returning a fixed Decision.
type mockAuthorizer struct {
	decision authz.Decision
}

func (m *mockAuthorizer) Authorize(context.Context, string, string, string) (authz.Decision, error) {
	return m.decision, nil
}

func allowAuthorizer(t *testing.T) *mockAuthorizer {
	t.Helper()
	dec, err := authz.Allow(authz.Obligations{})
	if err != nil {
		t.Fatalf("authz.Allow: %v", err)
	}
	return &mockAuthorizer{decision: dec}
}

func denyAuthorizer() *mockAuthorizer {
	return &mockAuthorizer{decision: authz.Deny("test: deny")}
}

// newMux mounts the status route via the generated Handler on a fresh test mux.
// authorizer is injected into the request context via auth.WithAuthorizer — exactly
// as bootstrap.WithPrimaryAuthorizer does for real requests.
func newMux(t *testing.T, repo Repository, authorizer auth.Authorizer) http.Handler {
	t.Helper()
	svc := NewService(repo, clockmock.New(fixedNow))
	route := svc.FrameworkRoute()
	if route.ContractID != ContractID {
		t.Fatalf("FrameworkRoute ContractID=%q, want %q", route.ContractID, ContractID)
	}
	mux := celltest.NewTestMux()
	if err := route.Group.Register(mux); err != nil {
		t.Fatalf("RegisterRoutes: %v", err)
	}
	// Wrap mux to inject authorizer and principal into every request context.
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := auth.WithAuthorizer(r.Context(), authorizer)
		mux.ServeHTTP(w, r.WithContext(ctx))
	})
}

// adminCtx returns a context with an admin principal injected (for tests that need
// an authenticated caller to reach the service layer).
func adminCtx(authorizer auth.Authorizer) context.Context {
	ctx := auth.WithPrincipal(context.Background(), &auth.Principal{
		Kind: auth.PrincipalUser, Subject: "admin-1",
		Roles: []string{"mdm-admin"}, AuthMethod: "test",
	})
	return auth.WithAuthorizer(ctx, authorizer)
}

const statusPath = "/api/v1/deviceidentity/status"

// statusURL builds the path-param status URL for a deviceId. The contract is
// GET /api/v1/deviceidentity/status/{deviceId} (path param, #2426 F1), so the
// deviceId is a trailing segment, not a query parameter.
func statusURL(deviceID string) string { return statusPath + "/" + deviceID }

// TestStatus_NoDeviceID_NotFound: the bare /api/v1/deviceidentity/status path (no
// deviceId segment) does not match the path-param route ⇒ 404. With a path-param
// deviceId the "missing identifier" case is a routing miss, not a 400 validation
// failure (#2426 F1; mirrors cellmodules/deviceserving TestDevicestate_NoDeviceID_NotFound).
func TestStatus_NoDeviceID_NotFound(t *testing.T) {
	repo := newInMemRepo()
	handler := newMux(t, repo, allowAuthorizer(t))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, statusPath, nil)
	req = req.WithContext(adminCtx(allowAuthorizer(t)))
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("no deviceId segment: got %d, want 404 (route miss); body=%s", rec.Code, rec.Body.String())
	}
}

// TestStatus_DeviceIDTooLong: a 257-char deviceId exceeds maxLength 256, so the
// generated handler rejects it with 400 before reaching the service (#2426 F7).
func TestStatus_DeviceIDTooLong(t *testing.T) {
	repo := newInMemRepo()
	handler := newMux(t, repo, allowAuthorizer(t))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, statusURL(strings.Repeat("x", 257)), nil)
	req = req.WithContext(adminCtx(allowAuthorizer(t)))
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("257-char deviceId: got %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); !strings.Contains(body, `"error"`) {
		t.Errorf("400 body must use the canonical error envelope, got %s", body)
	}
}

// TestStatus_DeviceIDMaxLength: a 256-char deviceId is exactly at the upper bound, so
// it passes path-param validation and reaches the service; with an empty repo that is
// a 404 (not a 400) — proving the boundary is inclusive (#2426 F7).
func TestStatus_DeviceIDMaxLength(t *testing.T) {
	repo := newInMemRepo()
	handler := newMux(t, repo, allowAuthorizer(t))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, statusURL(strings.Repeat("x", 256)), nil)
	req = req.WithContext(adminCtx(allowAuthorizer(t)))
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("256-char deviceId (upper bound): got %d, want 404 (passes validation, not found); body=%s", rec.Code, rec.Body.String())
	}
}

// TestStatus_UnknownDevice: returns 404 with error body when deviceId is not found.
func TestStatus_UnknownDevice(t *testing.T) {
	repo := newInMemRepo()
	handler := newMux(t, repo, allowAuthorizer(t))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, statusURL("not-found"), nil)
	req = req.WithContext(adminCtx(allowAuthorizer(t)))
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown deviceId: got %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
	// Error body must follow {"error":{...}} shape.
	if !strings.Contains(rec.Body.String(), `"error"`) {
		t.Errorf("404 body missing \"error\" key: %s", rec.Body.String())
	}
}

// TestStatus_OK_FullSchema: 200 with all schema fields populated and correct values.
func TestStatus_OK_FullSchema(t *testing.T) {
	repo := newInMemRepo()
	renewal := fixedNow.Add(300 * 24 * time.Hour)
	repo.put(CertRecord{
		DeviceID:    "dev-1",
		Issuer:      "CN=MDM-CA",
		Serial:      "ABCD1234",
		State:       certlifecycle.StateActive(),
		NotBefore:   fixedNow,
		NotAfter:    fixedNow.Add(365 * 24 * time.Hour),
		Epoch:       3,
		RenewalTime: &renewal,
	})

	handler := newMux(t, repo, allowAuthorizer(t))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, statusURL("dev-1"), nil)
	req = req.WithContext(adminCtx(allowAuthorizer(t)))
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d; body=%s", rec.Code, rec.Body.String())
	}

	// Parse the typed response envelope.
	var env struct {
		Data struct {
			DeviceID string `json:"deviceId"`
			CertRef  *struct {
				Issuer string `json:"issuer"`
				Serial string `json:"serial"`
			} `json:"certRef"`
			Status      string  `json:"status"`
			NotBefore   string  `json:"notBefore"`
			NotAfter    string  `json:"notAfter"`
			Epoch       int64   `json:"epoch"`
			RenewalTime *string `json:"renewalTime"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal 200 body: %v; body=%s", err, rec.Body.String())
	}
	d := env.Data
	if d.DeviceID != "dev-1" {
		t.Errorf("deviceId: got %q, want %q", d.DeviceID, "dev-1")
	}
	if d.CertRef == nil {
		t.Fatal("certRef is nil, want populated")
	}
	if d.CertRef.Issuer != "CN=MDM-CA" {
		t.Errorf("certRef.issuer: got %q, want %q", d.CertRef.Issuer, "CN=MDM-CA")
	}
	if d.CertRef.Serial != "ABCD1234" {
		t.Errorf("certRef.serial: got %q, want %q", d.CertRef.Serial, "ABCD1234")
	}
	if d.Status != "active" {
		t.Errorf("status: got %q, want %q", d.Status, "active")
	}
	wantNotBefore := fixedNow.UTC().Format(time.RFC3339)
	if d.NotBefore != wantNotBefore {
		t.Errorf("notBefore: got %q, want %q", d.NotBefore, wantNotBefore)
	}
	wantNotAfter := fixedNow.Add(365 * 24 * time.Hour).UTC().Format(time.RFC3339)
	if d.NotAfter != wantNotAfter {
		t.Errorf("notAfter: got %q, want %q", d.NotAfter, wantNotAfter)
	}
	if d.Epoch != 3 {
		t.Errorf("epoch: got %d, want 3", d.Epoch)
	}
	if d.RenewalTime == nil {
		t.Fatal("renewalTime is nil, want populated")
	}
	wantRenewal := renewal.UTC().Format(time.RFC3339)
	if *d.RenewalTime != wantRenewal {
		t.Errorf("renewalTime: got %q, want %q", *d.RenewalTime, wantRenewal)
	}
}

// TestStatus_OK_RenewalTimeNull: when CertRecord.RenewalTime is nil the response
// must serialize renewalTime as JSON null (full column set, no omitempty).
func TestStatus_OK_RenewalTimeNull(t *testing.T) {
	repo := newInMemRepo()
	repo.put(CertRecord{
		DeviceID:  "dev-null-renewal",
		Issuer:    "CN=MDM-CA",
		Serial:    "0001",
		State:     certlifecycle.StateIssued(),
		NotBefore: fixedNow,
		NotAfter:  fixedNow.Add(365 * 24 * time.Hour),
		Epoch:     1,
		// RenewalTime intentionally nil.
	})

	handler := newMux(t, repo, allowAuthorizer(t))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, statusURL("dev-null-renewal"), nil)
	req = req.WithContext(adminCtx(allowAuthorizer(t)))
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d; body=%s", rec.Code, rec.Body.String())
	}
	// renewalTime must be present in the response as null (not absent).
	body := rec.Body.String()
	if !strings.Contains(body, `"renewalTime":null`) {
		t.Errorf("renewalTime must be JSON null (full column set), got body=%s", body)
	}
}

// TestStatus_StateEnumMapping verifies that each KNOWN certlifecycle.State maps to
// the correct ResponseDataStatus enum value in the 200 response body. The unknown /
// zero-value State is not a valid wire enum; that case fails closed and is covered by
// TestStatus_UnknownState_FailClosed (#2426 F5).
func TestStatus_StateEnumMapping(t *testing.T) {
	cases := []struct {
		name     string
		state    certlifecycle.State
		wantEnum string
	}{
		{"requested", certlifecycle.StateRequested(), "requested"},
		{"issued", certlifecycle.StateIssued(), "issued"},
		{"active", certlifecycle.StateActive(), "active"},
		{"near-expiry", certlifecycle.StateNearExpiry(), "near-expiry"},
		{"renewing", certlifecycle.StateRenewing(), "renewing"},
		{"rotated", certlifecycle.StateRotated(), "rotated"},
		{"revoked", certlifecycle.StateRevoked(), "revoked"},
		{"expired", certlifecycle.StateExpired(), "expired"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			repo := newInMemRepo()
			repo.put(CertRecord{
				DeviceID:  "dev-state",
				Issuer:    "CN=CA",
				Serial:    "01",
				State:     tc.state,
				NotBefore: fixedNow,
				NotAfter:  fixedNow.Add(365 * 24 * time.Hour),
				Epoch:     1,
			})
			handler := newMux(t, repo, allowAuthorizer(t))

			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, statusURL("dev-state"), nil)
			req = req.WithContext(adminCtx(allowAuthorizer(t)))
			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("want 200, got %d; body=%s", rec.Code, rec.Body.String())
			}
			if body := rec.Body.String(); !strings.Contains(body, `"`+tc.wantEnum+`"`) {
				t.Errorf("status field: want %q in body, got %s", tc.wantEnum, body)
			}
		})
	}
}

// TestStatus_UnknownState_FailClosed: a CertRecord whose State is the zero value (or
// any state outside the contract enum) must NOT serialize as a schema-invalid 200
// with status:"" — the service fails closed with a framework 5xx instead (#2426 F5).
// response.schema.json's status enum has no empty member, so emitting "" would be an
// out-of-contract wire value.
func TestStatus_UnknownState_FailClosed(t *testing.T) {
	repo := newInMemRepo()
	repo.put(CertRecord{
		DeviceID:  "dev-unknown-state",
		Issuer:    "CN=CA",
		Serial:    "01",
		State:     certlifecycle.State{}, // zero value — not a valid contract enum
		NotBefore: fixedNow,
		NotAfter:  fixedNow.Add(365 * 24 * time.Hour),
		Epoch:     1,
	})
	handler := newMux(t, repo, allowAuthorizer(t))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, statusURL("dev-unknown-state"), nil)
	req = req.WithContext(adminCtx(allowAuthorizer(t)))
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("unknown state: want 500 (fail-closed framework 5xx), got %d; body=%s", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); !strings.Contains(body, `"error"`) {
		t.Errorf("fail-closed body must use the canonical error envelope, got %s", body)
	}
	// Must NOT leak a schema-invalid empty status enum to the wire.
	if strings.Contains(rec.Body.String(), `"status":""`) {
		t.Errorf("must not emit schema-invalid status:\"\" on unknown state; got %s", rec.Body.String())
	}
}

// TestStatus_Unauthenticated: no principal → 401.
func TestStatus_Unauthenticated(t *testing.T) {
	repo := newInMemRepo()
	handler := newMux(t, repo, allowAuthorizer(t))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, statusURL("dev-1"), nil)
	// No principal injected into ctx — only authorizer.
	ctx := auth.WithAuthorizer(context.Background(), allowAuthorizer(t))
	handler.ServeHTTP(rec, req.WithContext(ctx))

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated: got %d, want 401; body=%s", rec.Code, rec.Body.String())
	}
}

// TestStatus_Forbidden: authenticated but PDP denies → 403.
func TestStatus_Forbidden(t *testing.T) {
	repo := newInMemRepo()
	handler := newMux(t, repo, denyAuthorizer())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, statusURL("dev-1"), nil)
	ctx := auth.WithPrincipal(context.Background(), &auth.Principal{
		Kind: auth.PrincipalUser, Subject: "no-role", Roles: nil, AuthMethod: "test",
	})
	ctx = auth.WithAuthorizer(ctx, denyAuthorizer())
	handler.ServeHTTP(rec, req.WithContext(ctx))

	if rec.Code != http.StatusForbidden {
		t.Errorf("forbidden: got %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
}

// TestNewService_NilClockPanic verifies that NewService panics when clk is nil.
func TestNewService_NilClockPanic(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("NewService(repo, nil) did not panic, want MustHaveClock panic")
		}
	}()
	NewService(newInMemRepo(), nil)
}

// TestNewService_NilRepoPanic verifies that NewService panics when repo is nil.
func TestNewService_NilRepoPanic(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("NewService(nil, clk) did not panic, want nil-repo panic")
		}
	}()
	NewService(nil, clockmock.New(fixedNow))
}

// TestService_Interface confirms the compile-time interface assertion in service.go.
func TestService_Interface(t *testing.T) {
	repo := newInMemRepo()
	svc := NewService(repo, clockmock.New(fixedNow))
	var _ statusv1.Service = svc
}

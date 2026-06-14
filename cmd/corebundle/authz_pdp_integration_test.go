//go:build integration

package main

// TestABACPDPGatesAuditQuery is the acceptance test for #1348 T10.4:
// the wired ABAC PDP must gate real HTTP traffic on GET /api/v1/audit/entries.
//
// Cases:
//   - admin principal + actorId != subject → 200 (baseline allow via PDP)
//   - non-admin + actorId != subject       → 403 (PDP default-deny)
//   - non-admin + actorId == subject (self) → 200 (self branch, no PDP role needed)
//
// Wiring: the composition root's bootstrap.PrimaryAuthorizerOption is exercised via the
// same call-site as production run.go. Access tokens carry live session IDs so
// they pass the session-validate verifier wired by MustAuthJWTFromAssembly.
//
// Setup sequence:
//  1. Bootstrap admin (Basic Auth) → admin JWT via login
//  2. Create regular user as admin → user JWT via login
//  3. Run the three assertion cases above

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	kauth "github.com/ghbvf/gocell/framework/kernel/auth"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	accesscore "github.com/ghbvf/gocell/corecells/accesscore"
	auditcore "github.com/ghbvf/gocell/corecells/auditcore"
	configcore "github.com/ghbvf/gocell/corecells/configcore"
	syscore "github.com/ghbvf/gocell/corecells/syscore"
	"github.com/ghbvf/gocell/framework/kernel/assembly"
	"github.com/ghbvf/gocell/framework/kernel/auth/authtest"
	"github.com/ghbvf/gocell/framework/kernel/cell"
	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/observability/metrics"
	"github.com/ghbvf/gocell/framework/kernel/outbox"
	"github.com/ghbvf/gocell/framework/kernel/persistence"
	"github.com/ghbvf/gocell/framework/pkg/query"
	"github.com/ghbvf/gocell/framework/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/framework/runtime/auth"
	"github.com/ghbvf/gocell/framework/runtime/auth/keystest"
	"github.com/ghbvf/gocell/framework/runtime/bootstrap"
	"github.com/ghbvf/gocell/framework/runtime/eventbus"
)

// pdpAuditPath is the audit list endpoint served by the auditquery slice
// (generated from the http.audit.list.v1 contract).
const pdpAuditPath = "/api/v1/audit/entries"

// pdpAuditClient uses a longer timeout than testHTTPClient because bcrypt at
// production cost (~12) takes ~1-2s per hash for setup/login.
var pdpAuditClient = &http.Client{Timeout: testtime.SelectAsyncSettle}

// startCorebundlePDPApp boots a full corebundle app (ac+cc+auc) with the ABAC
// PDP wired to the primary listener via bootstrap.PrimaryAuthorizerOption. It
// returns the base URL of the running app. The caller's t.Cleanup will cancel
// the context and wait for graceful shutdown.
//
// This helper is shared by TestABACPDPGatesAuditQuery and
// TestABACPDPGatesConfigcore so that app assembly is not duplicated.
func startCorebundlePDPApp(t *testing.T) string {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	healthLn := newCorebundleLocalListener(t)

	privKey, pubKey := keystest.MustGenerateKeyPair()
	keySet, err := auth.NewKeySet(privKey, pubKey, clock.Real())
	require.NoError(t, err)
	jwtIssuer, err := auth.NewJWTIssuer(keySet, "pdp-test", testtime.D15min, clock.Real(),
		auth.WithIssuerAudiencesFromSlice([]string{"gocell"}))
	require.NoError(t, err)
	jwtVerifier, err := auth.NewJWTVerifier(keySet, clock.Real(), auth.WithExpectedAudiences("gocell"))
	require.NoError(t, err)

	eb := eventbus.New(clock.Real())
	var nw outbox.Writer = outbox.NoopWriter{}

	auditCursorCodec, err := query.NewCursorCodec([]byte("pdp-test-audit-cursor-32-bytes!!"))
	require.NoError(t, err)
	configCursorCodec, err := query.NewCursorCodec([]byte("pdp-test-cfg-cursor-key-32bytes!"))
	require.NoError(t, err)

	const pdpBootstrapUser = "pdp-test-op"
	const pdpBootstrapPass = "pdp-test-op-pass!"
	bootstrapMW := auth.NewBootstrapMiddleware(
		auth.BootstrapCredentials{
			Username: []byte(pdpBootstrapUser),
			Password: []byte(pdpBootstrapPass),
		},
		setupTestAllowAllLimiter{},
		nil,
	)
	ac := accesscore.NewAccessCore(clock.Real(), append(
		buildAccessCoreMemOptions(t, clock.Real()),
		accesscore.WithOutboxDeps(outbox.WrapPublisherForCell(eb), outbox.WrapWriterForCell(nw)),
		accesscore.WithJWTIssuer(jwtIssuer),
		accesscore.WithJWTVerifier(jwtVerifier),
		accesscore.WithMetricsProvider(metrics.NopProvider{}),
		accesscore.WithBootstrapAuth(bootstrapMW),
		accesscore.WithCASProtocol(mustNewCASProtocol(t, accesscore.PasswordVersionField)),
	)...)
	cc := configcore.NewConfigCore(
		clock.Real(),
		configcore.WithInMemoryDefaults(),
		configcore.WithOutboxDeps(outbox.WrapPublisherForCell(eb), outbox.WrapWriterForCell(nw)),
		configcore.WithTxManager(persistence.WrapForCell(noopTxRunner{})),
		configcore.WithCursorCodec(configCursorCodec),
		configcore.WithMetricsProvider(metrics.NopProvider{}),
		configcore.WithCASProtocol(mustNewCASProtocol(t, configcore.VersionField)),
	)
	auc := auditcore.NewAuditCore(clock.Real(), append([]auditcore.Option{
		auditcore.WithOutboxDeps(outbox.WrapPublisherForCell(eb), outbox.WrapWriterForCell(nw)),
		auditcore.WithTxManager(persistence.WrapForCell(noopTxRunner{})),
		auditcore.WithCursorCodec(auditCursorCodec),
		auditcore.WithMetricsProvider(metrics.NopProvider{}),
	}, auditcoreLedgerOpts(t, []byte("pdp-test-hmac-key-32-bytes-long!"))...)...)

	// syscore (#1860): stateless cell serving the aggregated cell-health endpoint.
	// Registering it makes the assembly realistic — the HealthView reports every
	// registered cell — and lets the system:read PDP gate be exercised end-to-end.
	sysc := syscore.New()

	asm := assembly.New(clock.Real(), assembly.Config{
		ID:             "pdp-test",
		DurabilityMode: outbox.DurabilityDemo,
	})
	require.NoError(t, asm.Register(ac))
	require.NoError(t, asm.Register(cc))
	require.NoError(t, asm.Register(auc))
	require.NoError(t, asm.Register(sysc))

	// Wire the ABAC PDP via bootstrap.PrimaryAuthorizerOption — mirrors production
	// run.go. This is the core subject under test: every request to the primary
	// listener carries the PDP in context, so RequirePermission drives the authz
	// decision.
	cells := []cell.Cell{ac, cc, auc, sysc}
	authzOpt, err := bootstrap.PrimaryAuthorizerOption(cells)
	require.NoError(t, err, "bootstrap.PrimaryAuthorizerOption must succeed with accesscore present")

	app := bootstrap.New(
		clock.Real(),
		bootstrap.WithAssembly(asm),
		bootstrap.WithListener(
			cell.PrimaryListener,
			ln.Addr().String(),
			[]kauth.ListenerAuth{authtest.MustAuthJWTFromAssembly(asm)},
			bootstrap.WithListenerNet(ln),
		),
		withCorebundleTestInternalListener(t, newCorebundleLocalListener(t)),
		bootstrap.WithListener(
			cell.HealthListener,
			healthLn.Addr().String(),
			[]kauth.ListenerAuth{kauth.AuthNone{}},
			bootstrap.WithListenerNet(healthLn),
		),
		bootstrap.WithPublisher(eb),
		bootstrap.WithSubscriber(eb),
		bootstrap.WithConsumerBase(newCorebundleTestConsumerBase(t, clock.Real())),
		bootstrap.WithShutdownTimeout(testtime.D2s),
		authzOpt, // ABAC PDP wired — this is the system under test
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- app.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case runErr := <-done:
			assert.NoError(t, runErr)
		case <-time.After(testtime.SelectShutdown):
			t.Fatal("bootstrap did not shut down in time")
		}
	})

	base := "http://" + ln.Addr().String()
	waitForHealthy(t, healthLn.Addr().String())
	return base
}

// provisionPDPAdminAndUser runs the four-step provisioning sequence used by the
// PDP gate tests, all scoped to tenantID:
//  1. Bootstrap admin via Basic Auth → capture adminID
//  2. Admin login → adminToken
//  3. Admin creates a regular user → capture userID
//  4. User login → userToken
//
// tenantID flows through every step (bootstrap is per-tenant, login/create are
// tenant-scoped), so calling this twice with distinct tenant IDs provisions two
// fully independent tenants in one app — the basis of the cross-tenant deny tests.
//
// Returns (adminToken, userToken, userID, adminID).
func provisionPDPAdminAndUser(t *testing.T, base, tenantID string) (adminToken, userToken, userID, adminID string) {
	t.Helper()

	const pdpBootstrapUser = "pdp-test-op"
	const pdpBootstrapPass = "pdp-test-op-pass!"
	// Usernames are per-tenant unique (the mem store keys by [tenantID][username], mirroring
	// the PG composite unique index), so the SAME username constants are reused safely across
	// tenants when this helper is called with distinct tenantIDs — no collision, and the user
	// IDs returned differ per tenant (global UUID PK).
	const pdpAdminUsername = "pdp-admin"
	const pdpAdminPassword = "PdpAdminPass!1"
	const pdpUserUsername = "pdp-regular-user"
	const pdpUserPassword = "PdpUserPass!99"

	// Step 1: bootstrap admin.
	adminID = pdpSetupAdmin(t, base, pdpBootstrapUser, pdpBootstrapPass, pdpAdminUsername, pdpAdminPassword, tenantID)

	// Step 2: admin login → JWT with roles=[admin] + tenant claim.
	adminToken = pdpLogin(t, base, pdpAdminUsername, pdpAdminPassword, tenantID)

	// Step 3: create a regular user as admin.
	userID = pdpCreateUser(t, base, adminToken, pdpUserUsername, "pdp-user@test.local", pdpUserPassword, tenantID)

	// Step 4: regular user login → JWT with roles=[] (no admin).
	userToken = pdpLogin(t, base, pdpUserUsername, pdpUserPassword, tenantID)

	return adminToken, userToken, userID, adminID
}

func TestABACPDPGatesAuditQuery(t *testing.T) {
	base := startCorebundlePDPApp(t)
	adminToken, userToken, pdpUserID, adminUserID := provisionPDPAdminAndUser(t, base, testTenantID)

	// adminUserID is the "other actor" for the non-admin cross-actor assertion:
	// it is a different subject from the regular user, so the non-admin is asking
	// for another actor's audit trail — exactly the PDP gate we want to exercise.
	otherActorID := adminUserID

	// Case 1: admin querying ANOTHER actor's audit → baseline allow → 200.
	t.Run("admin_other_actor_200", func(t *testing.T) {
		resp, body := pdpAuditReq(t, base, adminToken, otherActorID)
		assert.Equal(t, http.StatusOK, resp.StatusCode,
			"admin querying another actor must get 200 (baseline allow); body=%s", body)
	})

	// Case 2: non-admin querying ANOTHER actor's audit → PDP default-deny → 403.
	// This is the T10.4 acceptance criterion: the decision comes from the PDP
	// (RequirePermission), not a hard-coded role check in the handler.
	// F7: assert on the response body message ("insufficient permissions") to
	// distinguish a PDP deny from a fail-closed no-Authorizer 403
	// ("authorization policy engine not wired"). The admin→200 case (Case 1)
	// already proves the PDP is reachable and wired; this assertion proves the
	// deny came from the PDP path, not a configuration gap.
	t.Run("non_admin_other_actor_403", func(t *testing.T) {
		resp, body := pdpAuditReq(t, base, userToken, otherActorID)
		assert.Equal(t, http.StatusForbidden, resp.StatusCode,
			"non-admin querying another actor must get 403 (PDP default-deny); body=%s", body)
		assert.Contains(t, body, "ERR_AUTH_FORBIDDEN",
			"PDP deny must produce ERR_AUTH_FORBIDDEN; body=%s", body)
		assert.Contains(t, body, "insufficient permissions",
			"PDP deny message must be %q, not %q (which would indicate a missing Authorizer wiring); body=%s",
			"insufficient permissions", "authorization policy engine not wired", body)
	})

	// Case 3: non-admin querying their OWN audit (actorId == subject) → 200.
	// The self branch in auditQueryPolicy returns nil without consulting the PDP,
	// proving the migration did not break self-access.
	t.Run("non_admin_self_200", func(t *testing.T) {
		resp, body := pdpAuditReq(t, base, userToken, pdpUserID)
		assert.Equal(t, http.StatusOK, resp.StatusCode,
			"non-admin querying their own audit (actorId==subject) must get 200 (self branch); body=%s", body)
	})

	// Case 4 (#1860): the system:read PDP gate on GET /api/v1/admin/health/cells.
	// Proves the full wiring end-to-end: bootstrap injects the runtime HealthView
	// into the primary-listener request ctx, the syscore handler projects it, and
	// the same ABAC baseline that grants audit:read grants system:read to admin.
	t.Run("admin_system_health_200", func(t *testing.T) {
		status, body := pdpHealthReq(t, base, adminToken)
		require.Equal(t, http.StatusOK, status,
			"admin must read aggregated cell health (baseline grants system:read); body=%s", body)
		var out struct {
			Data struct {
				Overall string `json:"overall"`
				Cells   []struct {
					ID string `json:"id"`
				} `json:"cells"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal([]byte(body), &out), "body=%s", body)
		assert.NotEmpty(t, out.Data.Overall, "overall status must be set; body=%s", body)
		ids := make(map[string]bool, len(out.Data.Cells))
		for _, c := range out.Data.Cells {
			ids[c.ID] = true
		}
		// Real HealthView (not a stub): the report enumerates the registered cells.
		assert.True(t, ids["syscore"], "report must include syscore cell; body=%s", body)
		assert.True(t, ids["accesscore"], "report must include accesscore cell; body=%s", body)
	})

	t.Run("non_admin_system_health_403", func(t *testing.T) {
		status, body := pdpHealthReq(t, base, userToken)
		assert.Equal(t, http.StatusForbidden, status,
			"non-admin lacks system:read → PDP default-deny 403; body=%s", body)
	})

	t.Run("no_token_system_health_401", func(t *testing.T) {
		status, body := pdpHealthReq(t, base, "")
		assert.Equal(t, http.StatusUnauthorized, status,
			"missing bearer token → 401; body=%s", body)
	})
}

// pdpHealthReq issues GET /api/v1/admin/health/cells with an optional bearer
// token (empty token → no Authorization header, exercising the 401 path) and
// returns the status code + body. It closes the response body internally so
// callers hold no open *http.Response (bodyclose-clean).
func pdpHealthReq(t *testing.T, base, token string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, base+"/api/v1/admin/health/cells", nil)
	require.NoError(t, err)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := pdpAuditClient.Do(req)
	require.NoError(t, err)
	b, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	require.NoError(t, err)
	return resp.StatusCode, string(b)
}

// pdpSetupAdmin provisions the bootstrap admin via POST /api/v1/access/setup/admin
// in the tenant named by tenantID (the X-Tenant-ID header; bootstrap is per-tenant).
// Returns the UUID assigned to the newly created admin.
func pdpSetupAdmin(t *testing.T, base, opUser, opPass, adminUser, adminPass, tenantID string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]string{
		"username": adminUser,
		"email":    adminUser + "@test.local",
		"password": adminPass,
	})
	req, err := http.NewRequest(http.MethodPost, base+"/api/v1/access/setup/admin",
		bytes.NewReader(body))
	require.NoError(t, err)
	req.SetBasicAuth(opUser, opPass)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Tenant-ID", tenantID)
	resp, err := pdpAuditClient.Do(req)
	require.NoError(t, err)
	var result struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode, "pdp: admin setup must return 201")
	require.NotEmpty(t, result.Data.ID, "pdp: admin setup must return a non-empty ID")
	return result.Data.ID
}

// pdpLogin authenticates against tenantID (the X-Tenant-ID header scopes the user
// lookup) and returns the access token from the response. The minted JWT carries
// tenant_id=tenantID.
func pdpLogin(t *testing.T, base, username, password, tenantID string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"username": username, "password": password})
	req, err := http.NewRequest(http.MethodPost, base+"/api/v1/access/sessions/login",
		bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Tenant-ID", tenantID)
	resp, err := pdpAuditClient.Do(req)
	require.NoError(t, err)
	var result struct {
		Data struct {
			AccessToken string `json:"accessToken"`
		} `json:"data"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode, "pdp: login must return 201")
	require.NotEmpty(t, result.Data.AccessToken, "pdp: login must return a non-empty access token")
	return result.Data.AccessToken
}

// pdpCreateUser creates a user as an admin in tenantID and returns the new user's ID.
func pdpCreateUser(t *testing.T, base, adminToken, username, email, password, tenantID string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]string{
		"username": username,
		"email":    email,
		"password": password,
	})
	req, err := http.NewRequest(http.MethodPost, base+"/api/v1/access/users",
		bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Tenant-ID", tenantID)
	resp, err := pdpAuditClient.Do(req)
	require.NoError(t, err)
	var result struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode, "pdp: create user must return 201")
	require.NotEmpty(t, result.Data.ID, "pdp: created user must have a non-empty ID")
	return result.Data.ID
}

// pdpAuditReq issues GET /api/v1/audit/entries?actorId=<actorID> with the given
// Bearer token. Returns the response and body string. Body is already closed.
func pdpAuditReq(t *testing.T, base, token, actorID string) (*http.Response, string) {
	t.Helper()
	url := fmt.Sprintf("%s%s?actorId=%s", base, pdpAuditPath, actorID)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := testHTTPClient.Do(req)
	require.NoError(t, err)
	raw, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	require.NoError(t, err)
	return resp, string(raw)
}

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	accesscore "github.com/ghbvf/gocell/cells/accesscore"
	auditcore "github.com/ghbvf/gocell/cells/auditcore"
	configcore "github.com/ghbvf/gocell/cells/configcore"
	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/bootstrap"
	"github.com/ghbvf/gocell/runtime/eventbus"
)

// setupHTTPClient uses a longer timeout than the shared testHTTPClient because
// bcrypt at domain.BcryptCost=12 takes ~1-2s per password hash, which exceeds
// the 2s client-default when the CPU is contended by parallel test packages.
var setupHTTPClient = &http.Client{Timeout: 10 * time.Second}

// TestSetupEndpoints_FirstRunFlow boots a real assembly (accesscore+configcore+auditcore)
// and walks the interactive first-run admin flow end-to-end:
//
//  1. GET /api/v1/setup/status            → {hasAdmin:false}  (no JWT required)
//  2. POST /api/v1/setup/admin            → 201 + user body
//  3. POST /api/v1/setup/admin (again)    → 409 ERR_SETUP_ALREADY_INITIALIZED
//  4. GET /api/v1/setup/status            → {hasAdmin:true}
//  5. POST /api/v1/access/sessions/login  → 201 with access/refresh tokens
//
// Step 5 proves the setup-created admin can actually authenticate — i.e. the
// password was hashed and persisted correctly by bcrypt round-trip.
func TestSetupEndpoints_FirstRunFlow(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	privKey, pubKey := auth.MustGenerateTestKeyPair()
	keySet, err := auth.NewKeySet(privKey, pubKey)
	require.NoError(t, err)
	jwtIssuer, err := auth.NewJWTIssuer(keySet, "test", 15*time.Minute,
		auth.WithIssuerAudiencesFromSlice([]string{"gocell"}))
	require.NoError(t, err)
	jwtVerifier, err := auth.NewJWTVerifier(keySet, auth.WithExpectedAudiences("gocell"))
	require.NoError(t, err)

	eb := eventbus.New()
	var nw outbox.Writer = outbox.NoopWriter{}

	auditCursorCodec, err := query.NewCursorCodec([]byte("test-audit-cursor-key-32-bytes!!"))
	require.NoError(t, err)
	configCursorCodec, err := query.NewCursorCodec([]byte("test-config-cursor-key-32bytes!!"))
	require.NoError(t, err)

	ac := accesscore.NewAccessCore(
		accesscore.WithInMemoryDefaults(),
		accesscore.WithPublisher(eb),
		accesscore.WithJWTIssuer(jwtIssuer),
		accesscore.WithJWTVerifier(jwtVerifier),
		accesscore.WithOutboxWriter(nw),
		accesscore.WithTxManager(noopTxRunner{}),
	)
	cc := configcore.NewConfigCore(
		configcore.WithInMemoryDefaults(),
		configcore.WithPublisher(eb),
		configcore.WithOutboxWriter(nw),
		configcore.WithTxManager(noopTxRunner{}),
		configcore.WithCursorCodec(configCursorCodec),
	)
	auc := auditcore.NewAuditCore(
		auditcore.WithInMemoryDefaults(),
		auditcore.WithPublisher(eb),
		auditcore.WithHMACKey([]byte("test-hmac-key-32-bytes-long!!!!")),
		auditcore.WithOutboxWriter(nw),
		auditcore.WithTxManager(noopTxRunner{}),
		auditcore.WithCursorCodec(auditCursorCodec),
	)

	asm := assembly.New(assembly.Config{ID: "setup-test", DurabilityMode: cell.DurabilityDemo})
	require.NoError(t, asm.Register(ac))
	require.NoError(t, asm.Register(cc))
	require.NoError(t, asm.Register(auc))

	app := bootstrap.New(
		bootstrap.WithAssembly(asm),
		bootstrap.WithPrimaryListener(ln),
		bootstrap.WithInternalListener(newCorebundleLocalListener(t)),
		bootstrap.WithPublisher(eb), bootstrap.WithSubscriber(eb),
		bootstrap.WithShutdownTimeout(2*time.Second),
		bootstrap.WithAuthDiscovery(),
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- app.Run(ctx) }()
	defer func() {
		cancel()
		select {
		case runErr := <-done:
			assert.NoError(t, runErr)
		case <-time.After(5 * time.Second):
			t.Fatal("bootstrap did not shut down in time")
		}
	}()

	addr := ln.Addr().String()
	require.Eventually(t, func() bool {
		resp, err := setupHTTPClient.Get(fmt.Sprintf("http://%s/healthz", addr))
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, 3*time.Second, 50*time.Millisecond, "HTTP server did not become ready")

	base := "http://" + addr

	// 1. Fresh system: hasAdmin=false (endpoint is Public — no Authorization header).
	t.Run("status_before_returns_false", func(t *testing.T) {
		resp, err := setupHTTPClient.Get(base + "/api/v1/setup/status")
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusOK, resp.StatusCode, "setup/status must be Public (not 401)")
		var body struct {
			Data struct {
				HasAdmin bool `json:"hasAdmin"`
			} `json:"data"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		assert.False(t, body.Data.HasAdmin)
	})

	// 2. Create first admin.
	password := "SecretPass!23"
	t.Run("create_admin_returns_201", func(t *testing.T) {
		payload := `{"username":"root","email":"root@local","password":"` + password + `"}`
		resp, err := setupHTTPClient.Post(base+"/api/v1/setup/admin", "application/json", strings.NewReader(payload))
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusCreated, resp.StatusCode, "first setup/admin POST must return 201")
		var body struct {
			Data struct {
				ID       string `json:"id"`
				Username string `json:"username"`
			} `json:"data"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		assert.Equal(t, "root", body.Data.Username)
		assert.Contains(t, body.Data.ID, "usr-")
	})

	// 3. Second POST must 409.
	t.Run("second_create_returns_409", func(t *testing.T) {
		payload := `{"username":"root2","email":"other@local","password":"AnotherPass!99"}`
		resp, err := setupHTTPClient.Post(base+"/api/v1/setup/admin", "application/json", strings.NewReader(payload))
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusConflict, resp.StatusCode)
		raw, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		assert.Contains(t, string(raw), "ERR_SETUP_ALREADY_INITIALIZED")
	})

	// 4. Status now reports hasAdmin=true.
	t.Run("status_after_returns_true", func(t *testing.T) {
		resp, err := setupHTTPClient.Get(base + "/api/v1/setup/status")
		require.NoError(t, err)
		defer resp.Body.Close()
		var body struct {
			Data struct {
				HasAdmin bool `json:"hasAdmin"`
			} `json:"data"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		assert.True(t, body.Data.HasAdmin)
	})

	// 5. Created admin can login with the password they chose — confirms bcrypt
	//    round-trip and role assignment both succeeded.
	t.Run("created_admin_can_login", func(t *testing.T) {
		payload := `{"username":"root","password":"` + password + `"}`
		resp, err := setupHTTPClient.Post(base+"/api/v1/access/sessions/login",
			"application/json", strings.NewReader(payload))
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusCreated, resp.StatusCode, "setup-created admin must be able to login")
		var body struct {
			Data struct {
				AccessToken  string `json:"accessToken"`
				RefreshToken string `json:"refreshToken"`
			} `json:"data"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		assert.NotEmpty(t, body.Data.AccessToken)
		assert.NotEmpty(t, body.Data.RefreshToken)
	})
}

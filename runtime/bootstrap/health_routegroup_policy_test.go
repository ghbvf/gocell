package bootstrap

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRouteGroupPolicy_VerboseTokenOnReadyz_E2E is the regression test owed
// from R2-01: WithHealthRoutes(WithReadyzPolicy(PolicyVerboseToken(...)))
// must actually gate /readyz?verbose. Pre-F1 mountOneRouteGroup ignored
// rg.Policy entirely, so the verbose-token guard was silently disabled and
// every previous unit test passed. This test starts a real bootstrap, sends
// /readyz?verbose with three header configurations, and asserts the policy
// runs in the request path.
func TestRouteGroupPolicy_VerboseTokenOnReadyz_E2E(t *testing.T) {
	const (
		headerName = "X-Readyz-Token"
		token      = "round-3-secret"
	)

	asm := assembly.New(assembly.Config{ID: "f1-test", DurabilityMode: cell.DurabilityDemo})
	require.NoError(t, asm.Register(newTestCell("cell-1")))

	healthLn := newLocalListener(t)
	healthAddr := healthLn.Addr().String()

	app := New(
		WithAssembly(asm),
		WithListener(cell.HealthListener, healthAddr, nil, WithListenerNet(healthLn)),
		WithHealthRoutes(
			WithReadyzAuth(cell.NewAuthVerboseToken(headerName, token)),
		),
		WithShutdownTimeout(2*time.Second),
	)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- app.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-errCh:
			assert.NoError(t, err, "bootstrap must shut down cleanly")
		case <-time.After(5 * time.Second):
			t.Error("bootstrap did not shut down in time")
		}
	})

	waitForHealthy(t, healthAddr)

	cases := []struct {
		name       string
		path       string
		headers    map[string]string
		wantStatus int
		why        string
	}{
		{
			name:       "no_verbose_query_passes_through",
			path:       "/readyz",
			wantStatus: http.StatusOK,
			why:        "PolicyVerboseToken must let non-verbose requests through unchanged",
		},
		{
			name:       "verbose_with_correct_token_200",
			path:       "/readyz?verbose=true",
			headers:    map[string]string{headerName: token},
			wantStatus: http.StatusOK,
			why:        "verbose request with matching token must reach the readyz handler",
		},
		{
			name:       "verbose_missing_token_401",
			path:       "/readyz?verbose=true",
			wantStatus: http.StatusUnauthorized,
			why:        "verbose request without token must be 401'd by PolicyVerboseToken (regression: pre-F1 was 200 because rg.Policy was dropped)",
		},
		{
			name:       "verbose_wrong_token_401",
			path:       "/readyz?verbose=true",
			headers:    map[string]string{headerName: "wrong"},
			wantStatus: http.StatusUnauthorized,
			why:        "verbose request with mismatched token must be 401'd",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("http://%s%s", healthAddr, tc.path), nil)
			require.NoError(t, err)
			for k, v := range tc.headers {
				req.Header.Set(k, v)
			}
			resp, err := testHTTPClient.Do(req)
			require.NoError(t, err)
			defer resp.Body.Close()
			assert.Equal(t, tc.wantStatus, resp.StatusCode, tc.why)
		})
	}
}

// TestRouteGroupPolicy_LivezUnaffectedByReadyzPolicy verifies the per-group
// scope guarantee: a Policy on the readyz group must not bleed into the livez
// group on the same listener. Sibling groups stay independent.
func TestRouteGroupPolicy_LivezUnaffectedByReadyzPolicy(t *testing.T) {
	asm := assembly.New(assembly.Config{ID: "f1-livez-test", DurabilityMode: cell.DurabilityDemo})
	require.NoError(t, asm.Register(newTestCell("cell-1")))

	healthLn := newLocalListener(t)
	healthAddr := healthLn.Addr().String()

	app := New(
		WithAssembly(asm),
		WithListener(cell.HealthListener, healthAddr, nil, WithListenerNet(healthLn)),
		WithHealthRoutes(
			WithReadyzAuth(cell.NewAuthVerboseToken("X-Readyz-Token", "secret")),
		),
		WithShutdownTimeout(2*time.Second),
	)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- app.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-errCh
	})

	waitForHealthy(t, healthAddr)

	// /healthz must not be gated by readyz's PolicyVerboseToken.
	resp, err := testHTTPClient.Get(fmt.Sprintf("http://%s/healthz?verbose=true", healthAddr))
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode,
		"livez group must not inherit the readyz group's PolicyVerboseToken; per-group scope is the F1 contract")
}

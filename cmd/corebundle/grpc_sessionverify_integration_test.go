//go:build integration

package main

// grpc_sessionverify_integration_test.go — #1154 forced-wiring regression guard:
// boots a corebundle-style assembly through the PRODUCTION gRPC wiring path
// (grpclistener.ServerFromEnv + bootstrap.WithGRPCListener) and dials the
// resulting listener, proving accesscore's grpc.auth.session.verify.v1 service is
// actually served + the auth gate is active on the wired listener — not just that
// bootstrap did not fail-fast (checkOrphanGRPCServices).
//
// The PDP allow/deny matrix is covered cell-scoped in
// corecells/accesscore/grpc_pdp_gate_test.go; this test only proves the corebundle
// listener wiring serves a real call (single happy-path: a gated call with no bearer
// is rejected by the chain → Unauthenticated, which can only happen if the listener
// is up, serving, and the auth interceptor is wired).

import (
	"context"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	"github.com/ghbvf/gocell/cellmodules/grpclistener"
	accesscore "github.com/ghbvf/gocell/corecells/accesscore"
	"github.com/ghbvf/gocell/framework/kernel/assembly"
	kauth "github.com/ghbvf/gocell/framework/kernel/auth"
	"github.com/ghbvf/gocell/framework/kernel/auth/authtest"
	"github.com/ghbvf/gocell/framework/kernel/cell"
	"github.com/ghbvf/gocell/framework/kernel/clock"
	kernelmetrics "github.com/ghbvf/gocell/framework/kernel/observability/metrics"
	"github.com/ghbvf/gocell/framework/kernel/outbox"
	"github.com/ghbvf/gocell/framework/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/framework/runtime/auth"
	"github.com/ghbvf/gocell/framework/runtime/auth/keystest"
	"github.com/ghbvf/gocell/framework/runtime/bootstrap"
	"github.com/ghbvf/gocell/framework/runtime/eventbus"
	"github.com/ghbvf/gocell/framework/runtime/grpc/interceptor"
	obmetrics "github.com/ghbvf/gocell/framework/runtime/observability/metrics"
	sessionverifyv1 "github.com/ghbvf/gocell/generated/contracts/grpc/auth/session/verify/v1"
)

// startSessionVerifyGRPCApp boots an accesscore-only assembly with the gRPC listener
// wired through the production grpc.go helper (newGRPCServerFromEnv) and returns the
// gRPC listen address. It mirrors the production run.go wiring: one authorizer feeds
// both the HTTP primary listener and the gRPC gate.
func startSessionVerifyGRPCApp(t *testing.T) string {
	t.Helper()

	primaryLn := newCorebundleLocalListener(t)
	healthLn := newCorebundleLocalListener(t)
	grpcLn, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	privKey, pubKey := keystest.MustGenerateKeyPair()
	keySet, err := auth.NewKeySet(privKey, pubKey, clock.Real())
	require.NoError(t, err)
	jwtIssuer, err := auth.NewJWTIssuer(keySet, "grpc-sv-test", testtime.D15min, clock.Real(),
		auth.WithIssuerAudiencesFromSlice([]string{"gocell"}))
	require.NoError(t, err)
	jwtVerifier, err := auth.NewJWTVerifier(keySet, clock.Real(), auth.WithExpectedAudiences("gocell"))
	require.NoError(t, err)

	eb := eventbus.New(clock.Real())
	var nw outbox.Writer = outbox.NoopWriter{}

	bootstrapMW := auth.NewBootstrapMiddleware(
		auth.BootstrapCredentials{Username: []byte("grpc-sv-op"), Password: []byte("grpc-sv-op-pass!")},
		setupTestAllowAllLimiter{}, nil,
	)
	ac := accesscore.NewAccessCore(clock.Real(), append(
		buildAccessCoreMemOptions(t, clock.Real()),
		accesscore.WithOutboxDeps(outbox.WrapPublisherForCell(eb), outbox.WrapWriterForCell(nw)),
		accesscore.WithJWTIssuer(jwtIssuer),
		accesscore.WithJWTVerifier(jwtVerifier),
		accesscore.WithMetricsProvider(kernelmetrics.NopProvider{}),
		accesscore.WithBootstrapAuth(bootstrapMW),
		accesscore.WithCASProtocol(mustNewCASProtocol(t, accesscore.PasswordVersionField)),
	)...)

	asm := assembly.New(clock.Real(), assembly.Config{ID: "grpc-sv-test", DurabilityMode: outbox.DurabilityDemo})
	require.NoError(t, asm.Register(ac))

	cells := []cell.Cell{ac}
	authorizer, err := bootstrap.AuthorizerFromCells(cells)
	require.NoError(t, err)
	grpcCollector, err := obmetrics.NewGRPCProviderCollector(kernelmetrics.NopProvider{}, obmetrics.ProviderCollectorConfig{})
	require.NoError(t, err)
	// Production gRPC wiring path: grpclistener.ServerFromEnv builds the adapter
	// server from the interceptor.Deps, exactly as run.go does.
	grpcServer, err := grpclistener.ServerFromEnv(outbox.DurabilityDemo, grpcLn.Addr().String(), interceptor.Deps{
		Verifier:        jwtVerifier,
		Clock:           clock.Real(),
		Collector:       grpcCollector,
		Authorizer:      authorizer,
		MetricsProvider: kernelmetrics.NopProvider{},
		CellIDClosedSet: asm.CellIDs(),
	})
	require.NoError(t, err)

	app := bootstrap.New(
		clock.Real(),
		bootstrap.WithAssembly(asm),
		bootstrap.WithListener(
			cell.PrimaryListener, primaryLn.Addr().String(),
			[]kauth.ListenerAuth{authtest.MustAuthJWTFromAssembly(asm)},
			bootstrap.WithListenerNet(primaryLn),
		),
		withCorebundleTestInternalListener(t, newCorebundleLocalListener(t)),
		bootstrap.WithListener(
			cell.HealthListener, healthLn.Addr().String(),
			[]kauth.ListenerAuth{kauth.AuthNone{}},
			bootstrap.WithListenerNet(healthLn),
		),
		bootstrap.WithGRPCListener(cell.PrimaryListener, grpcServer, grpcLn.Addr().String(), bootstrap.WithGRPCListenerNet(grpcLn)),
		bootstrap.WithPublisher(eb),
		bootstrap.WithSubscriber(eb),
		bootstrap.WithConsumerBase(newCorebundleTestConsumerBase(t, clock.Real())),
		bootstrap.WithShutdownTimeout(testtime.D2s),
		bootstrap.WithPrimaryAuthorizer(authorizer),
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- app.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	waitForHealthy(t, healthLn.Addr().String())
	return grpcLn.Addr().String()
}

// TestSessionVerifyGRPC_Corebundle_ListenerServed proves the forced gRPC wiring is
// live end-to-end: a VerifyToken call with no bearer reaches the corebundle-wired
// listener and is rejected by the auth interceptor (Unauthenticated). The bundle
// booting at all (waitForHealthy) already proves the orphan-grpc fail-fast did not
// trip; this additionally proves the listener serves and is gated.
func TestSessionVerifyGRPC_Corebundle_ListenerServed(t *testing.T) {
	grpcAddr := startSessionVerifyGRPCApp(t)

	conn, err := grpc.NewClient(grpcAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	client := sessionverifyv1.NewSessionVerifyServiceClient(conn)

	_, err = client.VerifyToken(context.Background(), &sessionverifyv1.VerifyTokenRequest{Token: "any-subject-token"})
	assert.Equal(t, codes.Unauthenticated, status.Code(err),
		"a gated call with no bearer must be Unauthenticated — proving the wired listener serves + the auth gate is active")
}

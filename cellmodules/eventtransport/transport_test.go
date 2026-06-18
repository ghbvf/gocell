package eventtransport

import (
	"context"
	"errors"
	"testing"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/adapters/rabbitmq"
	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/runtime/bootstrap"
)

// mkTopo builds a validated bootstrap.Topology for tests. postgres requires the
// "real" adapter mode (Topology coupling rule), so the helper pairs them.
func mkTopo(t *testing.T, storageBackend string) bootstrap.Topology {
	t.Helper()
	adapterMode := ""
	singlePod := false
	if storageBackend == "postgres" {
		adapterMode = "real"
		singlePod = true
	}
	topo, err := bootstrap.NewTopology(adapterMode, storageBackend, singlePod)
	require.NoError(t, err, "NewTopology(%q,%q)", adapterMode, storageBackend)
	return topo
}

// --- fake AMQP connection: lets the RabbitMQ branch construct without a live
// broker. connect() only dials + stores the conn (it does not acquire a channel
// during construction), so a 4-method fake is sufficient. ---

// errFakeChannelUnused is returned by fakeAMQPConn.Channel; the fake never has
// Channel called in these tests (Resolve only constructs; it never publishes or
// subscribes), so returning a sentinel error is both correct and avoids the
// nilnil lint (a nil value paired with a nil error).
var errFakeChannelUnused = errors.New("fakeAMQPConn: Channel not used by Resolve")

type fakeAMQPConn struct{}

func (fakeAMQPConn) Channel() (rabbitmq.AMQPChannel, error)          { return nil, errFakeChannelUnused }
func (fakeAMQPConn) NotifyClose(c chan *amqp.Error) chan *amqp.Error { return c }
func (fakeAMQPConn) IsClosed() bool                                  { return false }
func (fakeAMQPConn) Close() error                                    { return nil }

func okDial(string) (rabbitmq.AMQPConnection, error) { return fakeAMQPConn{}, nil }

// fakeDialOpt is the connOpts slice that makes the postgres → RabbitMQ branch
// constructible without a live broker.
func fakeDialOpt() []rabbitmq.ConnectionOption {
	return []rabbitmq.ConnectionOption{rabbitmq.WithDialFunc(okDial)}
}

// TestDedupBrokerURL exercises the pure per-cell broker-URL dedup gate directly
// (the broker-side twin of percellpg.Resolve): same URL → agreed (alphabetically
// first) cell URL; distinct/empty/missing → fail-closed.
func TestDedupBrokerURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		cells      map[string]string
		wantURL    string
		wantErr    bool
		wantErrSub string
	}{
		{
			name:    "single cell yields its url",
			cells:   map[string]string{"configcore": "amqp://h:5672/"},
			wantURL: "amqp://h:5672/",
		},
		{
			name: "three cells identical url dedup to one (agreed = first alpha)",
			cells: map[string]string{
				"configcore": "amqp://shared:5672/",
				"accesscore": "amqp://shared:5672/",
				"auditcore":  "amqp://shared:5672/",
			},
			wantURL: "amqp://shared:5672/",
		},
		{
			name: "whitespace-only difference still dedups to one",
			cells: map[string]string{
				"accesscore": "amqp://shared:5672/",
				"configcore": "  amqp://shared:5672/  ",
			},
			wantURL: "amqp://shared:5672/",
		},
		{
			name:       "empty cell set fail-closed",
			cells:      map[string]string{},
			wantErr:    true,
			wantErrSub: "at least one broker cell",
		},
		{
			name:  "missing per-cell url fail-closed names env var",
			cells: map[string]string{"configcore": ""},
			// errcode.Error() surfaces the precise internal attrs (the derived
			// per-cell env var) in place of the const guidance message; the
			// operator-facing prose lives in e.Message (structured logs).
			wantErr:    true,
			wantErrSub: "env_var=GOCELL_CONFIGCORE_AMQP_URL",
		},
		{
			name: "distinct urls fail-closed (egress-only)",
			cells: map[string]string{
				"accesscore": "amqp://a:5672/",
				"configcore": "amqp://b:5672/",
			},
			wantErr:    true,
			wantErrSub: "distinct_url_count=2",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			url, err := dedupBrokerURL(tc.cells)
			if tc.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErrSub)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantURL, url)
		})
	}
}

func TestResolveBrokerSpec(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		storage    string
		cells      map[string]string
		wantKind   brokerKind
		wantURL    string
		wantErr    bool
		wantErrSub string
	}{
		{
			name:     "demo/memory selects in-memory (cells ignored)",
			storage:  "memory",
			cells:    map[string]string{"configcore": "amqp://ignored"},
			wantKind: brokerInMemory,
		},
		{
			name:     "demo/memory selects in-memory (no cells)",
			storage:  "memory",
			wantKind: brokerInMemory,
		},
		{
			name:     "postgres + single url selects rabbitmq",
			storage:  "postgres",
			cells:    map[string]string{"configcore": "amqp://localhost:5672/"},
			wantKind: brokerRabbitMQ,
			wantURL:  "amqp://localhost:5672/",
		},
		{
			name:    "postgres + colocated identical urls dedup to one",
			storage: "postgres",
			cells: map[string]string{
				"configcore": "amqp://localhost:5672/",
				"accesscore": "amqp://localhost:5672/",
			},
			wantKind: brokerRabbitMQ,
			wantURL:  "amqp://localhost:5672/",
		},
		{
			name:       "postgres without cells fail-closed",
			storage:    "postgres",
			wantErr:    true,
			wantErrSub: "at least one broker cell",
		},
		{
			name:       "postgres with empty url fail-closed",
			storage:    "postgres",
			cells:      map[string]string{"configcore": ""},
			wantErr:    true,
			wantErrSub: "env_var=GOCELL_CONFIGCORE_AMQP_URL",
		},
		{
			name:    "postgres with distinct urls fail-closed",
			storage: "postgres",
			cells: map[string]string{
				"accesscore": "amqp://a:5672/",
				"configcore": "amqp://b:5672/",
			},
			wantErr:    true,
			wantErrSub: "distinct_url_count=2",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			spec, err := resolveBrokerSpec(mkTopo(t, tc.storage), Config{Cells: tc.cells})
			if tc.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErrSub)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantKind, spec.kind)
			assert.Equal(t, tc.wantURL, spec.url)
		})
	}
}

func TestResolve_Demo_InMemorySharedInstance(t *testing.T) {
	t.Parallel()

	tr, err := Resolve(clock.Real(), mkTopo(t, "memory"), Config{})
	require.NoError(t, err)
	require.NotNil(t, tr.Publisher)
	require.NotNil(t, tr.Subscriber)
	assert.Empty(t, tr.Resources, "demo transport owns no managed resources")
	// In-memory pub and sub must be the SAME instance so in-process publish is
	// visible to in-process subscribers.
	assert.Same(t, tr.Publisher, tr.Subscriber,
		"demo Publisher and Subscriber must be the same in-memory bus instance")
}

func TestResolve_Postgres_EmptyCells_FailClosed(t *testing.T) {
	t.Parallel()

	_, err := Resolve(clock.Real(), mkTopo(t, "postgres"), Config{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "at least one broker cell",
		"postgres topology with no broker cells must fail-fast, not silently use in-memory")
}

func TestResolve_Postgres_MissingURL_FailClosed(t *testing.T) {
	t.Parallel()

	_, err := Resolve(clock.Real(), mkTopo(t, "postgres"), Config{Cells: map[string]string{"configcore": ""}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "env_var=GOCELL_CONFIGCORE_AMQP_URL",
		"postgres without a per-cell broker URL must fail-fast naming the missing env var, not silently use in-memory")
	// The operator-facing guidance prose lives in e.Message (rendered by structured
	// slog), distinct from the precise internal attrs that Error() surfaces. Lock it
	// so the actionable guidance cannot silently disappear from a future refactor.
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Contains(t, ec.Message, "GOCELL_<CELLID>_AMQP_URL",
		"the guidance message must name the per-cell env var pattern for operators")
}

// TestResolve_Postgres_DistinctURLs_FailClosed is the egress-only boundary: with
// PR-2 wiring only the relay (publisher) fans out, while the single subscriber can
// consume just one broker, so distinct per-cell broker URLs would orphan events.
// They are rejected at resolve time, pointing operators at the colocated
// requirement (and the ingress fan-out follow-up tracked under #2152 / #2341).
func TestResolve_Postgres_DistinctURLs_FailClosed(t *testing.T) {
	t.Parallel()

	_, err := Resolve(clock.Real(), mkTopo(t, "postgres"), Config{
		Cells: map[string]string{
			"accesscore": "amqp://a:5672/",
			"configcore": "amqp://b:5672/",
		},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "distinct_url_count=2")
}

func TestResolve_Postgres_RabbitMQ_BundlesConnAsResource(t *testing.T) {
	t.Parallel()

	tr, err := Resolve(
		clock.Real(),
		mkTopo(t, "postgres"),
		Config{Cells: map[string]string{"configcore": "amqp://localhost:5672/"}, connOpts: fakeDialOpt()},
	)
	require.NoError(t, err)
	require.NotNil(t, tr.Publisher)
	require.NotNil(t, tr.Subscriber)
	require.Len(t, tr.Resources, 1, "rabbitmq transport returns its connection as one managed resource")

	// The resource is the broker connection; closing it releases the transport.
	require.NoError(t, tr.Resources[0].Close(context.Background()))
}

// TestResolve_Postgres_Colocated_BuildsSingleBroker proves the colocated dedup
// path is behavior-preserving: N cells with an identical URL open exactly one
// broker connection (the agreed, alphabetically-first cell's URL).
func TestResolve_Postgres_Colocated_BuildsSingleBroker(t *testing.T) {
	t.Parallel()

	tr, err := Resolve(
		clock.Real(),
		mkTopo(t, "postgres"),
		Config{
			Cells: map[string]string{
				"configcore": "amqp://shared:5672/",
				"accesscore": "amqp://shared:5672/",
				"auditcore":  "amqp://shared:5672/",
			},
			connOpts: fakeDialOpt(),
		},
	)
	require.NoError(t, err)
	require.Len(t, tr.Resources, 1, "colocated cells with identical URL open exactly one broker connection")
	require.NoError(t, tr.Resources[0].Close(context.Background()))
}

// TestResolve_TransportKind verifies Resolve stamps the sealed
// bootstrap.EventTransportKind that the phase0 broker-mandatory gate trusts
// (#2211): demo/memory → not a real broker; postgres rabbitmq → real broker.
// This sealed fact is what replaces the gate's StorageBackend()=="postgres"
// proxy — the eventtransport resolver is the single sanctioned minter of the
// real-broker variant (EVENT-TRANSPORT-KIND-MINTER-FUNNEL-01).
func TestResolve_TransportKind(t *testing.T) {
	t.Parallel()

	demo, err := Resolve(clock.Real(), mkTopo(t, "memory"), Config{})
	require.NoError(t, err)
	assert.False(t, demo.Kind.IsRealBroker(),
		"demo/memory transport must not report a real broker (in-process bus)")

	pg, err := Resolve(
		clock.Real(),
		mkTopo(t, "postgres"),
		Config{Cells: map[string]string{"configcore": "amqp://localhost:5672/"}, connOpts: fakeDialOpt()},
	)
	require.NoError(t, err)
	assert.True(t, pg.Kind.IsRealBroker(),
		"postgres rabbitmq transport must report a real broker")
	require.NoError(t, pg.Resources[0].Close(context.Background()))
}

func TestDLXExchange_StableName(t *testing.T) {
	t.Parallel()
	// The DLX exchange name is an operations contract (broker topology); pin it so
	// a drift is a deliberate, reviewed change (renaming requires migrating in-flight
	// dead letters). The rabbitmq adapter fail-fasts at Setup if it is empty.
	assert.Equal(t, "gocell.events.dlx", dlxExchange)
	assert.NotEmpty(t, dlxExchange, "DLXExchange must be non-empty or rabbitmq Setup fail-fasts")
}

func TestDispatchTransport_UnhandledBrokerKind_FailClosed(t *testing.T) {
	t.Parallel()
	// White-box: resolveBrokerSpec can only emit the two valid kinds, so the
	// fail-closed default is unreachable via Resolve. Exercise it directly to prove
	// a future brokerKind added without a dispatch arm refuses to start (rather than
	// silently falling back to a wrong transport).
	_, err := dispatchTransport(clock.Real(), brokerSpec{kind: brokerKind(99)}, Config{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unhandled broker kind")
}

func TestResolve_Postgres_RabbitMQ_DialFailureFailsFast(t *testing.T) {
	t.Parallel()

	dialErr := errors.New("broker unreachable")
	_, err := Resolve(
		clock.Real(),
		mkTopo(t, "postgres"),
		Config{
			Cells: map[string]string{"configcore": "amqp://localhost:5672/"},
			connOpts: []rabbitmq.ConnectionOption{rabbitmq.WithDialFunc(
				func(string) (rabbitmq.AMQPConnection, error) { return nil, dialErr },
			)},
		},
	)
	require.Error(t, err, "an unreachable broker must fail-fast at resolve time, not silently degrade")
	assert.Contains(t, err.Error(), "rabbitmq connection")
}

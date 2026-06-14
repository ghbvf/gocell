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

func TestResolveBrokerSpec(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		storage    string
		amqpURL    string
		wantKind   brokerKind
		wantURL    string
		wantErr    bool
		wantErrSub string
	}{
		{
			name:     "demo/memory selects in-memory (url ignored)",
			storage:  "memory",
			amqpURL:  "amqp://ignored",
			wantKind: brokerInMemory,
		},
		{
			name:     "demo/memory selects in-memory (no url)",
			storage:  "memory",
			wantKind: brokerInMemory,
		},
		{
			name:     "postgres + url selects rabbitmq",
			storage:  "postgres",
			amqpURL:  "amqp://localhost:5672/",
			wantKind: brokerRabbitMQ,
			wantURL:  "amqp://localhost:5672/",
		},
		{
			name:       "postgres without url fail-closed",
			storage:    "postgres",
			wantErr:    true,
			wantErrSub: "GOCELL_AMQP_URL",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			spec, err := resolveBrokerSpec(mkTopo(t, tc.storage), Config{AMQPURL: tc.amqpURL})
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

func TestResolve_Postgres_NoBrokerURL_FailClosed(t *testing.T) {
	t.Parallel()

	_, err := Resolve(clock.Real(), mkTopo(t, "postgres"), Config{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "GOCELL_AMQP_URL",
		"postgres without broker URL must fail-fast, not silently use in-memory")
}

func TestResolve_Postgres_RabbitMQ_BundlesConnAsResource(t *testing.T) {
	t.Parallel()

	tr, err := Resolve(
		clock.Real(),
		mkTopo(t, "postgres"),
		Config{AMQPURL: "amqp://localhost:5672/", connOpts: []rabbitmq.ConnectionOption{rabbitmq.WithDialFunc(okDial)}},
	)
	require.NoError(t, err)
	require.NotNil(t, tr.Publisher)
	require.NotNil(t, tr.Subscriber)
	require.Len(t, tr.Resources, 1, "rabbitmq transport returns its connection as one managed resource")

	// The resource is the broker connection; closing it releases the transport.
	require.NoError(t, tr.Resources[0].Close(context.Background()))
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
			AMQPURL: "amqp://localhost:5672/",
			connOpts: []rabbitmq.ConnectionOption{rabbitmq.WithDialFunc(
				func(string) (rabbitmq.AMQPConnection, error) { return nil, dialErr },
			)},
		},
	)
	require.Error(t, err, "an unreachable broker must fail-fast at resolve time, not silently degrade")
	assert.Contains(t, err.Error(), "rabbitmq connection")
}

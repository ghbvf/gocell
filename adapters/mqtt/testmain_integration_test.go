//go:build integration

package mqtt

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/tests/testutil"
)

// mosquittoConf is the in-memory broker configuration mounted into the
// eclipse-mosquitto container. Anonymous auth + listener on 0.0.0.0:1883.
const mosquittoConf = `listener 1883 0.0.0.0
allow_anonymous true
`

var (
	sharedBrokerOnce     sync.Once
	sharedBrokerURLValue string
	sharedBrokerShutdown func()
	sharedBrokerStartErr error
)

// sharedBrokerURL returns the URL of a shared Mosquitto MQTT container started
// lazily on first call. Subsequent tests in the same package share the broker —
// fast and avoids per-test container startup cost. Cleanup happens in TestMain
// after all tests in the package finish.
//
// RequireDocker is called here so individual tests that call sharedBrokerURL
// get the correct skip/fatal behavior when Docker is absent.
func sharedBrokerURL(t *testing.T) string {
	t.Helper()
	testutil.RequireDocker(t)
	sharedBrokerOnce.Do(func() {
		sharedBrokerURLValue, sharedBrokerShutdown, sharedBrokerStartErr = startMosquittoContainer(t)
	})
	if sharedBrokerStartErr != nil {
		t.Fatalf("mqtt: shared broker start: %v", sharedBrokerStartErr)
	}
	return sharedBrokerURLValue
}

// startMosquittoContainer brings up an eclipse-mosquitto v2.0 MQTT broker with
// anonymous auth. The conf file is mounted in-memory via ContainerFile so no
// host filesystem access is required. t is taken so RequireDocker can gate the
// container start in this function (INTEGRATION-GUARD-01), even though
// sharedBrokerURL also calls it for the cross-helper path.
//
// ref: adapters/otel/integration_test.go (GenericContainer + ContainerFile pattern)
func startMosquittoContainer(t *testing.T) (string, func(), error) {
	t.Helper()
	testutil.RequireDocker(t) // INTEGRATION-GUARD: fail-fast/skip before starting a testcontainer
	ctx := context.Background()
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        testutil.MosquittoImage,
			ExposedPorts: []string{"1883/tcp"},
			Files: []testcontainers.ContainerFile{
				{
					Reader:            strings.NewReader(mosquittoConf),
					ContainerFilePath: "/mosquitto/config/mosquitto.conf",
					FileMode:          0o644,
				},
			},
			WaitingFor: wait.ForListeningPort("1883/tcp").
				WithStartupTimeout(testtime.D30s),
		},
		Started: true,
	})
	if err != nil {
		return "", nil, fmt.Errorf("mqtt broker container start: %w", err)
	}
	host, err := container.Host(ctx)
	if err != nil {
		_ = container.Terminate(ctx)
		return "", nil, fmt.Errorf("mqtt broker host: %w", err)
	}
	port, err := container.MappedPort(ctx, "1883/tcp")
	if err != nil {
		_ = container.Terminate(ctx)
		return "", nil, fmt.Errorf("mqtt broker mapped port: %w", err)
	}
	url := fmt.Sprintf("tcp://%s:%s", host, port.Port())
	shutdown := func() {
		termCtx, cancel := context.WithTimeout(context.Background(), testtime.D10s)
		defer cancel()
		_ = container.Terminate(termCtx)
	}
	return url, shutdown, nil
}

// TestMain runs all tests in this package and then tears down the shared
// broker. Mirrors adapters/rabbitmq/testmain_integration_test.go.
func TestMain(m *testing.M) {
	os.Exit(runTestsWithShutdown(m))
}

// runTestsWithShutdown runs m.Run() and defers broker shutdown so the
// container is torn down even if m.Run() panics before returning. It also stops
// the shared in-process mochi broker: the untagged unit tests (which start it)
// are compiled into the integration binary too, so it would otherwise leak here.
func runTestsWithShutdown(m *testing.M) int {
	defer func() {
		if sharedBrokerShutdown != nil {
			sharedBrokerShutdown()
		}
		stopSharedInternalBroker()
	}()
	return m.Run()
}

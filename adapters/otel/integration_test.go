//go:build integration && otelcollector

package otel_test

import (
	"context"
	"io"
	"net/netip"
	"strings"
	"testing"
	"time"

	gcotel "github.com/ghbvf/gocell/adapters/otel"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/tests/testutil"
	dockercontainer "github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

const collectorConfig = `
receivers:
  otlp:
    protocols:
      grpc:
        endpoint: 0.0.0.0:4317

exporters:
  debug:
    verbosity: detailed

service:
  pipelines:
    traces:
      receivers: [otlp]
      exporters: [debug]
`

func TestNewTracer_ExportsSpanToOTLPCollector(t *testing.T) {
	testutil.RequireDocker(t)

	ctx, cancel := context.WithTimeout(context.Background(), testtime.D2min)
	defer cancel()

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        testutil.OTelCollectorImage,
			ExposedPorts: []string{"4317/tcp"},
			Cmd:          []string{"--config=/etc/otelcol/config.yaml"},
			HostConfigModifier: func(hostConfig *dockercontainer.HostConfig) {
				hostConfig.PortBindings = network.PortMap{
					network.MustParsePort("4317/tcp"): []network.PortBinding{
						{
							HostIP:   netip.MustParseAddr("127.0.0.1"),
							HostPort: "0",
						},
					},
				}
			},
			Files: []testcontainers.ContainerFile{
				{
					Reader:            strings.NewReader(collectorConfig),
					ContainerFilePath: "/etc/otelcol/config.yaml",
					FileMode:          0o644,
				},
			},
			WaitingFor: wait.ForListeningPort("4317/tcp").
				WithStartupTimeout(testtime.D1min),
		},
		Started: true,
	})
	require.NoError(t, err, "start otel collector")
	t.Cleanup(func() {
		require.NoError(t, container.Terminate(context.Background()), "terminate otel collector")
	})

	endpoint, err := container.PortEndpoint(ctx, "4317/tcp", "")
	require.NoError(t, err, "get collector OTLP endpoint")

	tracer, shutdown, err := gcotel.NewTracer(ctx, gcotel.TracerConfig{
		ServiceName:      "gocell-otel-integration",
		ExporterEndpoint: endpoint,
		Insecure:         true,
	})
	require.NoError(t, err, "create OTLP tracer")

	_, span := tracer.Start(context.Background(), "otelcollector-round-trip")
	span.End()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), testtime.SelectAsyncSettle)
	defer shutdownCancel()
	require.NoError(t, shutdown(shutdownCtx), "flush span to collector")

	waitForCollectorLog(t, container, "otelcollector-round-trip")
}

func waitForCollectorLog(t *testing.T, container testcontainers.Container, want string) {
	t.Helper()
	deadline := time.Now().Add(testtime.SelectAsyncSettle)
	var lastLogs string
	for time.Now().Before(deadline) {
		logs, err := container.Logs(context.Background())
		require.NoError(t, err, "read collector logs")
		buf, err := io.ReadAll(logs)
		require.NoError(t, err, "drain collector logs")
		require.NoError(t, logs.Close(), "close collector logs")
		lastLogs = string(buf)
		if strings.Contains(lastLogs, want) {
			return
		}
		time.Sleep(testtime.D200ms) //archtest:allow:test-sleep poll loop waiting for otel collector to receive spans; no push notification API
	}
	t.Fatalf("collector logs did not contain %q; logs:\n%s", want, lastLogs)
}

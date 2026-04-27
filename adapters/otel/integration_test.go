//go:build integration && otelcollector

package otel_test

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	dockercontainer "github.com/docker/docker/api/types/container"
	"github.com/docker/go-connections/nat"
	gcotel "github.com/ghbvf/gocell/adapters/otel"
	"github.com/ghbvf/gocell/tests/testutil"
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

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        testutil.OTelCollectorImage,
			ExposedPorts: []string{"4317/tcp"},
			Cmd:          []string{"--config=/etc/otelcol/config.yaml"},
			HostConfigModifier: func(hostConfig *dockercontainer.HostConfig) {
				hostConfig.PortBindings = nat.PortMap{
					nat.Port("4317/tcp"): []nat.PortBinding{
						{
							HostIP:   "127.0.0.1",
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
			WaitingFor: wait.ForListeningPort(nat.Port("4317/tcp")).
				WithStartupTimeout(time.Minute),
		},
		Started: true,
	})
	require.NoError(t, err, "start otel collector")
	t.Cleanup(func() {
		require.NoError(t, container.Terminate(context.Background()), "terminate otel collector")
	})

	endpoint, err := container.PortEndpoint(ctx, nat.Port("4317/tcp"), "")
	require.NoError(t, err, "get collector OTLP endpoint")

	tracer, shutdown, err := gcotel.NewTracer(ctx, gcotel.TracerConfig{
		ServiceName:      "gocell-otel-integration",
		ExporterEndpoint: endpoint,
		Insecure:         true,
	})
	require.NoError(t, err, "create OTLP tracer")

	_, span := tracer.Start(context.Background(), "otelcollector-round-trip")
	span.End()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	require.NoError(t, shutdown(shutdownCtx), "flush span to collector")

	waitForCollectorLog(t, container, "otelcollector-round-trip")
}

func waitForCollectorLog(t *testing.T, container testcontainers.Container, want string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
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
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("collector logs did not contain %q; logs:\n%s", want, lastLogs)
}

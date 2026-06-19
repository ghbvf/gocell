//go:build archtest

// INVARIANT: LOCAL-RMQ-HEALTHCHECK-TCP-PROBE-01
//
// deploy/docker-compose.local.yml MUST gate RabbitMQ readiness with a lightweight
// local TCP probe against AMQP port 5672, not rabbitmq-diagnostics. The local
// stack only needs to know that the listener is accepting connections before
// corebundle starts; starting a full diagnostics Erlang VM every few seconds can
// accumulate helper processes during long Docker Desktop sessions (#2437).
//
// AI-robust grade: Medium — content-scan over the authoritative local compose
// file. Hard is not available because Docker Compose cannot express "this
// specific service healthcheck command must be this token sequence" in schema.
package archtest

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

const localRMQHealthcheckRuleID = "LOCAL-RMQ-HEALTHCHECK-TCP-PROBE-01"

var localRMQExpectedHealthcheck = []string{"CMD", "nc", "-z", "127.0.0.1", "5672"}

func localRabbitMQHealthcheckCommand(body []byte) ([]string, error) {
	var doc struct {
		Services map[string]struct {
			Healthcheck struct {
				Test []string `yaml:"test"`
			} `yaml:"healthcheck"`
		} `yaml:"services"`
	}
	if err := yaml.NewDecoder(bytes.NewReader(body)).Decode(&doc); err != nil {
		return nil, fmt.Errorf("parse compose yaml: %w", err)
	}
	rabbitmq, ok := doc.Services["rabbitmq"]
	if !ok {
		return nil, fmt.Errorf("rabbitmq service missing")
	}
	if len(rabbitmq.Healthcheck.Test) == 0 {
		return nil, fmt.Errorf("rabbitmq healthcheck.test missing")
	}
	return rabbitmq.Healthcheck.Test, nil
}

func TestLocalRabbitMQHealthcheckUsesTcpProbe(t *testing.T) {
	root := findModuleRoot(t)
	path := filepath.Join(root, "deploy", "docker-compose.local.yml")
	body, err := os.ReadFile(path) //nolint:gosec // fixed repo path derived from findModuleRoot
	require.NoError(t, err, "%s: read local compose", localRMQHealthcheckRuleID)

	got, err := localRabbitMQHealthcheckCommand(body)
	require.NoError(t, err, "%s: extract rabbitmq healthcheck", localRMQHealthcheckRuleID)
	require.Equal(t, localRMQExpectedHealthcheck, got,
		"%s: local RabbitMQ healthcheck must use a lightweight TCP probe "+
			"and must not invoke rabbitmq-diagnostics",
		localRMQHealthcheckRuleID)
}

func TestLocalRabbitMQHealthcheckCommand(t *testing.T) {
	t.Run("extracts-command", func(t *testing.T) {
		got, err := localRabbitMQHealthcheckCommand([]byte(`
services:
  rabbitmq:
    image: rabbitmq:3-alpine
    healthcheck:
      test: ["CMD", "nc", "-z", "127.0.0.1", "5672"]
`))
		require.NoError(t, err)
		require.Equal(t, localRMQExpectedHealthcheck, got)
	})

	t.Run("rejects-missing-service", func(t *testing.T) {
		_, err := localRabbitMQHealthcheckCommand([]byte(`
services:
  redis:
    image: redis:7-alpine
`))
		require.ErrorContains(t, err, "rabbitmq service missing")
	})

	t.Run("rejects-missing-healthcheck-test", func(t *testing.T) {
		_, err := localRabbitMQHealthcheckCommand([]byte(`
services:
  rabbitmq:
    image: rabbitmq:3-alpine
    healthcheck:
      interval: 3s
`))
		require.ErrorContains(t, err, "rabbitmq healthcheck.test missing")
	})
}

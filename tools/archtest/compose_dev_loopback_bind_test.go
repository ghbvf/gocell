//go:build archtest

// INVARIANT: COMPOSE-DEV-LOOPBACK-BIND-01
//
// deploy/docker-compose.yml is the local-dev quick-start stack (run via
// `make up`, #1781), and it ships FIXED, committed dev credentials (POSTGRES_PASSWORD:
// gocell_dev, RABBITMQ_DEFAULT_PASS: gocell_dev, MINIO_ROOT_PASSWORD:
// gocell_dev_secret). Because those credentials are known to anyone with the
// repo, every published host port MUST bind to 127.0.0.1 (loopback) rather than
// the docker default 0.0.0.0 — otherwise a developer workstation exposes
// Postgres / Redis / RabbitMQ / MinIO (with known credentials) to its LAN or any
// host interface. This guard makes the dev-only boundary enforced by the
// port-publish semantics, not just declared in the file-header comment (#2250
// F1: "dev-only 语义停留在注释层，没有被 compose 端口发布语义强制").
//
// AI-robust grade: Medium — content-scan (machine-checkable at CI time; an edit
// that drops the 127.0.0.1 prefix is a reviewer-visible diff that reds this
// test). Hard is unreachable: docker-compose YAML has no type-system way to pin a
// host IP onto a port string, and Go has no compile-time equivalent.
//
// Scope: ONLY deploy/docker-compose.yml. deploy/docker-compose.local.yml uses
// ${VAR:?} (caller-supplied, non-committed) credentials plus a shared netns, and
// tests/e2e/docker-compose.e2e.yaml uses network_mode: host with no published
// ports — both have a different risk model and are intentionally out of scope of
// the fixed-credential loopback invariant.
//
// Blind spots (disclosed):
//   - Only short-syntax ports ("[IP:]HOST:CONTAINER[/proto]") are parsed;
//     long-syntax (- target:/published:/host_ip:) and IPv6 host IPs are not. The
//     file uses IPv4 short syntax; a future switch must extend hostIsLoopback.
//   - Asserts only the literal 127.0.0.1 host-IP prefix, not runtime reachability.
package archtest

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// composeLoopbackRuleID is the rule identifier for COMPOSE-DEV-LOOPBACK-BIND-01.
const composeLoopbackRuleID = "COMPOSE-DEV-LOOPBACK-BIND-01"

// loopbackHostIP is the only host IP a fixed-credential dev service may publish on.
const loopbackHostIP = "127.0.0.1"

// composeNonLoopbackPorts parses body as a Docker Compose YAML and returns one
// violation string per short-syntax published port whose host side is not bound
// to 127.0.0.1, plus the total number of published ports inspected (for the
// caller's anti-vacuity assertion).
//
// Pure helper (no testing side-effects) so the RED/GREEN fixtures below can drive
// it directly without touching the file system.
func composeNonLoopbackPorts(body []byte) (violations []string, checked int) {
	var doc struct {
		Services map[string]struct {
			Ports []string `yaml:"ports"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(body, &doc); err != nil {
		return []string{fmt.Sprintf("parse error: %v", err)}, 0
	}
	for name, svc := range doc.Services {
		for _, p := range svc.Ports {
			checked++
			if !hostIsLoopback(p) {
				violations = append(violations,
					fmt.Sprintf("service %q publishes port %q without a %s host IP "+
						"(binds 0.0.0.0; fixed-credential dev infra must bind loopback)",
						name, p, loopbackHostIP))
			}
		}
	}
	return violations, checked
}

// hostIsLoopback reports whether a docker-compose short-syntax port string binds
// its host port to 127.0.0.1. Short syntax is "[HOST_IP:]HOST:CONTAINER[/proto]";
// only the three-part "IP:HOST:CONTAINER" form carries an explicit host IP, so a
// bare "HOST:CONTAINER" (binds every interface) or "CONTAINER" (random host port
// on every interface) is not loopback-bound.
func hostIsLoopback(portSpec string) bool {
	spec := portSpec
	if i := strings.IndexByte(spec, '/'); i >= 0 { // strip /tcp|/udp protocol suffix
		spec = spec[:i]
	}
	parts := strings.Split(spec, ":")
	if len(parts) != 3 {
		return false
	}
	return parts[0] == loopbackHostIP
}

// TestComposeDevLoopbackBind enforces COMPOSE-DEV-LOOPBACK-BIND-01 against
// deploy/docker-compose.yml.
//
// Anti-vacuity: asserts at least 4 published ports were inspected. The file
// publishes 6 (postgres, redis, rabbitmq×2, minio×2); a count below 4 means the
// parse or file path broke and the guard would otherwise be vacuously green.
func TestComposeDevLoopbackBind(t *testing.T) {
	root := findModuleRoot(t)
	path := filepath.Clean(filepath.Join(root, "deploy", "docker-compose.yml"))
	body, err := os.ReadFile(path)
	require.NoError(t, err, "%s: deploy/docker-compose.yml must exist", composeLoopbackRuleID)

	violations, checked := composeNonLoopbackPorts(body)
	for _, v := range violations {
		t.Errorf("%s: deploy/docker-compose.yml: %s", composeLoopbackRuleID, v)
	}
	require.GreaterOrEqualf(t, checked, 4,
		"%s: inspected only %d published ports in deploy/docker-compose.yml — expected ≥4; "+
			"parse or path may be broken", composeLoopbackRuleID, checked)
}

// TestComposeDevLoopbackBind_RejectsNonLoopbackPort is the anti-vacuity RED case:
// a bare HOST:CONTAINER publish (and a container-only publish) must be flagged.
func TestComposeDevLoopbackBind_RejectsNonLoopbackPort(t *testing.T) {
	cases := map[string]string{
		"bare-host-container": `
services:
  postgres:
    image: postgres:15-alpine
    ports:
      - "5432:5432"
`,
		"container-only": `
services:
  redis:
    image: redis:7-alpine
    ports:
      - "6379"
`,
		"wrong-host-ip": `
services:
  minio:
    image: minio/minio
    ports:
      - "0.0.0.0:9000:9000"
`,
		"mixed-one-bad": `
services:
  postgres:
    ports:
      - "127.0.0.1:5432:5432"
  redis:
    ports:
      - "6379:6379"
`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			violations, _ := composeNonLoopbackPorts([]byte(body))
			require.NotEmptyf(t, violations, "expected a violation for case %q", name)
		})
	}
}

// TestComposeDevLoopbackBind_AcceptsLoopbackPorts is the GREEN case: every port
// bound to 127.0.0.1 (including a /tcp protocol suffix) passes with zero
// violations, and a service without any ports is skipped.
func TestComposeDevLoopbackBind_AcceptsLoopbackPorts(t *testing.T) {
	body := `
services:
  postgres:
    ports:
      - "127.0.0.1:5432:5432"
  rabbitmq:
    ports:
      - "127.0.0.1:5672:5672"
      - "127.0.0.1:15672:15672/tcp"
  migrate:
    image: migrate
`
	violations, checked := composeNonLoopbackPorts([]byte(body))
	require.Empty(t, violations, "expected no violations: %v", violations)
	require.Equal(t, 3, checked, "expected 3 published ports inspected")
}

//go:build archtest

// INVARIANT: COMPOSE-HEALTHCHECK-START-PERIOD-01
//
// Every service in every docker-compose* file that declares a healthcheck block
// MUST also declare start_period. Without start_period Docker begins the failure
// countdown the instant the container starts, so an app that takes even a few
// seconds to bind causes spurious failed-health marks that propagate to
// depends_on: service_healthy gates and abort the compose stack.
//
// AI-robust grade: Medium — content-scan (machine-checkable at CI time; adding a
// healthcheck without start_period is a reviewer-visible diff that fails this
// test). Hard is unreachable: a Docker Compose YAML schema cannot enforce field
// co-presence at the type-system level, and Go has no compile-time equivalent for
// the docker-compose key constraint.
//
// Scan scope: every file whose base name starts with "docker-compose" (suffix
// .yml or .yaml) found under the repo root (ModuleScope). Other YAML files
// (cell.yaml, contract.yaml, workflow files, …) are skipped via the base-name
// prefix filter inside [composeStartPeriodViolations].
//
// Blind spots (disclosed):
//   - Does NOT verify that the start_period value is numerically reasonable
//     (e.g. "1ns" is accepted). Value correctness is a human/review concern.
//   - Only scans YAML files reachable by ModuleScope (honors the default
//     excludes: vendor/, testdata/, .git/, node_modules/). Files placed in those
//     directories are not checked.
//   - Services whose healthcheck block contains "disable: true" are intentionally
//     skipped: Docker Compose treats this as an explicit opt-out of the healthcheck
//     mechanism, so requiring start_period there would be meaningless.
//     This carve-out is tested by the inline GREEN fixture sub-case
//     "disabled-healthcheck-skipped" in TestComposeHealthcheckStartPeriod_AcceptsCompliantCompose.
package archtest

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// composeHealthcheckRuleID is the rule identifier for COMPOSE-HEALTHCHECK-START-PERIOD-01.
const composeHealthcheckRuleID = "COMPOSE-HEALTHCHECK-START-PERIOD-01"

// composeFilePrefix is the base-name prefix that identifies Docker Compose files.
// All docker-compose* files (docker-compose.yml, docker-compose.local.yml,
// docker-compose.e2e.yaml, etc.) share this prefix.
const composeFilePrefix = "docker-compose"

// composeStartPeriodViolations parses body as a Docker Compose YAML and returns
// one violation string per service whose healthcheck block lacks start_period.
//
// Returns nil (no violations) when:
//   - the file name does not start with composeFilePrefix (skip non-compose files)
//   - the file has no services section
//   - all services with a healthcheck already declare start_period
//
// The function is a pure helper (no t.Testing side-effects) so it can be driven
// directly by the RED/GREEN inline fixtures below without touching the real file
// system.
func composeStartPeriodViolations(path string, body []byte) []string {
	if !strings.HasPrefix(filepath.Base(path), composeFilePrefix) {
		return nil
	}

	var doc struct {
		Services map[string]struct {
			Healthcheck map[string]yaml.Node `yaml:"healthcheck"`
		} `yaml:"services"`
	}
	if err := yaml.NewDecoder(bytes.NewReader(body)).Decode(&doc); err != nil {
		return []string{fmt.Sprintf("parse error: %v", err)}
	}

	var violations []string
	for name, svc := range doc.Services {
		hc := svc.Healthcheck
		if len(hc) == 0 {
			continue
		}
		// Docker Compose healthcheck: {disable: true} is a valid explicit opt-out.
		// Requiring start_period on a disabled healthcheck is meaningless, so skip.
		if node, ok := hc["disable"]; ok && node.Value == "true" {
			continue
		}
		if _, ok := hc["start_period"]; !ok {
			violations = append(violations,
				fmt.Sprintf("service %q has healthcheck but is missing start_period", name))
		}
	}
	return violations
}

// TestComposeHealthcheckStartPeriod enforces COMPOSE-HEALTHCHECK-START-PERIOD-01
// across every docker-compose* YAML file in the repository.
//
// Anti-vacuity: asserts that at least 10 healthcheck blocks were observed. If the
// glob logic or file detection stops working (e.g. every compose file is moved to
// an excluded directory) the count drops below the threshold and the test fails,
// preventing a silent false-green.
func TestComposeHealthcheckStartPeriod(t *testing.T) {
	root := findModuleRoot(t)
	scope := ModuleScope(root)

	totalHealthchecks := 0
	EachContentFile(t, scope, []string{".yml", ".yaml"}, func(ft *testing.T, fc ContentContext) {
		if !strings.HasPrefix(filepath.Base(fc.Rel), composeFilePrefix) {
			return
		}

		// Count healthcheck blocks in this file for anti-vacuity.
		var doc struct {
			Services map[string]struct {
				Healthcheck map[string]yaml.Node `yaml:"healthcheck"`
			} `yaml:"services"`
		}
		if err := yaml.NewDecoder(bytes.NewReader(fc.Bytes)).Decode(&doc); err != nil {
			ft.Errorf("%s: parse %s: %v", composeHealthcheckRuleID, fc.Rel, err)
			return
		}
		for _, svc := range doc.Services {
			if len(svc.Healthcheck) > 0 {
				totalHealthchecks++
			}
		}

		violations := composeStartPeriodViolations(fc.Rel, fc.Bytes)
		for _, v := range violations {
			ft.Errorf("%s: %s: %s", composeHealthcheckRuleID, fc.Rel, v)
		}
	})

	// Anti-vacuity: current repo has ~20 healthcheck blocks across all compose
	// files. A count below 10 indicates the scope or base-name filter has broken.
	require.GreaterOrEqualf(t, totalHealthchecks, 10,
		"%s: observed only %d healthcheck blocks "+
			"across all docker-compose* files — expected ≥10; scope or file detection "+
			"may be broken (compose files moved to excluded dirs?)",
		composeHealthcheckRuleID, totalHealthchecks)
}

// --- synthetic fixtures: RED (must detect violations) -------------------------

// TestComposeHealthcheckStartPeriod_RejectsServiceWithoutStartPeriod verifies
// that composeStartPeriodViolations reports a violation for a service whose
// healthcheck block omits start_period.
func TestComposeHealthcheckStartPeriod_RejectsServiceWithoutStartPeriod(t *testing.T) {
	cases := map[string]string{
		"postgres-missing": `
services:
  postgres:
    image: postgres:16-alpine
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U gocell"]
      interval: 5s
      timeout: 3s
      retries: 5
`,
		"redis-missing": `
services:
  redis:
    image: redis:7-alpine
    healthcheck:
      test: ["CMD", "redis-cli", "ping"]
      interval: 5s
      timeout: 3s
      retries: 5
`,
		"mixed-one-missing": `
services:
  postgres:
    image: postgres:16-alpine
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U gocell"]
      interval: 5s
      timeout: 3s
      retries: 5
      start_period: 10s
  redis:
    image: redis:7-alpine
    healthcheck:
      test: ["CMD", "redis-cli", "ping"]
      interval: 5s
      timeout: 3s
      retries: 5
`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			violations := composeStartPeriodViolations("docker-compose.yml", []byte(body))
			require.NotEmpty(t, violations,
				"expected violations for case %q but got none", name)
		})
	}
}

// --- synthetic fixtures: GREEN (must pass with zero violations) ---------------

// TestComposeHealthcheckStartPeriod_AcceptsCompliantCompose verifies that
// composeStartPeriodViolations returns no violations when every healthcheck
// block includes start_period, and that non-compose YAML files are skipped.
func TestComposeHealthcheckStartPeriod_AcceptsCompliantCompose(t *testing.T) {
	cases := map[string]struct {
		path string
		body string
	}{
		"all-have-start-period": {
			path: "docker-compose.yml",
			body: `
services:
  postgres:
    image: postgres:16-alpine
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U gocell"]
      interval: 5s
      timeout: 3s
      retries: 5
      start_period: 10s
  redis:
    image: redis:7-alpine
    healthcheck:
      test: ["CMD", "redis-cli", "ping"]
      interval: 5s
      timeout: 3s
      retries: 5
      start_period: 10s
  rabbitmq:
    image: rabbitmq:3-management-alpine
    healthcheck:
      test: ["CMD", "rabbitmq-diagnostics", "-q", "ping"]
      interval: 10s
      timeout: 5s
      retries: 5
      start_period: 15s
`,
		},
		"no-healthcheck-service": {
			path: "docker-compose.yml",
			body: `
services:
  migrate:
    image: migrate
`,
		},
		"non-compose-yaml-skipped": {
			path: "cell.yaml",
			body: `
services:
  postgres:
    healthcheck:
      test: ping
      interval: 5s
`,
		},
		"workflow-yaml-skipped": {
			path: ".github/workflows/build.yml",
			body: `
services:
  postgres:
    healthcheck:
      test: ping
`,
		},
		"disabled-healthcheck-skipped": {
			path: "docker-compose.yml",
			body: `
services:
  legacy:
    image: legacy:latest
    healthcheck:
      disable: true
`,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			violations := composeStartPeriodViolations(tc.path, []byte(tc.body))
			require.Empty(t, violations,
				"expected no violations for case %q but got: %v", name, violations)
		})
	}
}

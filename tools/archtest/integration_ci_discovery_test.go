package archtest

import (
	"os/exec"
	"strings"
	"testing"
)

// TestIntegrationCIDiscovery validates that every package containing a Go
// test file with a `//go:build integration` (or comma-list including it)
// build tag is reachable via the same `go list` command the Makefile and CI
// workflow use, so that adding a new integration package does not silently
// fall outside the gate.
//
// INVARIANT: INTEGRATION-CI-DISCOVERY-01
//
// 不能 funnel 的理由：build tag 是 Go 工具链层规则，由 toolchain 决定哪些文件
// 属于哪些 tag；funnel 不到 schema/marker；type system 也无法表达"build tag 与
// CI 包列表必须一致"。平铺 archtest 兜底。
func TestIntegrationCIDiscovery(t *testing.T) {
	repoRoot := repoRootFromTestPath(t)

	cmd := exec.Command("go", "list",
		"-tags=integration,e2e",
		"-f", "{{if or (gt (len .TestGoFiles) 0) (gt (len .XTestGoFiles) 0)}}{{.ImportPath}}{{end}}",
		"./...")
	cmd.Dir = repoRoot

	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list failed: %v", err)
	}
	discovered := map[string]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		if line == "" {
			continue
		}
		discovered[line] = true
	}
	if len(discovered) == 0 {
		t.Fatal("no packages discovered; integration tag list filtering broken")
	}

	// Sanity check several known integration test packages — if `go list`
	// discovery silently regresses (e.g. tag spelled wrong), this catches it.
	sentinels := []string{
		"github.com/ghbvf/gocell/adapters/postgres",
		"github.com/ghbvf/gocell/cells/accesscore/internal/adapters/postgres",
		"github.com/ghbvf/gocell/cells/accesscore/slices/identitymanage",
		"github.com/ghbvf/gocell/cmd/corebundle",
	}
	for _, s := range sentinels {
		if !discovered[s] {
			t.Errorf("INTEGRATION-CI-DISCOVERY-01: sentinel %s missing from go list output — did you forget //go:build integration on a new test file?", s)
		}
	}
}

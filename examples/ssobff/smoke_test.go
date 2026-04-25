//go:build examples_smoke

// Package main_test holds the ssobff startup smoke regression guard.
//
// Build tag `examples_smoke` keeps this isolated from:
//   - the `integration` tag, whose tests assume DB/RMQ testcontainers and
//     run inside the slow integration-test job (~10 min) — adding subprocess
//     work there would extend the PR critical path.
//   - the default no-tag run, so a developer's `go test ./...` does not
//     accidentally spawn a child binary.
//
// CI invokes this via the dedicated `examples-smoke` job in
// .github/workflows/_build-lint.yml, which runs in parallel with build-test
// and integration-test (no `needs:` dependency).
//
// ref: kubernetes/test/e2e_node — subprocess-driven node smoke pattern.
package main_test

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// smokeEnvAllowlist is the set of host environment variables we forward to
// the ssobff subprocess. Using an allowlist (instead of os.Environ() pass-
// through) keeps CI secrets — GITHUB_TOKEN, SONAR_TOKEN, anything in the
// runner job env — from crossing into the child binary. The kept variables
// cover Go toolchain plumbing and PATH/HOME-style basics needed by `go run`
// or any future helper command we shell out to.
var smokeEnvAllowlist = map[string]struct{}{
	"PATH":           {},
	"HOME":           {},
	"USER":           {},
	"TMPDIR":         {},
	"GOPATH":         {},
	"GOCACHE":        {},
	"GOMODCACHE":     {},
	"GOTMPDIR":       {},
	"GOROOT":         {},
	"LANG":           {},
	"LC_ALL":         {},
	"SYSTEMROOT":     {},
	"COMSPEC":        {},
	"WINDIR":         {},
	"PROGRAMFILES":   {},
	"PROGRAMFILESX86": {},
}

// TestSSOBFFStartupSmoke boots ./examples/ssobff as a subprocess, waits up
// to 5 s for /readyz on the dedicated health listener (127.0.0.1:9091),
// then issues SIGTERM and verifies the process exits cleanly within 5 s.
//
// Why subprocess and not in-process: walkthrough_test.go already drives the
// cell chain via httptest.NewServer, but main.go's bootstrap path
// (option wiring, lifecycle hook discovery, dual-listener bind order,
// /readyz aggregation) is only exercised when the binary actually runs.
// This test is the regression guard for the demo composition root.
func TestSSOBFFStartupSmoke(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("smoke test relies on POSIX signals; ssobff has no Windows production target")
	}

	tmp := t.TempDir()
	binPath := filepath.Join(tmp, "ssobff-smoke")
	stateDir := filepath.Join(tmp, "state")

	moduleDir, err := filepath.Abs(".")
	if err != nil {
		t.Fatalf("resolve module dir: %v", err)
	}

	build := exec.Command("go", "build", "-o", binPath, ".")
	build.Dir = moduleDir
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build ./examples/ssobff failed: %v\n%s", err, out)
	}

	// High ports avoid collisions with developer dev servers / docker
	// proxies that frequently bind the canonical 8081/9081/9091 demo
	// ports. CI runners are clean so any port works there; the values
	// mainly matter for `go test -tags=examples_smoke` on a developer
	// machine.
	const (
		smokePrimaryAddr  = "127.0.0.1:28081"
		smokeInternalAddr = "127.0.0.1:29081"
		smokeHealthAddr   = "127.0.0.1:29091"
	)

	logs := newSyncBuffer()
	cmd := exec.Command(binPath)
	cmd.Env = append(filteredEnv(),
		"GOCELL_STATE_DIR="+stateDir,
		"GOCELL_SSOBFF_PRIMARY_ADDR="+smokePrimaryAddr,
		"GOCELL_SSOBFF_INTERNAL_ADDR="+smokeInternalAddr,
		"GOCELL_SSOBFF_HEALTH_ADDR="+smokeHealthAddr,
		// PR-CFG-F made the internal listener service-token mandatory; the
		// process fails fast when this is missing or shorter than 32 bytes.
		// A literal 32-byte filler keeps the smoke test self-contained.
		"GOCELL_SSOBFF_SERVICE_SECRET=ssobff-smoke-service-secret-32b!!",
	)
	cmd.Stdout = logs
	cmd.Stderr = logs

	if err := cmd.Start(); err != nil {
		t.Fatalf("start ssobff: %v", err)
	}
	// Single-Wait discipline: the goroutine below is the sole caller of
	// cmd.Wait (`exec.Cmd.Wait` documents "Callers may call Wait at most
	// once"). `done` is closed (not send) so multiple receivers — the
	// happy-path select and the cleanup hook — can both observe
	// completion without competing for a single value.
	var waitErr error
	done := make(chan struct{})
	go func() {
		waitErr = cmd.Wait()
		close(done)
	}()
	t.Cleanup(func() {
		if cmd.Process != nil {
			// Kill is the cross-platform "force terminate" entry point:
			// SIGKILL on POSIX, TerminateProcess on Windows. It is a
			// no-op (ErrProcessDone) once the process has already exited
			// via the SIGTERM path, so the call is safe in both happy
			// and failure paths.
			_ = cmd.Process.Kill()
		}
		<-done // wait for cmd.Wait to release stdout/stderr pipes before TempDir teardown
	})

	readyCtx, cancelReady := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelReady()
	if !pollReadyz(readyCtx, "http://"+smokeHealthAddr+"/readyz") {
		t.Fatalf("ssobff /readyz did not return 200 within 5s\nlogs:\n%s", logs.String())
	}

	if err := sigtermProcess(cmd); err != nil {
		t.Fatalf("send SIGTERM: %v", err)
	}

	select {
	case <-done:
		if waitErr != nil {
			t.Fatalf("ssobff exited non-zero on SIGTERM: %v\nlogs:\n%s", waitErr, logs.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("ssobff did not exit within 5s of SIGTERM\nlogs:\n%s", logs.String())
	}
}

// filteredEnv returns os.Environ() trimmed to the keys in smokeEnvAllowlist.
// See smokeEnvAllowlist's doc comment for the rationale.
func filteredEnv() []string {
	src := os.Environ()
	out := make([]string, 0, len(smokeEnvAllowlist))
	for _, kv := range src {
		k, _, found := strings.Cut(kv, "=")
		if !found {
			continue
		}
		if _, ok := smokeEnvAllowlist[strings.ToUpper(k)]; ok {
			out = append(out, kv)
		}
	}
	return out
}

// sigtermProcess sends SIGTERM to cmd. The outer test skips on Windows
// (where SIGTERM has no graceful-shutdown semantics), so this lives in
// the smoke_test.go body with no GOOS guard.
func sigtermProcess(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Signal(syscall.SIGTERM)
}

// pollReadyz hits url every 100 ms until a 200 is observed or ctx expires.
func pollReadyz(ctx context.Context, url string) bool {
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	client := &http.Client{Timeout: 500 * time.Millisecond}
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			// URL is a const literal so this is unreachable in practice,
			// but `_ = err` violates the project's error-handling rule
			// and would mask a future regression that swaps the URL
			// for an env-derived value.
			return false
		}
		if resp, err := client.Do(req); err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return true
			}
		}
		select {
		case <-ctx.Done():
			return false
		case <-tick.C:
		}
	}
}

// syncBuffer is a goroutine-safe io.Writer used to capture child stdout/stderr.
// bytes.Buffer is not concurrency-safe, and exec.Cmd writes from the wait
// goroutine while the test reads from the main goroutine.
type syncBuffer struct {
	mu  sync.Mutex
	buf []byte
}

func newSyncBuffer() *syncBuffer { return &syncBuffer{} }

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.buf = append(s.buf, p...)
	return len(p), nil
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return string(s.buf)
}

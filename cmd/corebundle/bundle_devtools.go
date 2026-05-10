package main

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/ghbvf/gocell/cmd/internal/wiresummary"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/governance"
	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/runtime/bootstrap"
)

// defaultDevtoolsParseTimeout is the max time allowed for project metadata
// parsing during bootstrap. Exceeding this disables the catalog endpoint
// (best-effort degradation) rather than blocking server startup.
const defaultDevtoolsParseTimeout = 30 * time.Second

// devtoolsOption builds the WithDevtoolsCatalog bootstrap option for the catalog
// endpoint. Best-effort metadata parse: logs at Warn (degraded operation per
// observability.md) and disables the endpoint when GOCELL_PROJECT_ROOT is unset,
// resolves outside the working tree, or doesn't expose a valid project tree.
// The endpoint is absent when pm is nil — Bootstrap treats nil pm as "disabled".
//
// generatedPackageGraph is the build-time generated package dep graph from
// catalog_gen.go (produced by `go generate ./cmd/corebundle/`). When nil (e.g.
// go generate has not been run), the packageDeps block is simply omitted.
func devtoolsOption(shared *SharedDeps) bootstrap.Option {
	root := shared.ProjectRoot
	if root == "" {
		slog.Warn("devtools: GOCELL_PROJECT_ROOT unset; catalog endpoint disabled")
		return bootstrap.WithDevtoolsCatalog(nil, "", nil)
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		slog.Warn("devtools: GOCELL_PROJECT_ROOT path resolution failed; catalog endpoint disabled",
			slog.String("root", root),
			slog.Any("error", err))
		return bootstrap.WithDevtoolsCatalog(nil, "", nil)
	}
	cwd, err := os.Getwd()
	if err != nil {
		slog.Warn("devtools: cwd lookup failed; catalog endpoint disabled",
			slog.Any("error", err))
		return bootstrap.WithDevtoolsCatalog(nil, "", nil)
	}
	if !governance.IsWithinRoot(cwd, absRoot) {
		slog.Warn("devtools: GOCELL_PROJECT_ROOT escapes working tree; catalog endpoint disabled",
			slog.String("root", root),
			slog.String("absRoot", absRoot),
			slog.String("cwd", cwd))
		return bootstrap.WithDevtoolsCatalog(nil, "", nil)
	}
	pm, err := parseProjectWithTimeout(absRoot, defaultDevtoolsParseTimeout, shared.Clock)
	if err != nil {
		return bootstrap.WithDevtoolsCatalog(nil, "", nil)
	}

	// Derive wire summaries from cell.go marker comments. Best-effort: a scan
	// error (e.g. malformed marker) disables wireSummary but does not block the
	// catalog endpoint. See docs/architecture/202605051500-adr-k05-markergen-cellgen-unified.md
	// Decision 6.
	wireSummaries, wsErr := wiresummary.BuildCellWireSummaries(absRoot, pm)
	if wsErr != nil {
		slog.Warn("devtools: wire summary scan failed; wireSummary omitted from catalog",
			slog.String("root", absRoot),
			slog.Any("error", wsErr))
		wireSummaries = nil
	}

	slog.Info("devtools: catalog endpoint enabled", slog.String("root", absRoot))
	return bootstrap.WithDevtoolsCatalog(pm, absRoot, generatedPackageGraph, wireSummaries)
}

// parseProjectWithTimeout runs metadata.NewParser(absRoot).Parse() in a
// goroutine and returns within timeout. On parse error or timeout the catalog
// endpoint is disabled (best-effort degradation); the caller receives nil and
// the error/timeout is already logged here. The clock is injected for testability
// and to satisfy PROD-CLOCK-INJECTION-01 (no time.After in production).
func parseProjectWithTimeout(absRoot string, timeout time.Duration, clk clock.Clock) (*metadata.ProjectMeta, error) {
	type parseResult struct {
		pm  *metadata.ProjectMeta
		err error
	}
	done := make(chan parseResult, 1)
	go func() {
		pm, err := metadata.NewParser(absRoot).Parse()
		done <- parseResult{pm, err}
	}()
	timer := clk.NewTimerAt(clk.Now().Add(timeout))
	defer timer.Stop()
	select {
	case r := <-done:
		if r.err != nil {
			slog.Warn("devtools: project metadata parse failed; catalog endpoint disabled",
				slog.String("root", absRoot),
				slog.Any("error", r.err))
			return nil, r.err
		}
		return r.pm, nil
	case <-timer.C():
		slog.Warn("devtools: project metadata parse timeout; catalog endpoint disabled",
			slog.String("root", absRoot),
			slog.Duration("timeout", timeout))
		return nil, fmt.Errorf("devtools: parse timeout after %s", timeout)
	}
}

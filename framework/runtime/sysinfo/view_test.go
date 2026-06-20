package sysinfo

import (
	"context"
	"testing"
	"time"

	"github.com/ghbvf/gocell/framework/kernel/clock/clockmock"
)

const systemViewReportElapsed = 90 * time.Second

func TestSystemView_Report(t *testing.T) {
	start := time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC)
	clk := clockmock.New(start.Add(systemViewReportElapsed))
	lastDeployedAt := "2026-06-19T09:00:00Z"
	view := New(clk, Config{
		Build: BuildInfo{
			Version: "v0.3.1", Commit: "8c3a4b9abcdef", CommitDate: "2026-06-18T00:00:00Z",
			BuildDate: "2026-06-19T00:00:00Z", GoVersion: "go1.25.0", Dirty: true,
		},
		Assembly:    AssemblyInfo{Name: "corebundle", Cells: []string{"accesscore", "syscore"}},
		Environment: EnvironmentInfo{Env: "production", Containerized: true},
		Deployment:  DeploymentInfo{Available: true, LastDeployedAt: &lastDeployedAt, Source: "env"},
		StartTime:   start,
	})

	rep := view.Report(context.Background())
	if rep.Build.Commit != "8c3a4b9abcde" {
		t.Fatalf("commit = %q, want 12-char short hash", rep.Build.Commit)
	}
	if rep.Runtime.UptimeSeconds != 90 {
		t.Fatalf("uptime = %d, want 90", rep.Runtime.UptimeSeconds)
	}
	if rep.Runtime.Goroutines <= 0 {
		t.Fatalf("goroutines = %d, want > 0", rep.Runtime.Goroutines)
	}
	if rep.Runtime.MemoryAllocBytes <= 0 {
		t.Fatalf("memory not populated: %+v", rep.Runtime)
	}
	if rep.Environment.Env != EnvProd {
		t.Fatalf("env = %q, want prod", rep.Environment.Env)
	}
	if rep.Deployment.LastDeployedAt == nil || *rep.Deployment.LastDeployedAt != lastDeployedAt {
		t.Fatalf("deployment = %+v", rep.Deployment)
	}
}

func TestSystemView_ClonesCells(t *testing.T) {
	clk := clockmock.New(time.Unix(0, 0))
	cells := []string{"accesscore"}
	view := New(clk, Config{Assembly: AssemblyInfo{Name: "corebundle", Cells: cells}})
	cells[0] = "mutated"

	rep := view.Report(context.Background())
	if rep.Assembly.Cells[0] != "accesscore" {
		t.Fatalf("stored cells were mutated: %+v", rep.Assembly.Cells)
	}
	rep.Assembly.Cells[0] = "mutated-again"
	if got := view.Report(context.Background()).Assembly.Cells[0]; got != "accesscore" {
		t.Fatalf("reported cells share backing array: %q", got)
	}
}

func TestSystemView_ContextRoundTrip(t *testing.T) {
	view := New(clockmock.New(time.Unix(0, 0)), Config{})
	ctx := WithSystemView(context.Background(), view)
	got, ok := SystemViewFromContext(ctx)
	if !ok || got == nil {
		t.Fatal("SystemViewFromContext did not retrieve view")
	}
	if _, ok := SystemViewFromContext(context.Background()); ok {
		t.Fatal("empty context unexpectedly had SystemView")
	}
}

func TestNormalizeEnv(t *testing.T) {
	tests := map[string]string{
		"":           EnvUnknown,
		"dev":        EnvDev,
		"staging":    EnvStaging,
		"prod":       EnvProd,
		"production": EnvProd,
		"qa":         EnvUnknown,
	}
	for in, want := range tests {
		if got := NormalizeEnv(in); got != want {
			t.Fatalf("NormalizeEnv(%q) = %q, want %q", in, got, want)
		}
	}
}

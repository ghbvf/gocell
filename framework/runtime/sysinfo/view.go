// Package sysinfo projects process-local build/runtime metadata into the
// operator-facing http.admin.system.v1 contract served by syscore.
package sysinfo

import (
	"context"
	"math"
	"runtime"
	"runtime/debug"
	"strings"
	"time"

	"github.com/ghbvf/gocell/framework/kernel/clock"
)

const (
	EnvDev     = "dev"
	EnvStaging = "staging"
	EnvProd    = "prod"
	EnvUnknown = "unknown"
)

// BuildInfo is the safe build metadata subset exposed on the admin system wire.
type BuildInfo struct {
	Version    string
	Commit     string
	CommitDate string
	BuildDate  string
	GoVersion  string
	Dirty      bool
}

// RuntimeInfo is a point-in-time process runtime snapshot.
type RuntimeInfo struct {
	UptimeSeconds    int64
	Goroutines       int64
	MemoryMB         float64
	MemoryAllocBytes int64
	GCPauseTotalNs   int64
	CPUPercent       float64
}

// AssemblyInfo is the non-sensitive assembly identity snapshot.
type AssemblyInfo struct {
	Name  string
	Cells []string
}

// EnvironmentInfo is the sanitized deployment environment snapshot.
type EnvironmentInfo struct {
	Env           string
	Containerized bool
}

// DeploymentInfo is optional deployment metadata. Missing deployment metadata is
// represented as Available=false rather than failing the endpoint.
type DeploymentInfo struct {
	Available      bool
	LastDeployedAt *string
	Source         string
}

// Report is the complete system metadata response model consumed by syscore.
type Report struct {
	Build       BuildInfo
	Runtime     RuntimeInfo
	Assembly    AssemblyInfo
	Environment EnvironmentInfo
	Deployment  DeploymentInfo
}

// SystemView is the read-side facade consumed from request context.
type SystemView interface {
	Report(ctx context.Context) Report
}

// Config captures startup-stable metadata for the default SystemView.
type Config struct {
	Build       BuildInfo
	Assembly    AssemblyInfo
	Environment EnvironmentInfo
	Deployment  DeploymentInfo
	StartTime   time.Time
}

type view struct {
	clk clock.Clock
	cfg Config
}

// New builds a SystemView over startup-stable metadata plus live runtime stats.
func New(clk clock.Clock, cfg Config) SystemView {
	clock.MustHaveClock(clk, "sysinfo.New")
	cfg.Build = normalizeBuildInfo(cfg.Build)
	cfg.Assembly.Cells = cloneStrings(cfg.Assembly.Cells)
	cfg.Environment.Env = NormalizeEnv(cfg.Environment.Env)
	if cfg.Deployment.Source == "" {
		cfg.Deployment.Source = "unavailable"
	}
	if cfg.StartTime.IsZero() {
		cfg.StartTime = clk.Now()
	}
	return &view{clk: clk, cfg: cfg}
}

// Report returns a fresh runtime snapshot while keeping startup metadata stable.
func (v *view) Report(context.Context) Report {
	cfg := v.cfg
	return Report{
		Build:       cfg.Build,
		Runtime:     runtimeSnapshot(v.clk, cfg.StartTime),
		Assembly:    AssemblyInfo{Name: cfg.Assembly.Name, Cells: cloneStrings(cfg.Assembly.Cells)},
		Environment: cfg.Environment,
		Deployment:  cfg.Deployment,
	}
}

// NormalizeEnv collapses deployment env input to the wire enum.
func NormalizeEnv(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case EnvDev:
		return EnvDev
	case "":
		return EnvUnknown
	case EnvStaging:
		return EnvStaging
	case "production", EnvProd:
		return EnvProd
	default:
		return EnvUnknown
	}
}

// BuildInfoFromRuntime combines optional ldflags with Go's embedded VCS build
// settings. Explicit ldflags win; debug.ReadBuildInfo supplies dev fallback.
func BuildInfoFromRuntime(version, commit, commitDate, buildDate string) BuildInfo {
	info := BuildInfo{
		Version:    strings.TrimSpace(version),
		Commit:     shortCommit(strings.TrimSpace(commit)),
		CommitDate: strings.TrimSpace(commitDate),
		BuildDate:  strings.TrimSpace(buildDate),
		GoVersion:  runtime.Version(),
	}
	if info.Version == "" {
		info.Version = "dev"
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		info = mergeBuildInfoFromDebug(info, bi)
	}
	return normalizeBuildInfo(info)
}

func mergeBuildInfoFromDebug(info BuildInfo, bi *debug.BuildInfo) BuildInfo {
	if info.Version == "dev" && validModuleVersion(bi.Main.Version) {
		info.Version = bi.Main.Version
	}
	for _, s := range bi.Settings {
		info = mergeBuildSetting(info, s)
	}
	return info
}

func mergeBuildSetting(info BuildInfo, setting debug.BuildSetting) BuildInfo {
	switch setting.Key {
	case "vcs.revision":
		if info.Commit == "" {
			info.Commit = shortCommit(setting.Value)
		}
	case "vcs.time":
		if info.CommitDate == "" {
			info.CommitDate = setting.Value
		}
	case "vcs.modified":
		info.Dirty = setting.Value == "true"
	}
	return info
}

func validModuleVersion(version string) bool {
	return version != "" && !strings.HasPrefix(version, "(devel")
}

func normalizeBuildInfo(info BuildInfo) BuildInfo {
	info.Version = strings.TrimSpace(info.Version)
	if info.Version == "" {
		info.Version = "dev"
	}
	info.Commit = shortCommit(info.Commit)
	info.CommitDate = strings.TrimSpace(info.CommitDate)
	info.BuildDate = strings.TrimSpace(info.BuildDate)
	if info.GoVersion == "" {
		info.GoVersion = runtime.Version()
	}
	return info
}

func runtimeSnapshot(clk clock.Clock, start time.Time) RuntimeInfo {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	uptime := int64(clk.Since(start).Seconds())
	if uptime < 0 {
		uptime = 0
	}
	return RuntimeInfo{
		UptimeSeconds:    uptime,
		Goroutines:       int64(runtime.NumGoroutine()),
		MemoryMB:         math.Round((float64(ms.Alloc)/1024/1024)*10) / 10,
		MemoryAllocBytes: saturatingInt64(ms.Alloc),
		GCPauseTotalNs:   saturatingInt64(ms.PauseTotalNs),
		CPUPercent:       -1,
	}
}

func saturatingInt64(v uint64) int64 {
	if v > uint64(math.MaxInt64) {
		return math.MaxInt64
	}
	return int64(v)
}

func shortCommit(v string) string {
	v = strings.TrimSpace(v)
	if len(v) > 12 {
		return v[:12]
	}
	return v
}

func cloneStrings(in []string) []string {
	out := make([]string, len(in))
	copy(out, in)
	return out
}

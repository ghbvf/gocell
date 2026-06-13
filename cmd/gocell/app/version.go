package app

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// version is the gocell release version, injected at build time by goreleaser
// via -ldflags "-X github.com/ghbvf/gocell/cmd/gocell/app.version=vX.Y.Z" (see
// .goreleaser.yaml). It is the SINGLE source for every field `gocell version`
// reports: the gocell CLI and the gocell framework module
// (github.com/ghbvf/gocell) are tagged and released atomically at the same
// version by .github/workflows/release.yml, so cli_version == framework_version
// by construction and the compatible framework range is a pure function of it
// (compatibleFrameworkRange) — there is no separately-maintained compatibility
// constant that could drift. In local / `go build` / go.work development builds
// no ldflags are set, so it stays "dev" and resolveVersion falls back to the
// VCS revision via toolVersion().
var version = "dev"

// versionInfo is the wire shape of `gocell version --format json`. The field
// set is pinned by version_test.go (TestRunVersion_JSONFormat); keys are
// camelCase per the repo JSON convention.
type versionInfo struct {
	CLIVersion               string `json:"cliVersion"`
	FrameworkVersion         string `json:"frameworkVersion"`
	CompatibleFrameworkRange string `json:"compatibleFrameworkRange"`
}

// runVersion prints the gocell CLI / framework version and the compatible
// framework range. Default format is text; --format json emits the versionInfo
// object. It reads no project metadata — purely static build information, so it
// ignores ctx like the other metadata-free subcommands.
func runVersion(_ context.Context, args []string) error {
	fs := flag.NewFlagSet("version", flag.ContinueOnError)
	format := fs.String("format", "text", "output format: text|json")
	if err := fs.Parse(args); err != nil {
		// -h returns flag.ErrHelp (Dispatch maps to ExitOK); anything else is a
		// genuine parse error surfaced to the caller.
		return err
	}

	v := resolveVersion()
	info := versionInfo{
		CLIVersion:               v,
		FrameworkVersion:         v,
		CompatibleFrameworkRange: compatibleFrameworkRange(v),
	}

	switch *format {
	case "text":
		fmt.Printf("cli_version: %s\n", info.CLIVersion)
		fmt.Printf("framework_version: %s\n", info.FrameworkVersion)
		fmt.Printf("compatible_framework_range: %s\n", info.CompatibleFrameworkRange)
		return nil
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		// compatibleFrameworkRange always carries '<' / '>'; keep them literal
		// rather than </> so the JSON is readable on a terminal (the
		// value is a version range, never HTML).
		enc.SetEscapeHTML(false)
		return enc.Encode(info)
	default:
		return fmt.Errorf("version: unknown format %q (want text|json)", *format)
	}
}

// resolveVersion returns the build-injected release version, or the VCS-derived
// dev string when none was injected (local / go.work builds).
func resolveVersion() string {
	if version != "dev" {
		return version
	}
	return toolVersion()
}

// compatibleFrameworkRange derives the semver range of framework module
// (github.com/ghbvf/gocell) versions this CLI release is compatible with, as a
// pure function of the release version — NOT a hand-maintained constant (the
// Hard single-source property; see version_test.go TestCompatibleFrameworkRange).
// The CLI is built against, and co-tagged with, exactly one framework minor
// line; pre-v1.0 minor bumps may break (SemVer §4), so the range is bounded to
// the current minor: vX.Y.Z -> ">=vX.Y.0 <vX.(Y+1).0". A non-semver / dev
// version passes through unchanged.
func compatibleFrameworkRange(v string) string {
	major, minor, ok := parseMajorMinor(v)
	if !ok {
		return v
	}
	return fmt.Sprintf(">=v%d.%d.0 <v%d.%d.0", major, minor, major, minor+1)
}

// parseMajorMinor extracts the major and minor numbers from a semver string
// like "v0.1.0" or "v0.1.0-develop.<ts>" (pre-release/build suffixes after the
// first '-' or '+' are ignored). ok is false when v is not a parseable
// vMAJOR.MINOR.* — e.g. "dev" or a bare VCS revision.
func parseMajorMinor(v string) (major, minor int, ok bool) {
	core := strings.TrimPrefix(v, "v")
	if i := strings.IndexAny(core, "-+"); i >= 0 {
		core = core[:i]
	}
	parts := strings.Split(core, ".")
	if len(parts) < 2 {
		return 0, 0, false
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, false
	}
	minor, err = strconv.Atoi(parts[1])
	if err != nil {
		return 0, 0, false
	}
	return major, minor, true
}

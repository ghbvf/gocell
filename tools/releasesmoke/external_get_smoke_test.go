//go:build releasesmoke

// INVARIANT: RELEASE-EXTERNAL-GET-01 (see doc.go for the full statement, grading,
// synthetic red case, and faithfulness gaps).

package releasesmoke_test

import (
	"archive/zip"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"

	"github.com/ghbvf/gocell/tools/gomodutil"
	"github.com/ghbvf/gocell/tools/modrelease"
)

const (
	// synthVersion is never produced by a real git tag, so it cannot be masked by
	// a stale entry in the warm module cache. Major v0 keeps it valid for the
	// suffix-less module paths (a vN>=2 would require a /vN path suffix).
	synthVersion = "v0.99.99"
	// probe is the satellite an external consumer fetches; its transitive internal
	// closure (gocell, adapterutil) is the anti-vacuity surface.
	probe       = "github.com/ghbvf/gocell/adapters/postgres"
	probeCore   = "github.com/ghbvf/gocell"
	probeAdpUtl = "github.com/ghbvf/gocell/adapters/adapterutil"
)

// TestExternalGet_Bumped_Resolves is the green path: publish every library module
// at synthVersion with its release-BUMPED go.mod to a hermetic file GOPROXY, then
// an external consumer `go get`s the probe and `go list -m all` resolves the probe
// AND its transitive internal requires to synthVersion — proving the bumped
// require versions are externally consumable even though the published go.mod
// still carries its (ignored) local replace.
func TestExternalGet_Bumped_Resolves(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping external go get smoke in -short mode (spawns go get/build, ~10-40s)")
	}
	goBin := mustGo(t)
	root := repoRoot(t)
	proxy := buildProxy(t, root, synthVersion)
	env := offlineEnv(t, proxy)

	// (1) The issue's literal command resolves against the bumped publish.
	runOK(t, goBin, newConsumer(t), env, "get", probe+"@"+synthVersion)

	// (2) A consumer that IMPORTS the probe compiles against it, deepening into
	// the probe's internal requires (gocell, adapterutil) and resolving the whole
	// internal graph to the published synthVersion — the real `go build` path.
	consumer := newConsumerRequiring(t, probe, synthVersion)
	runOK(t, goBin, consumer, env, "build", "./...")

	out := runOK(t, goBin, consumer, env, "list", "-m", "all")
	assertResolved(t, out, probe, synthVersion)
	assertResolved(t, out, probeCore, synthVersion)
	assertResolved(t, out, probeAdpUtl, synthVersion)
	// No internal (github.com/ghbvf/gocell…) module sits at v0.0.0 — the bump
	// rewrote them all (the projected proxy carries no external pseudo-versions).
	assertNoInternalV000(t, out)
	// The published go.mods carry replace, but Go must ignore a dependency's
	// replace: no internal module may resolve via a replacement (=>).
	assertNoInternalReplace(t, out)
}

// TestExternalGet_IncompleteSet_Fails is the synthetic red case (the real #1843
// failure mode): one internal sibling of the probe — the root module gocell — is
// left UNPUBLISHED, exactly as #1842 reported a satellite with "no per-module
// tag". Compiling a consumer that imports the probe must then FAIL to resolve the
// missing sibling, proving the green path's success genuinely depends on the
// COMPLETE synchronized published set rather than passing vacuously.
//
// (Once every module IS published, Go's MVS leniently upgrades any stale v0.0.0
// lower bound to the available version, so an unbumped-but-published proxy is not
// a discriminator on a single version — the bump's value is compatible
// cross-version pinning, byte-locked separately by modrelease's golden.)
func TestExternalGet_IncompleteSet_Fails(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping external go get smoke in -short mode")
	}
	goBin := mustGo(t)
	root := repoRoot(t)
	// Publish everything EXCEPT the probe's core sibling — an incomplete set.
	proxy := buildProxy(t, root, synthVersion, probeCore)
	env := offlineEnv(t, proxy)

	consumer := newConsumerRequiring(t, probe, synthVersion)
	out, err := run(goBin, consumer, env, "build", "./...")
	if err == nil {
		t.Fatalf("RELEASE-EXTERNAL-GET-01: consumer built against an INCOMPLETE published set "+
			"(missing %s) — the green path is vacuous.\n%s", probeCore, out)
	}
	// The failure must name the missing internal sibling, not something incidental.
	if !strings.Contains(out, probeCore) {
		t.Fatalf("RELEASE-EXTERNAL-GET-01: build failed but not on the missing sibling %s — unexpected cause:\n%s", probeCore, out)
	}
}

// TestExternalGet_AllPublishable_Build extends the probe-only green path to EVERY
// publishable module: a consumer that imports each one in turn must compile
// against the bumped publish, deepening into that module's own internal closure.
// This proves the synchronized release makes the WHOLE set externally consumable,
// not just the postgres probe (the detailed go get / go list assertions stay on
// the probe in TestExternalGet_Bumped_Resolves).
func TestExternalGet_AllPublishable_Build(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping all-module external build smoke in -short mode")
	}
	goBin := mustGo(t)
	root := repoRoot(t)
	proxy := buildProxy(t, root, synthVersion)
	env := offlineEnv(t, proxy)

	mods, err := modrelease.PublishableModules(root)
	if err != nil {
		t.Fatalf("PublishableModules: %v", err)
	}
	if len(mods) < 10 {
		t.Fatalf("anti-vacuity: only %d publishable modules enumerated", len(mods))
	}
	for _, m := range mods {
		t.Run(m.ImportPath, func(t *testing.T) {
			consumer := newConsumerRequiring(t, m.ImportPath, synthVersion)
			if out, err := run(goBin, consumer, env, "build", "./..."); err != nil {
				t.Fatalf("RELEASE-EXTERNAL-GET-01: external build against published %s@%s failed:\n%s",
					m.ImportPath, synthVersion, out)
			}
		})
	}
}

// buildProxy publishes every publishable module at version into a fresh file
// GOPROXY directory and returns its path. When bump is true each module's go.mod
// is release-bumped first; otherwise the as-committed go.mod (with v0.0.0) is used.
//
// Each published module is an INTERNAL-DEPS-ONLY PROJECTION of the real module:
// external requires are stripped and the package is a stub that blank-imports the
// module's internal siblings (the github.com/ghbvf/gocell… requires). This keeps
// the smoke hermetic — building a consumer that imports the probe traverses the
// real INTERNAL dependency graph (postgres → gocell, adapterutil), forcing
// version resolution of exactly the modules the bump rewrites, without dragging
// in the root module's external universe. The local replace is PRESERVED to prove
// Go ignores a dependency's replace (the OTel-canonical shape).
func buildProxy(t *testing.T, root, version string, omit ...string) string {
	t.Helper()
	prefix, err := gomodutil.ReadModulePath(root)
	if err != nil {
		t.Fatalf("read root module path: %v", err)
	}
	mods, err := modrelease.PublishableModules(root)
	if err != nil {
		t.Fatalf("PublishableModules: %v", err)
	}
	omitted := make(map[string]bool, len(omit))
	for _, o := range omit {
		omitted[o] = true
	}
	proxy := t.TempDir()
	for _, m := range mods {
		if omitted[m.ImportPath] {
			continue // simulate an incomplete published set (a sibling left untagged)
		}
		goMod := stageGoMod(t, filepath.Join(root, m.Dir), prefix, version)
		projected, internalImports := internalProjection(t, goMod, prefix)
		writeProxyModule(t, proxy, m.ImportPath, version, projected, stubPackage(m.ImportPath, internalImports))
	}
	return proxy
}

// internalProjection drops every external require from goMod (keeping the module
// line, go directive, internal github.com/ghbvf/gocell… requires, and all replace
// directives) and returns the projected go.mod plus the internal require paths
// (the siblings the stub package must import to force their resolution).
func internalProjection(t *testing.T, goMod []byte, prefix string) ([]byte, []string) {
	t.Helper()
	mf, err := modfile.Parse("go.mod", goMod, nil)
	if err != nil {
		t.Fatalf("parse go.mod for projection: %v", err)
	}
	var internalImports []string
	var drop []string
	for _, r := range mf.Require {
		if r.Mod.Path == prefix || strings.HasPrefix(r.Mod.Path, prefix+"/") {
			internalImports = append(internalImports, r.Mod.Path)
		} else {
			drop = append(drop, r.Mod.Path)
		}
	}
	for _, p := range drop {
		_ = mf.DropRequire(p)
	}
	mf.Cleanup()
	out, err := mf.Format()
	if err != nil {
		t.Fatalf("format projected go.mod: %v", err)
	}
	return out, internalImports
}

// stageGoMod copies modDir/go.mod into a temp dir, release-bumps it, and returns
// the resulting bytes (the published .mod / zip go.mod).
func stageGoMod(t *testing.T, modDir, prefix, version string) []byte {
	t.Helper()
	src, err := os.ReadFile(filepath.Clean(filepath.Join(modDir, "go.mod")))
	if err != nil {
		t.Fatalf("read %s/go.mod: %v", modDir, err)
	}
	stage := t.TempDir()
	if err := os.WriteFile(filepath.Clean(filepath.Join(stage, "go.mod")), src, 0o644); err != nil {
		t.Fatalf("stage go.mod: %v", err)
	}
	if _, err := modrelease.BumpModule(stage, prefix, version); err != nil {
		t.Fatalf("BumpModule(%s): %v", modDir, err)
	}
	out, err := os.ReadFile(filepath.Clean(filepath.Join(stage, "go.mod")))
	if err != nil {
		t.Fatalf("read staged go.mod: %v", err)
	}
	return out
}

// writeProxyModule writes the GOPROXY file layout (.info/.mod/.zip/list) for one
// module at version. The .mod is the projected go.mod; the .zip carries that
// go.mod plus pkgSource — the package source for the module-root package (a
// library stub from [stubPackage] for go-get probes, or a main stub from
// [stubMainPackage] for the go-install smoke), which blank-imports the module's
// internal siblings so building it forces their resolution.
func writeProxyModule(t *testing.T, proxyRoot, importPath, version string, goMod []byte, pkgSource string) {
	t.Helper()
	esc, err := module.EscapePath(importPath)
	if err != nil {
		t.Fatalf("escape %s: %v", importPath, err)
	}
	dir := filepath.Join(proxyRoot, filepath.FromSlash(esc), "@v")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir proxy dir: %v", err)
	}
	write := func(name string, data []byte) {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	write(version+".info", []byte(fmt.Sprintf(`{"Version":%q,"Time":"2000-01-01T00:00:00Z"}`, version)))
	write(version+".mod", goMod)
	write("list", []byte(version+"\n"))
	writeModuleZip(t, filepath.Join(dir, version+".zip"), importPath, version, goMod, pkgSource)
}

func writeModuleZip(t *testing.T, zipPath, importPath, version string, goMod []byte, pkgSource string) {
	t.Helper()
	f, err := os.Create(zipPath) //nolint:gosec // G304: zipPath is under t.TempDir()
	if err != nil {
		t.Fatalf("create zip: %v", err)
	}
	defer func() { _ = f.Close() }()
	zw := zip.NewWriter(f)
	prefix := importPath + "@" + version + "/"
	add := func(name string, data []byte) {
		w, err := zw.Create(prefix + name)
		if err != nil {
			t.Fatalf("zip create %s: %v", name, err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatalf("zip write %s: %v", name, err)
		}
	}
	add("go.mod", goMod)
	add("doc.go", []byte(pkgSource))
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
}

// stubPackage renders a minimal compilable package for importPath that
// blank-imports its internal siblings, so building it traverses the internal
// dependency graph (forcing version resolution) without importing anything
// external.
func stubPackage(importPath string, internalImports []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "package %s\n", pkgName(importPath))
	if len(internalImports) > 0 {
		b.WriteString("\nimport (\n")
		for _, imp := range internalImports {
			fmt.Fprintf(&b, "\t_ %q\n", imp)
		}
		b.WriteString(")\n")
	}
	return b.String()
}

// pkgName derives a valid package identifier from a module path's last segment.
func pkgName(importPath string) string {
	seg := path.Base(importPath)
	var b strings.Builder
	for _, r := range seg {
		if r == '_' || ('a' <= r && r <= 'z') || ('A' <= r && r <= 'Z') || ('0' <= r && r <= '9') {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "lib"
	}
	return b.String()
}

// newConsumer creates a throwaway external module that depends on nothing yet.
func newConsumer(t *testing.T) string {
	t.Helper()
	return writeConsumer(t, "module smoke.example/consumer\n\ngo 1.25\n")
}

// newConsumerRequiring creates a throwaway external module that directly requires
// AND imports modPath@version. The blank import forces Go (under the default
// pruned graph) to treat the probe as providing-an-imported-package and deepen
// into ITS internal requires (gocell, adapterutil), fetching their .mod —
// performing real build-list MVS resolution rather than `go get`'s lenient
// upgrade. The probe's published stub imports nothing, so deepening stops there:
// the root module's external universe is never loaded.
func newConsumerRequiring(t *testing.T, modPath, version string) string {
	t.Helper()
	dir := writeConsumer(t, fmt.Sprintf(
		"module smoke.example/consumer\n\ngo 1.25\n\nrequire %s %s\n", modPath, version))
	use := fmt.Sprintf("package consumer\n\nimport _ %q\n", modPath)
	if err := os.WriteFile(filepath.Join(dir, "use.go"), []byte(use), 0o644); err != nil {
		t.Fatalf("write consumer use.go: %v", err)
	}
	return dir
}

func writeConsumer(t *testing.T, goMod string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(goMod), 0o644); err != nil {
		t.Fatalf("write consumer go.mod: %v", err)
	}
	return dir
}

// offlineEnv resolves every module from the file proxy and nothing else: because
// the proxy serves internal-deps-only projections, a consumer's build closure is
// entirely internal modules, so a FRESH per-test GOMODCACHE keeps the test fully
// hermetic — no warm cache, no network — and prevents one test's published
// version from masking another's omission. GOWORK=off stops the repo's go.work
// from absorbing the consumer and bypassing the proxy; GOTOOLCHAIN=local prevents
// a toolchain download.
func offlineEnv(t *testing.T, proxy string) []string {
	t.Helper()
	return append(os.Environ(),
		"GOPROXY=file://"+filepath.ToSlash(proxy),
		"GOMODCACHE="+filepath.Join(t.TempDir(), "modcache"),
		"GOSUMDB=off",
		// -modcacherw keeps the per-test module cache writable so t.TempDir()
		// cleanup can remove it (Go otherwise extracts read-only files).
		"GOFLAGS=-mod=mod -modcacherw",
		"GOWORK=off",
		"GOTOOLCHAIN=local",
	)
}

func mustGo(t *testing.T) string {
	t.Helper()
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go not in PATH; external get smoke skipped")
	}
	return goBin
}

func run(goBin, dir string, env []string, args ...string) (string, error) {
	cmd := exec.Command(goBin, args...) //nolint:gosec // G204: const args; cwd is t.TempDir()
	cmd.Dir = dir
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func runOK(t *testing.T, goBin, dir string, env []string, args ...string) string {
	t.Helper()
	out, err := run(goBin, dir, env, args...)
	if err != nil {
		t.Fatalf("go %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

func assertResolved(t *testing.T, listOut, modPath, version string) {
	t.Helper()
	want := modPath + " " + version
	for _, line := range strings.Split(listOut, "\n") {
		if strings.TrimSpace(line) == want {
			return
		}
	}
	t.Errorf("RELEASE-EXTERNAL-GET-01: `go list -m all` did not resolve %q to %q:\n%s", modPath, version, listOut)
}

const internalPrefix = "github.com/ghbvf/gocell"

// assertNoInternalV000 fails if any internal (github.com/ghbvf/gocell…) module
// resolves to exactly v0.0.0. It ignores external pseudo-versions of the form
// v0.0.0-<timestamp>-<hash> by comparing the version field exactly.
func assertNoInternalV000(t *testing.T, listOut string) {
	t.Helper()
	for _, line := range strings.Split(listOut, "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && strings.HasPrefix(f[0], internalPrefix) && f[1] == "v0.0.0" {
			t.Errorf("RELEASE-EXTERNAL-GET-01: internal module %q resolved to v0.0.0 (require not bumped):\n%s", f[0], listOut)
			return
		}
	}
}

// assertNoInternalReplace fails if any internal module resolves via a replacement
// (=>), which would mean a dependency's local replace leaked into resolution.
func assertNoInternalReplace(t *testing.T, listOut string) {
	t.Helper()
	for _, line := range strings.Split(listOut, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), internalPrefix) && strings.Contains(line, "=>") {
			t.Errorf("RELEASE-EXTERNAL-GET-01: internal module resolved via replacement (=>) — dependency replace leaked:\n%s", line)
			return
		}
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.work")); statErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("repoRoot: no go.work found walking up from %s", dir)
		}
		dir = parent
	}
}

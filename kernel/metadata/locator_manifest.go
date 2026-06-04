// locator_manifest.go — Manifest-driven layout walker for Locator.
//
// Reads .gocell/manifest.yaml (buf v2 style), expands include globs against
// each module path, and emits MetadataSources. The same manifest expresses
// both single-module (Operator-SDK external repo) and multi-module
// (Workspace aggregation) layouts.
//
// ref: bufbuild/buf v2 buf.yaml modules: schema.
//
// This file is part of the LOCATOR-DISCOVERY-FUNNEL-01 allowlist (see
// locator_conventional.go header). Path-prefix literals and fs.WalkDir
// callsites are permitted here because Locator's funnel boundary is
// kernel/metadata/locator*.go.

package metadata

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// ManifestSpec is the parsed .gocell/manifest.yaml file.
//
// Corresponds to the top-level buf v2 buf.yaml modules: schema:
//
//	version: v1
//	modules:
//	  - path: .
//	    includes: { cells: ["cells/*/cell.yaml"], ... }
//	    excludes: ["generated/**", "vendor/**"]
//
// Version must be "v1". Modules must have at least one entry.
type ManifestSpec struct {
	Version string           `yaml:"version"`
	Modules []ManifestModule `yaml:"modules"`
}

// ManifestModule is a single module entry inside ManifestSpec.Modules.
//
// Path is required, must be a non-empty relative path, and must not escape
// outside the manifest directory (no ".." segments, no absolute paths).
// Use "." for the module that lives at the same level as manifest.yaml.
//
// Includes carries per-kind glob patterns. When empty, the conventional
// 5-pattern defaults are used (cells/*/cell.yaml, cells/*/slices/*/slice.yaml,
// contracts/**/contract.yaml, journeys/J-*.yaml, assemblies/*/assembly.yaml,
// plus actors.yaml and journeys/status-board.yaml singletons). This lets
// Operator-SDK modules omit includes: entirely and still get conventional
// discovery.
//
// Excludes applies path-prefix filters across all source kinds in this module.
// "generated/**" is always appended to the effective excludes list (additive).
// "vendor/**" is NOT added automatically — declare it explicitly if needed.
// Specifying an explicit excludes: list does not replace the generated/** guard;
// both the user-declared patterns and "generated/**" are applied.
type ManifestModule struct {
	Path     string           `yaml:"path"`
	Includes ManifestIncludes `yaml:"includes"`
	Excludes []string         `yaml:"excludes"`
}

// ManifestIncludes maps each metadata source kind to one or more glob
// patterns. Globs are resolved relative to the owning ManifestModule.Path.
// Patterns support "*" (single segment) and "**" (zero or more segments,
// implemented via fs.WalkDir — not filepath.Glob which has undefined "**"
// semantics). Ref: bufbuild/buf v2 buf.yaml includes field.
//
// Cells / Slices / Contracts / Journeys / Assemblies accept multiple patterns
// (slice of strings); Actors and StatusBoard are singletons (single string).
//
// Actors and StatusBoard are workspace-level singletons: they may only appear
// in Modules[0] (validated by loadManifest). Declaring them in more than one
// module entry causes loadManifest to return a validation error.
type ManifestIncludes struct {
	// Cells corresponds to the "cells:" YAML key.
	Cells []string `yaml:"cells"`
	// Slices corresponds to the "slices:" YAML key.
	Slices []string `yaml:"slices"`
	// Contracts corresponds to the "contracts:" YAML key.
	Contracts []string `yaml:"contracts"`
	// Journeys corresponds to the "journeys:" YAML key.
	Journeys []string `yaml:"journeys"`
	// Assemblies corresponds to the "assemblies:" YAML key.
	Assemblies []string `yaml:"assemblies"`
	// Actors corresponds to the "actors:" YAML key (singular string, not a list).
	Actors string `yaml:"actors"`
	// StatusBoard corresponds to the "statusBoard:" YAML key (camelCase, not kebab-case).
	StatusBoard string `yaml:"statusBoard"`
}

// defaultManifestIncludes mirrors the conventional 5-pattern layout. Used
// when a module omits the includes: field.
func defaultManifestIncludes() ManifestIncludes {
	return ManifestIncludes{
		Cells:       []string{"cells/*/cell.yaml"},
		Slices:      []string{"cells/*/slices/*/slice.yaml"},
		Contracts:   []string{"contracts/**/contract.yaml"},
		Journeys:    []string{"journeys/J-*.yaml"},
		Assemblies:  []string{"assemblies/*/assembly.yaml"},
		Actors:      "actors.yaml",
		StatusBoard: "journeys/status-board.yaml",
	}
}

const (
	manifestSchemaVersion = "v1"

	// maxManifestFileSize caps the .gocell/manifest.yaml file at 64 KiB.
	// Real manifests are well under 1 KiB (a handful of module entries);
	// 64 KiB leaves headroom for very large workspaces while preventing
	// memory exhaustion from an adversarial multi-MB manifest.
	maxManifestFileSize = 64 << 10 // 64 KiB

	// maxManifestModules caps the number of module entries to prevent
	// O(modules × glob_patterns) WalkDir amplification. 256 is well
	// above any realistic workspace size and below the threshold where
	// glob expansion becomes a DoS vector.
	maxManifestModules = 256

	// maxManifestIncludePatternsPerKind caps the number of include patterns
	// per kind (cells/slices/contracts/journeys/assemblies) per module at 128.
	// Actors and StatusBoard are single-string fields and are not subject to
	// this cap. The limit prevents O(patterns × files) WalkDir amplification
	// from a manifest with thousands of explicit include globs.
	maxManifestIncludePatternsPerKind = 128

	// maxManifestMatchesPerGlob caps the number of files a single glob pattern
	// may match per WalkDir invocation. 50 000 is well above any realistic
	// workspace size (GoCell itself has ~50 metadata files). Exceeding the cap
	// aborts the walk with an error rather than silently returning a truncated
	// result set — no silent truncation.
	maxManifestMatchesPerGlob = 50_000
)

// ReadManifestModulePaths reads the manifest at manifestPath from fsys and
// returns its declared module disk paths (ManifestModule.Path), in declaration
// order. It runs the same loadManifest validation (schema version, path safety,
// singleton uniqueness), so a malformed manifest fails closed rather than
// yielding a partial set.
//
// It is the exported accessor for the workspace's metadata-module set. Tooling
// (the archtest workspace enumerator in tools/workspace) cross-checks this set
// against go.work's `use` directives — every metadata module MUST be a Go module
// the toolchain compiles (manifest.modules ⊆ go.work.use); a manifest module
// absent from go.work is a drift bug surfaced fail-closed at archtest time.
func ReadManifestModulePaths(fsys fs.FS, manifestPath string) ([]string, error) {
	spec, err := loadManifest(fsys, manifestPath)
	if err != nil {
		return nil, err
	}
	paths := make([]string, len(spec.Modules))
	for i, m := range spec.Modules {
		paths[i] = m.Path
	}
	return paths, nil
}

// loadManifest reads and decodes the manifest at manifestPath from fsys,
// validating the schema version, module path safety, and singleton
// uniqueness invariants.
func loadManifest(fsys fs.FS, manifestPath string) (*ManifestSpec, error) {
	spec, err := readManifestSpec(fsys, manifestPath)
	if err != nil {
		return nil, err
	}
	if err := validateManifestSpec(fsys, manifestPath, spec); err != nil {
		return nil, err
	}
	return spec, nil
}

// readManifestSpec performs only the file IO + YAML decode steps for
// loadManifest, keeping validation in validateManifestSpec to satisfy the
// kernel/-layer 15-complexity budget. The 64 KiB size cap prevents a
// huge-manifest DoS at the wire boundary, mirroring unmarshalFile's
// maxMetadataFileSize policy for cell/slice/contract YAML.
func readManifestSpec(fsys fs.FS, manifestPath string) (*ManifestSpec, error) {
	data, err := fs.ReadFile(fsys, manifestPath)
	if err != nil {
		return nil, fmt.Errorf("read manifest %s: %w", manifestPath, err)
	}
	if len(data) > maxManifestFileSize {
		return nil, fmt.Errorf("manifest %s: exceeds size limit (size=%d limit=%d)",
			manifestPath, len(data), maxManifestFileSize)
	}
	var spec ManifestSpec
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&spec); err != nil {
		return nil, fmt.Errorf("decode manifest %s: %w", manifestPath, err)
	}
	return &spec, nil
}

// validateManifestSpec applies the schema-version + module-path + singleton
// invariants. Split out from loadManifest to keep cognitive complexity below
// the kernel/-layer 15 budget.
func validateManifestSpec(fsys fs.FS, manifestPath string, spec *ManifestSpec) error {
	if spec.Version != manifestSchemaVersion {
		return fmt.Errorf("manifest %s: unsupported version %q (want %q)",
			manifestPath, spec.Version, manifestSchemaVersion)
	}
	if len(spec.Modules) == 0 {
		return fmt.Errorf("manifest %s: at least one module entry required", manifestPath)
	}
	if len(spec.Modules) > maxManifestModules {
		return fmt.Errorf(
			"manifest %s: too many modules (count=%d limit=%d) — risk WalkDir amplification",
			manifestPath, len(spec.Modules), maxManifestModules,
		)
	}
	seenPaths := make(map[string]int)
	for i, m := range spec.Modules {
		if err := validateManifestModuleEntry(fsys, manifestPath, i, m, seenPaths); err != nil {
			return err
		}
	}
	return nil
}

// validateManifestModuleEntry applies all per-module invariants: path
// safety (no absolute / no parent escape), directory existence, duplicate
// detection, singleton uniqueness, and exclude/include glob safety.
func validateManifestModuleEntry(fsys fs.FS, manifestPath string, i int, m ManifestModule, seenPaths map[string]int) error {
	if err := validateManifestModulePath(m.Path); err != nil {
		return fmt.Errorf("manifest %s: modules[%d].path: %w", manifestPath, i, err)
	}
	normPath := path.Clean(m.Path)
	if dup, ok := seenPaths[normPath]; ok {
		return fmt.Errorf("manifest %s: modules[%d].path %q duplicates modules[%d]",
			manifestPath, i, m.Path, dup)
	}
	seenPaths[normPath] = i
	if err := validateManifestModuleDir(fsys, manifestPath, i, normPath); err != nil {
		return err
	}
	if err := validateManifestSingleton(manifestPath, i, m); err != nil {
		return err
	}
	return validateManifestGlobs(manifestPath, i, m)
}

// validateManifestModuleDir checks that the module path exists as a directory
// in fsys. The special path "." (workspace root) is always considered valid
// because fs.FS roots do not expose themselves as a nameable directory entry.
func validateManifestModuleDir(fsys fs.FS, manifestPath string, i int, normPath string) error {
	if normPath == "." {
		return nil
	}
	info, err := fs.Stat(fsys, normPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("manifest %s: modules[%d].path %q does not exist",
				manifestPath, i, normPath)
		}
		return fmt.Errorf("manifest %s: modules[%d].path %q stat: %w", manifestPath, i, normPath, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("manifest %s: modules[%d].path %q exists but is not a directory",
			manifestPath, i, normPath)
	}
	return nil
}

// validateManifestGlobs rejects glob patterns in includes/excludes that
// contain parent-escape ("..") segments, contain empty strings, or exceed
// the per-kind pattern count cap. Without the path-escape guard, an exclude
// like "../../other-module/cells/**" could suppress entries from a sibling
// module in Workspace mode. Empty patterns are rejected to surface manifest
// typos early. The per-kind cap (maxManifestIncludePatternsPerKind) prevents
// O(patterns × files) WalkDir amplification.
func validateManifestGlobs(manifestPath string, i int, m ManifestModule) error {
	if err := validateManifestExcludeGlobs(manifestPath, i, m.Excludes); err != nil {
		return err
	}
	return validateManifestIncludeGlobs(manifestPath, i, m.Includes)
}

// validateManifestExcludeGlobs validates the excludes list for a module entry.
func validateManifestExcludeGlobs(manifestPath string, i int, excludes []string) error {
	for _, p := range excludes {
		if err := validateManifestModulePath(p); err != nil {
			return fmt.Errorf("manifest %s: modules[%d].excludes %q: %w", manifestPath, i, p, err)
		}
	}
	return nil
}

// validateManifestIncludeGlobs validates the includes struct for a module entry:
// checks per-kind count cap, rejects empty patterns, and validates path safety.
func validateManifestIncludeGlobs(manifestPath string, i int, inc ManifestIncludes) error {
	type namedBucket struct {
		name     string
		patterns []string
	}
	buckets := []namedBucket{
		{"cells", inc.Cells},
		{"slices", inc.Slices},
		{"contracts", inc.Contracts},
		{"journeys", inc.Journeys},
		{"assemblies", inc.Assemblies},
	}
	for _, nb := range buckets {
		if err := validateIncludeBucket(manifestPath, i, nb.name, nb.patterns); err != nil {
			return err
		}
	}
	if inc.Actors != "" {
		if err := validateManifestModulePath(inc.Actors); err != nil {
			return fmt.Errorf("manifest %s: modules[%d].includes.actors %q: %w",
				manifestPath, i, inc.Actors, err)
		}
	}
	if inc.StatusBoard != "" {
		if err := validateManifestModulePath(inc.StatusBoard); err != nil {
			return fmt.Errorf("manifest %s: modules[%d].includes.statusBoard %q: %w",
				manifestPath, i, inc.StatusBoard, err)
		}
	}
	return nil
}

// validateIncludeBucket validates a single named include pattern bucket.
func validateIncludeBucket(manifestPath string, i int, name string, patterns []string) error {
	if len(patterns) > maxManifestIncludePatternsPerKind {
		return fmt.Errorf(
			"manifest %s: modules[%d].includes.%s: too many patterns (count=%d limit=%d)",
			manifestPath, i, name, len(patterns), maxManifestIncludePatternsPerKind,
		)
	}
	for _, p := range patterns {
		if p == "" {
			return fmt.Errorf("manifest %s: modules[%d].includes.%s: empty pattern not allowed",
				manifestPath, i, name)
		}
		if err := validateManifestModulePath(p); err != nil {
			return fmt.Errorf("manifest %s: modules[%d].includes %q: %w", manifestPath, i, p, err)
		}
	}
	return nil
}

// validateManifestSingleton rejects actors / statusBoard declarations on
// modules other than modules[0] (the workspace root).
func validateManifestSingleton(manifestPath string, i int, m ManifestModule) error {
	if i == 0 {
		return nil
	}
	if m.Includes.Actors != "" {
		return fmt.Errorf("manifest %s: modules[%d].includes.actors set; "+
			"actors.yaml is a workspace-level singleton and may only be declared on modules[0]",
			manifestPath, i)
	}
	if m.Includes.StatusBoard != "" {
		return fmt.Errorf("manifest %s: modules[%d].includes.statusBoard set; "+
			"status-board.yaml is a workspace-level singleton and may only be declared on modules[0]",
			manifestPath, i)
	}
	return nil
}

// validateManifestModulePath rejects absolute paths and any path containing
// ".." segments. "." is allowed and means the manifest directory itself.
// This is the security boundary against escape outside the workspace root.
//
// Uses filepath.IsAbs(filepath.FromSlash(p)) — consistent with
// validateManifestRelativePath in locator.go — so that Windows-style absolute
// paths (e.g. "C:\foo") that path.IsAbs would not recognize are also caught.
func validateManifestModulePath(p string) error {
	if p == "" {
		return errors.New("path is required")
	}
	if filepath.IsAbs(filepath.FromSlash(p)) {
		return fmt.Errorf("absolute path not allowed: %s", p)
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return fmt.Errorf("path escape not allowed: %s contains parent reference", p)
		}
	}
	return nil
}

// discoverManifest walks each module declared in the manifest, expanding the
// include globs (or defaults) and merging the result into a deduplicated set
// of MetadataSources. allowSingletons is restricted to modules[0] so only the
// workspace root contributes actors.yaml and status-board.yaml.
func (l *Locator) discoverManifest() ([]MetadataSource, error) {
	if l.manifestSpec == nil {
		return nil, errors.New("metadata: locator: manifest mode but spec nil")
	}
	var out []MetadataSource
	seen := make(map[string]struct{})
	for i, mod := range l.manifestSpec.Modules {
		modSources, err := l.discoverManifestModule(mod, i == 0)
		if err != nil {
			return nil, fmt.Errorf("module[%d] %s: %w", i, mod.Path, err)
		}
		for _, src := range modSources {
			if _, dup := seen[src.Path]; dup {
				continue
			}
			seen[src.Path] = struct{}{}
			out = append(out, src)
		}
	}
	return out, nil
}

// manifestEmission groups one set of glob patterns by destination
// SourceKind plus an optional cell-ID derivation strategy.
//
// userDeclared is true when the patterns came from the manifest's explicit
// includes: field. When false (defaults apply), a zero-match is only a
// slog.Warn; when true, a zero-match is a hard error (typo guard — F2).
type manifestEmission struct {
	patterns     []string
	kind         SourceKind
	deriveCell   func(p string) string
	userDeclared bool // true when patterns came from explicit includes:
}

// manifestModulePlan packages the per-emission plan + the singleton paths +
// the compiled exclude set + the (already-normalised) module base path.
// Building the plan in one place keeps discoverManifestModule itself a
// straight three-line walk over the plan.
type manifestModulePlan struct {
	base            string
	excludes        *manifestExcludeSet
	emissions       []manifestEmission
	actorsPath      string
	statusBoardPath string
	allowSingletons bool
}

// discoverManifestModule walks one manifest module: expands each include
// pattern, classifies matching files, and applies excludes. allowSingletons
// gates actors / status-board emission to the first module (workspace root).
func (l *Locator) discoverManifestModule(mod ManifestModule, allowSingletons bool) ([]MetadataSource, error) {
	plan := buildManifestModulePlan(mod, allowSingletons)
	out, err := l.discoverManifestPlanEmissions(plan)
	if err != nil {
		return nil, err
	}
	singletons, err := l.discoverManifestPlanSingletons(plan)
	if err != nil {
		return nil, err
	}
	return append(out, singletons...), nil
}

// buildManifestModulePlan normalises the include defaults + base path and
// produces the per-emission plan executed by discoverManifestPlanEmissions /
// discoverManifestPlanSingletons.
//
// "generated/**" is always appended to the effective exclude list (additive,
// not replacing). This ensures codegen output never silently re-enters the
// metadata scan regardless of whether the user declared explicit excludes.
// Users who explicitly declare excludes: ["fixtures/**"] end up with an
// effective set of {"fixtures/**", "generated/**"}.
func buildManifestModulePlan(mod ManifestModule, allowSingletons bool) manifestModulePlan {
	userHasIncludes := !manifestIncludesEmpty(mod.Includes)
	includes := mod.Includes
	if !userHasIncludes {
		includes = defaultManifestIncludes()
	}
	base := path.Clean(mod.Path)
	if base == "." {
		base = ""
	}
	excludes := appendGeneratedExclude(mod.Excludes)
	// userDeclared is true for a specific kind's emission only when the user
	// explicitly provided patterns for that kind. When the user declares ANY
	// includes but omits a specific kind, that kind's patterns come from
	// defaults (or are empty) and should not trigger fail-closed on zero match.
	cellsDeclared := userHasIncludes && len(mod.Includes.Cells) > 0
	slicesDeclared := userHasIncludes && len(mod.Includes.Slices) > 0
	contractsDeclared := userHasIncludes && len(mod.Includes.Contracts) > 0
	journeysDeclared := userHasIncludes && len(mod.Includes.Journeys) > 0
	assembliesDeclared := userHasIncludes && len(mod.Includes.Assemblies) > 0
	plan := manifestModulePlan{
		base:            base,
		excludes:        compileManifestExcludes(base, excludes),
		allowSingletons: allowSingletons,
		emissions: []manifestEmission{
			{
				patterns: includes.Cells, kind: SourceCell, userDeclared: cellsDeclared,
				deriveCell: func(p string) string {
					id, _ := matchCellPath(stripBase(p, base))
					return id
				},
			},
			{
				patterns: includes.Slices, kind: SourceSlice, userDeclared: slicesDeclared,
				deriveCell: func(p string) string {
					cellID, _ := matchSlicePath(stripBase(p, base))
					return cellID
				},
			},
			{patterns: includes.Contracts, kind: SourceContract, userDeclared: contractsDeclared},
			{patterns: includes.Journeys, kind: SourceJourney, userDeclared: journeysDeclared},
			{patterns: includes.Assemblies, kind: SourceAssembly, userDeclared: assembliesDeclared},
		},
	}
	if allowSingletons {
		if includes.Actors != "" {
			plan.actorsPath = joinManifestPath(base, includes.Actors)
		}
		if includes.StatusBoard != "" {
			plan.statusBoardPath = joinManifestPath(base, includes.StatusBoard)
		}
	}
	return plan
}

// appendGeneratedExclude returns a new slice with "generated/**" appended if
// it is not already present. This is always additive — existing user-declared
// excludes are preserved (F13).
func appendGeneratedExclude(excludes []string) []string {
	const genExclude = "generated/**"
	for _, e := range excludes {
		if e == genExclude {
			return excludes
		}
	}
	result := make([]string, len(excludes)+1)
	copy(result, excludes)
	result[len(excludes)] = genExclude
	return result
}

// discoverManifestPlanEmissions runs each emission's glob expansion and
// excludes filter, producing MetadataSources for cells / slices / contracts
// / journeys / assemblies.
func (l *Locator) discoverManifestPlanEmissions(plan manifestModulePlan) ([]MetadataSource, error) {
	var out []MetadataSource
	for _, em := range plan.emissions {
		emitted, err := l.discoverEmission(plan.base, plan.excludes, em)
		if err != nil {
			return nil, err
		}
		out = append(out, emitted...)
	}
	return out, nil
}

// discoverEmission expands one emission's glob patterns against l.fsys,
// filters excludes, and emits MetadataSources with derived CellID where
// applicable. Split out so discoverManifestPlanEmissions itself stays a
// straight three-line walk under the kernel/-layer 15-complexity budget.
//
// Fail-closed at emission granularity (all patterns combined); per-pattern
// zero-match emits slog.Warn for typo diagnostics.
//
// For user-declared patterns (em.userDeclared == true): if all patterns
// combined produce zero matches, a hard error is returned — this is almost
// always a manifest typo and silent skipping would hide misconfiguration
// behind a "PASS, but 0 cells found" outcome (F2 fail-closed). Individual
// patterns that match zero files within a multi-pattern emission emit a
// slog.Warn; the emission as a whole succeeds as long as at least one
// pattern matches (multi-pattern fallback is legitimate).
//
// For default patterns (em.userDeclared == false): zero matches across the
// entire emission emits only a structured slog.Warn for workspaces that
// legitimately omit certain source kinds.
func (l *Locator) discoverEmission(base string, excludes *manifestExcludeSet, em manifestEmission) ([]MetadataSource, error) {
	var out []MetadataSource
	for _, g := range em.patterns {
		emitted, err := l.collectEmissionPattern(base, g, excludes, em)
		if err != nil {
			return nil, err
		}
		out = append(out, emitted...)
	}
	if len(out) == 0 {
		return l.handleEmissionZeroMatch(base, em)
	}
	return out, nil
}

// handleEmissionZeroMatch is called when all patterns in an emission produced
// zero results after excludes filtering. For user-declared emissions it returns
// a hard error (fail-closed); for default emissions it emits a slog.Warn.
func (l *Locator) handleEmissionZeroMatch(base string, em manifestEmission) ([]MetadataSource, error) {
	displayBase := base
	if displayBase == "" {
		displayBase = "."
	}
	if em.userDeclared {
		err := fmt.Errorf(
			"manifest module %q include patterns for kind=%s matched zero files"+
				" — check extension: .yaml not .yml; check path separator: forward slash;"+
				" verify glob pattern matches expected layout",
			displayBase, em.kind.String(),
		)
		slog.Error("metadata: locator manifest emission zero-match — fail-closed",
			slog.String("kind", em.kind.String()),
			slog.String("module_base", displayBase),
			slog.Any("err", err))
		return nil, err
	}
	slog.Warn("metadata: locator manifest emission matched zero files (default patterns)",
		slog.String("kind", em.kind.String()),
		slog.String("module_base", displayBase))
	return nil, nil
}

// collectEmissionPattern handles a single glob pattern within an emission,
// split from discoverEmission to keep cognitive complexity within budget.
// Per-pattern zero matches emit a slog.Warn for typo diagnostics; the
// emission-level fail-closed check is in discoverEmission.
func (l *Locator) collectEmissionPattern(
	base, g string,
	excludes *manifestExcludeSet,
	em manifestEmission,
) ([]MetadataSource, error) {
	matches, err := matchManifestGlob(l.fsys, joinManifestPath(base, g), excludes)
	if err != nil {
		return nil, fmt.Errorf("glob %s: %w", g, err)
	}
	var out []MetadataSource
	for _, m := range matches {
		if excludes.match(m) {
			continue
		}
		src := MetadataSource{Path: m, Kind: em.kind}
		if em.deriveCell != nil {
			src.CellID = em.deriveCell(m)
		}
		out = append(out, src)
	}
	if len(out) == 0 && em.userDeclared {
		displayBase := base
		if displayBase == "" {
			displayBase = "."
		}
		slog.Warn("metadata: locator manifest include pattern matched zero files",
			slog.String("pattern", g),
			slog.String("kind", em.kind.String()),
			slog.String("module_base", displayBase))
	}
	return out, nil
}

// discoverManifestPlanSingletons emits actors.yaml + status-board.yaml when
// allowSingletons was set and the files exist; absent files are skipped
// silently, errors other than not-exist surface up.
func (l *Locator) discoverManifestPlanSingletons(plan manifestModulePlan) ([]MetadataSource, error) {
	if !plan.allowSingletons {
		return nil, nil
	}
	var out []MetadataSource
	actors, ok, err := l.maybeManifestSingleton(plan.actorsPath, SourceActors, plan.excludes, "actors")
	if err != nil {
		return nil, err
	}
	if ok {
		out = append(out, actors)
	}
	sb, ok, err := l.maybeManifestSingleton(plan.statusBoardPath, SourceStatusBoard, plan.excludes, "statusBoard")
	if err != nil {
		return nil, err
	}
	if ok {
		out = append(out, sb)
	}
	return out, nil
}

// maybeManifestSingleton returns (MetadataSource, true, nil) when the
// singleton path exists and is not excluded; (zero, false, nil) when the
// path is empty, file is missing, or excluded; (zero, false, err) only for
// unexpected stat errors. The triple return avoids the (nil, nil) nilnil
// linter complaint.
func (l *Locator) maybeManifestSingleton(
	p string, kind SourceKind, excludes *manifestExcludeSet, label string,
) (MetadataSource, bool, error) {
	if p == "" {
		return MetadataSource{}, false, nil
	}
	exists, err := fileExists(l.fsys, p)
	if err != nil {
		return MetadataSource{}, false, fmt.Errorf("stat %s %s: %w", label, p, err)
	}
	if !exists || excludes.match(p) {
		return MetadataSource{}, false, nil
	}
	return MetadataSource{Path: p, Kind: kind}, true, nil
}

// manifestIncludesEmpty reports whether all include fields are unset.
func manifestIncludesEmpty(inc ManifestIncludes) bool {
	return len(inc.Cells) == 0 && len(inc.Slices) == 0 && len(inc.Contracts) == 0 &&
		len(inc.Journeys) == 0 && len(inc.Assemblies) == 0 &&
		inc.Actors == "" && inc.StatusBoard == ""
}

// joinManifestPath joins a module base path with a relative glob/file path,
// keeping forward-slash separators. Returns just the rel argument when base
// is empty (the workspace root case).
func joinManifestPath(base, rel string) string {
	if base == "" {
		return path.Clean(rel)
	}
	return path.Clean(base + "/" + rel)
}

// stripBase removes the module base prefix from a discovered path so the
// classifier helpers (matchCellPath, matchSlicePath) can derive cell IDs
// using their original layout assumptions. When base is empty, the path is
// returned as-is.
func stripBase(p, base string) string {
	if base == "" {
		return p
	}
	prefix := base + "/"
	if strings.HasPrefix(p, prefix) {
		return strings.TrimPrefix(p, prefix)
	}
	return p
}

// fileExists reports whether p exists as a regular file in fsys.
func fileExists(fsys fs.FS, p string) (bool, error) {
	info, err := fs.Stat(fsys, p)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	return !info.IsDir(), nil
}

// manifestExcludeSet holds compiled exclude patterns for one module.
type manifestExcludeSet struct {
	patterns []string
}

func compileManifestExcludes(base string, patterns []string) *manifestExcludeSet {
	es := &manifestExcludeSet{}
	for _, p := range patterns {
		es.patterns = append(es.patterns, joinManifestPath(base, p))
	}
	return es
}

func (es *manifestExcludeSet) match(filePath string) bool {
	for _, pat := range es.patterns {
		if matchManifestPattern(pat, filePath) {
			return true
		}
	}
	return false
}

// matchDir reports whether dir's entire subtree is excluded by a
// "<prefix>/**" pattern, so WalkDir can SkipDir-prune it. Non-"/**" patterns
// (e.g. a literal file path) never prune a directory — the directory may
// still contain non-excluded files and must be walked.
func (es *manifestExcludeSet) matchDir(dir string) bool {
	for _, pat := range es.patterns {
		prefix, ok := strings.CutSuffix(pat, "/**")
		if !ok {
			continue
		}
		if dir == prefix || strings.HasPrefix(dir, prefix+"/") {
			return true
		}
	}
	return false
}

// matchManifestGlob expands a glob pattern against fsys via WalkDir. Returns
// sorted matches. Symlinks are explicitly skipped to avoid traversal through
// unexpected filesystem topology that os.DirFS might not prevent.
//
// To avoid O(modules × total_files) WalkDir amplification, the walk is
// started from the longest fixed prefix before the first wildcard segment
// ("*" or "**"). For example, "cells/*/cell.yaml" starts from "cells/", so
// only the cells/ subtree is scanned. Patterns that begin with a wildcard
// (e.g. "**/cell.yaml" or "*/cell.yaml") fall back to walking from ".".
// When the computed prefix does not exist in fsys, an empty match list is
// returned without error. Passing an empty pattern returns (nil, nil).
//
// excludes may be nil (no directory pruning). When non-nil, directories whose
// entire subtree is excluded by a "<prefix>/**" pattern are pruned via
// fs.SkipDir, avoiding needless traversal of large excluded subtrees such as
// vendor/ or generated/. File-level exclude filtering is NOT performed here —
// that remains in collectEmissionPattern to cover non-"/**" patterns.
//
// The number of matches is capped at maxManifestMatchesPerGlob to prevent
// unbounded WalkDir amplification; exceeding the cap aborts with an error.
func matchManifestGlob(fsys fs.FS, pattern string, excludes *manifestExcludeSet) ([]string, error) {
	return matchManifestGlobCapped(fsys, pattern, excludes, maxManifestMatchesPerGlob)
}

// matchManifestGlobCapped is matchManifestGlob with an injectable match cap.
// Production always passes maxManifestMatchesPerGlob via matchManifestGlob;
// the parameter exists so tests can exercise the cap-exceeded path with a
// small value instead of materializing 50 000 files in a MapFS (which would
// blow the per-test slowgate budget).
func matchManifestGlobCapped(
	fsys fs.FS, pattern string, excludes *manifestExcludeSet, maxMatches int,
) ([]string, error) {
	if pattern == "" {
		return nil, nil
	}
	root, ok, err := manifestGlobWalkRoot(fsys, pattern)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	var matches []string
	count := 0
	if err := fs.WalkDir(fsys, root, manifestGlobWalkFn(pattern, excludes, &matches, &count, maxMatches)); err != nil {
		return nil, err
	}
	sort.Strings(matches)
	return matches, nil
}

// manifestGlobWalkRoot resolves the WalkDir start directory for a glob pattern.
// Returns (root, true, nil) when the root exists, ("", false, nil) when the
// computed root does not exist in fsys, or ("", false, err) for other stat errors.
func manifestGlobWalkRoot(fsys fs.FS, pattern string) (string, bool, error) {
	root := manifestGlobFixedPrefix(pattern)
	if root == "." {
		return root, true, nil
	}
	if _, err := fs.Stat(fsys, root); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", false, nil
		}
		return "", false, err
	}
	return root, true, nil
}

// manifestGlobWalkFn returns a WalkDir callback that appends matching paths to
// matches. Symlinks are skipped to avoid traversal through unexpected topology.
//
// When excludes is non-nil, directories whose subtree is entirely covered by a
// "<prefix>/**" exclude pattern are pruned with fs.SkipDir. File-level
// excludes (non-"/**" patterns) are NOT filtered here — the caller
// (collectEmissionPattern) does a post-walk file-level check.
//
// count is incremented for each appended match; when it would exceed
// maxMatches the walk is aborted with a descriptive error. Production passes
// maxManifestMatchesPerGlob (via matchManifestGlob); tests inject a small cap.
func manifestGlobWalkFn(pattern string, excludes *manifestExcludeSet, matches *[]string, count *int, maxMatches int) fs.WalkDirFunc {
	return func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		// Explicitly skip symlinks regardless of whether the underlying fs.FS
		// follows them. This avoids traversal through unexpected filesystem
		// topology (e.g. symlink loops) that os.DirFS does not prevent.
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		if d.IsDir() {
			// SkipDir-prune directories whose entire subtree is excluded by a
			// "<prefix>/**" pattern. Non-"/**" excludes are left to the caller.
			if excludes != nil && excludes.matchDir(p) {
				return fs.SkipDir
			}
			return nil
		}
		if matchManifestPattern(pattern, p) {
			*count++
			if *count > maxMatches {
				return fmt.Errorf(
					"metadata: glob %q exceeded match cap (limit=%d)"+
						" — workspace may be too large or pattern too broad",
					pattern, maxMatches,
				)
			}
			*matches = append(*matches, p)
		}
		return nil
	}
}

// manifestGlobFixedPrefix extracts the longest fixed (non-wildcard) path
// prefix from a glob pattern. It returns "." when the pattern begins with
// a wildcard segment or when there is no fixed directory prefix.
//
// Examples:
//
//	"cells/*/cell.yaml"          → "cells"
//	"contracts/**/contract.yaml" → "contracts"
//	"**/cell.yaml"               → "."
//	"*/cell.yaml"                → "."
//	"actors.yaml"                → "." (single-segment file at root, no dir prefix)
//	"cells/foo/bar/cell.yaml"    → "cells/foo/bar"
func manifestGlobFixedPrefix(pattern string) string {
	segs := strings.Split(pattern, "/")
	var fixed []string
	for _, seg := range segs {
		if strings.ContainsAny(seg, "*?[") {
			break
		}
		fixed = append(fixed, seg)
	}
	// fixed is shorter than segs when the loop broke early on a wildcard
	// segment. When fixed == segs, the pattern is a pure literal path with
	// no wildcards; in that case drop the last segment (the filename) so we
	// walk from its parent directory instead of needing a separate
	// Stat-and-match path.
	hasWild := len(fixed) < len(segs)
	if !hasWild && len(fixed) > 0 {
		// Literal path (no wildcards) — walk parent directory.
		fixed = fixed[:len(fixed)-1]
	}
	if len(fixed) == 0 {
		return "."
	}
	return strings.Join(fixed, "/")
}

// matchManifestPattern matches a glob with "*" (single-segment wildcard) and
// "**" (multi-segment wildcard) against a forward-slash path.
func matchManifestPattern(pattern, name string) bool {
	patSegs := strings.Split(pattern, "/")
	nameSegs := strings.Split(name, "/")
	return matchManifestSegments(patSegs, nameSegs)
}

// matchManifestSegments performs the recursive glob match. Supports "**" as
// a multi-segment wildcard and falls back to path.Match for single segments.
func matchManifestSegments(pat, name []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			return matchDoubleStarTail(pat[1:], name)
		}
		if !consumeSingleSegment(&pat, &name) {
			return false
		}
	}
	return len(name) == 0
}

// matchDoubleStarTail handles the "**" wildcard: try matching the remaining
// pattern against every suffix of name, including the empty suffix when
// "**" appears at the end of the pattern.
func matchDoubleStarTail(rest, name []string) bool {
	if len(rest) == 0 {
		return true
	}
	for i := 0; i <= len(name); i++ {
		if matchManifestSegments(rest, name[i:]) {
			return true
		}
	}
	return false
}

// consumeSingleSegment advances pat and name one segment when the leading
// segments match path.Match. Returns false (and leaves pat / name
// unchanged) when there is no match or name is exhausted.
func consumeSingleSegment(pat, name *[]string) bool {
	if len(*name) == 0 {
		return false
	}
	matched, err := path.Match((*pat)[0], (*name)[0])
	if err != nil || !matched {
		return false
	}
	*pat = (*pat)[1:]
	*name = (*name)[1:]
	return true
}

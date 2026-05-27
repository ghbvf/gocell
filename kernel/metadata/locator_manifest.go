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
	"path"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// ManifestSpec is the parsed .gocell/manifest.yaml file.
type ManifestSpec struct {
	Version string           `yaml:"version"`
	Modules []ManifestModule `yaml:"modules"`
}

// ManifestModule is a single module entry inside ManifestSpec.Modules.
//
// Path is required, must be relative, and must not escape outside the
// manifest directory (no ".." segments, no absolute paths).
//
// Includes carries glob patterns per metadata source kind. Empty Includes
// falls back to the conventional 5-pattern defaults.
//
// Excludes are applied across all source kinds within this module.
type ManifestModule struct {
	Path     string           `yaml:"path"`
	Includes ManifestIncludes `yaml:"includes"`
	Excludes []string         `yaml:"excludes"`
}

// ManifestIncludes maps each metadata source kind to one or more glob
// patterns. Globs are resolved relative to the owning ManifestModule.Path.
// Patterns support "*" (single segment) and "**" (any number of segments).
//
// Actors and StatusBoard are workspace-level singletons and may only be
// declared on Modules[0] (validated by loadManifest).
type ManifestIncludes struct {
	Cells       []string `yaml:"cells"`
	Slices      []string `yaml:"slices"`
	Contracts   []string `yaml:"contracts"`
	Journeys    []string `yaml:"journeys"`
	Assemblies  []string `yaml:"assemblies"`
	Actors      string   `yaml:"actors"`
	StatusBoard string   `yaml:"statusBoard"`
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

const manifestSchemaVersion = "v1"

// loadManifest reads and decodes the manifest at manifestPath from fsys,
// validating the schema version, module path safety, and singleton
// uniqueness invariants.
func loadManifest(fsys fs.FS, manifestPath string) (*ManifestSpec, error) {
	data, err := fs.ReadFile(fsys, manifestPath)
	if err != nil {
		return nil, fmt.Errorf("read manifest %s: %w", manifestPath, err)
	}
	var spec ManifestSpec
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&spec); err != nil {
		return nil, fmt.Errorf("decode manifest %s: %w", manifestPath, err)
	}
	if spec.Version != manifestSchemaVersion {
		return nil, fmt.Errorf("manifest %s: unsupported version %q (want %q)",
			manifestPath, spec.Version, manifestSchemaVersion)
	}
	if len(spec.Modules) == 0 {
		return nil, fmt.Errorf("manifest %s: at least one module entry required", manifestPath)
	}
	seenPaths := make(map[string]int)
	for i, m := range spec.Modules {
		if err := validateManifestModulePath(m.Path); err != nil {
			return nil, fmt.Errorf("manifest %s: modules[%d].path: %w", manifestPath, i, err)
		}
		normPath := path.Clean(m.Path)
		if dup, ok := seenPaths[normPath]; ok {
			return nil, fmt.Errorf("manifest %s: modules[%d].path %q duplicates modules[%d]",
				manifestPath, i, m.Path, dup)
		}
		seenPaths[normPath] = i
		if i > 0 {
			if m.Includes.Actors != "" {
				return nil, fmt.Errorf("manifest %s: modules[%d].includes.actors set; "+
					"actors.yaml is a workspace-level singleton and may only be declared on modules[0]",
					manifestPath, i)
			}
			if m.Includes.StatusBoard != "" {
				return nil, fmt.Errorf("manifest %s: modules[%d].includes.statusBoard set; "+
					"status-board.yaml is a workspace-level singleton and may only be declared on modules[0]",
					manifestPath, i)
			}
		}
	}
	return &spec, nil
}

// validateManifestModulePath rejects absolute paths and any path containing
// ".." segments. "." is allowed and means the manifest directory itself.
// This is the security boundary against escape outside the workspace root.
func validateManifestModulePath(p string) error {
	if p == "" {
		return errors.New("path is required")
	}
	if path.IsAbs(p) {
		return fmt.Errorf("absolute path not allowed: %s", p)
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return fmt.Errorf("path escape not allowed: %s contains ..", p)
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

// discoverManifestModule walks one manifest module: expands each include
// pattern, classifies matching files, and applies excludes. allowSingletons
// gates actors / status-board emission to the first module (workspace root).
func (l *Locator) discoverManifestModule(mod ManifestModule, allowSingletons bool) ([]MetadataSource, error) {
	includes := mod.Includes
	if manifestIncludesEmpty(includes) {
		includes = defaultManifestIncludes()
	}
	base := path.Clean(mod.Path)
	if base == "." {
		base = ""
	}
	excludes := compileManifestExcludes(base, mod.Excludes)
	type emission struct {
		patterns   []string
		kind       SourceKind
		deriveCell func(p string) string
	}
	plan := []emission{
		{patterns: includes.Cells, kind: SourceCell, deriveCell: func(p string) string {
			rel := stripBase(p, base)
			id, _ := matchCellPath(rel)
			return id
		}},
		{patterns: includes.Slices, kind: SourceSlice, deriveCell: func(p string) string {
			rel := stripBase(p, base)
			cellID, _, _ := matchSlicePath(rel)
			return cellID
		}},
		{patterns: includes.Contracts, kind: SourceContract},
		{patterns: includes.Journeys, kind: SourceJourney},
		{patterns: includes.Assemblies, kind: SourceAssembly},
	}
	var out []MetadataSource
	for _, em := range plan {
		for _, g := range em.patterns {
			matches, err := matchManifestGlob(l.fsys, joinManifestPath(base, g))
			if err != nil {
				return nil, fmt.Errorf("glob %s: %w", g, err)
			}
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
		}
	}
	if allowSingletons {
		if includes.Actors != "" {
			full := joinManifestPath(base, includes.Actors)
			if exists, err := fileExists(l.fsys, full); err != nil {
				return nil, fmt.Errorf("stat actors %s: %w", full, err)
			} else if exists && !excludes.match(full) {
				out = append(out, MetadataSource{Path: full, Kind: SourceActors})
			}
		}
		if includes.StatusBoard != "" {
			full := joinManifestPath(base, includes.StatusBoard)
			if exists, err := fileExists(l.fsys, full); err != nil {
				return nil, fmt.Errorf("stat statusBoard %s: %w", full, err)
			} else if exists && !excludes.match(full) {
				out = append(out, MetadataSource{Path: full, Kind: SourceStatusBoard})
			}
		}
	}
	return out, nil
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

// matchManifestGlob expands a glob pattern against fsys via WalkDir. Returns
// sorted matches.
func matchManifestGlob(fsys fs.FS, pattern string) ([]string, error) {
	var matches []string
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		if matchManifestPattern(pattern, p) {
			matches = append(matches, p)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(matches)
	return matches, nil
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
	for {
		if len(pat) == 0 {
			return len(name) == 0
		}
		if pat[0] == "**" {
			rest := pat[1:]
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
		if len(name) == 0 {
			return false
		}
		matched, err := path.Match(pat[0], name[0])
		if err != nil || !matched {
			return false
		}
		pat = pat[1:]
		name = name[1:]
	}
}

package packagesload

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"

	"github.com/ghbvf/gocell/tools/workspace"
)

// LoadWorkspace is the single sanctioned satellite-aware loader: it loads patterns
// against the go.work workspace at root, EXPANDING multi-member satellite
// parent-prefixes ("./cmd/...", "./adapters/...", "./examples/...") into their owning
// members so they are actually scanned, never silently skipped.
//
// Routing (faithful to the pre-#2147 typeseval loader it consolidates):
//   - a span across multiple members, OR a single group that IS the workspace root
//     (parent-of-member / root-level patterns), loads FROM the root in ModeWorkspace
//     using the rewritten rootPatterns (post-#1565 the root has no module to anchor a
//     relative "./parent/..." pattern, so it is rewritten to import-path form);
//   - a single member's patterns load per-member in ModeModule (GOWORK=off, via the
//     member's own go.mod);
//   - a tree with no go.work loads as a single ModeModule module at root.
//
// cfgTemplate supplies Mode (the go/packages Need* bits), Tests, BuildFlags, and
// Context; Dir/Env are owned per group (Env via [Load]'s mode handling). The returned
// []packages.Error is the flat, dir-prefixed set collected from every loaded package
// so callers can fail fast on type-check errors without re-walking. This is the ONLY
// satellite-aware package loader in the repo; both the OBS-01 metric-PII scan
// (tools/metricschema) and the archtest typed façade (tools/archtest) route through
// it, so "spawn a second satellite loader" cannot be expressed without reaching
// packages.Load — itself funneled here by PACKAGES-LOAD-FUNNEL-01.
func LoadWorkspace(root string, cfgTemplate packages.Config, patterns ...string) ([]*packages.Package, []packages.Error, error) {
	if groups, rootPatterns, ok := workspacePatternGroups(root, patterns); ok {
		// A span across multiple member modules, OR a single group that IS the
		// workspace root, must load from the root in ModeWorkspace using rootPatterns
		// (where unanchored "./parent/..." patterns are translated to import-path form).
		if len(groups) > 1 || (len(groups) == 1 && groups[0].dir == root) {
			return loadPackageGroups(ModeWorkspace, cfgTemplate, []patternGroup{{dir: root, patterns: rootPatterns}})
		}
		return loadPackageGroups(ModeModule, cfgTemplate, groups)
	}
	// No go.work workspace at root: a single ModeModule (GOWORK=off) load.
	return loadPackageGroups(ModeModule, cfgTemplate, []patternGroup{{dir: root, patterns: patterns}})
}

type patternGroup struct {
	dir      string
	patterns []string
}

func loadPackageGroups(
	mode Mode, cfgTemplate packages.Config, groups []patternGroup,
) ([]*packages.Package, []packages.Error, error) {
	var all []*packages.Package
	var allErrs []packages.Error
	for _, g := range groups {
		cfg := cfgTemplate // value copy; Dir/Env owned per group
		cfg.Dir = g.dir
		pkgs, err := Load(mode, &cfg, g.patterns...)
		if err != nil {
			return nil, nil, fmt.Errorf("packages.Load: %w", err)
		}
		packages.Visit(pkgs, nil, func(p *packages.Package) {
			for i := range p.Errors {
				p.Errors[i].Msg = g.dir + ": " + p.Errors[i].Msg
			}
			allErrs = append(allErrs, p.Errors...)
		})
		all = append(all, pkgs...)
	}
	return all, allErrs, nil
}

// frameworkModuleDir is the workspace-relative dir of the core framework module
// (kernel/runtime/pkg) since the #1565 split — the successor to the pre-split root
// module that "." / "./..." referred to.
const frameworkModuleDir = "framework"

// skipPatternDir is the sentinel member-dir splitWorkspacePattern returns for a
// root-relative parent prefix that no workspace member owns AND that has no member
// living under it — a genuine empty match (e.g. a typo "./nonexistent/..."). The NUL
// byte cannot occur in a real dir, so it is unambiguous.
const skipPatternDir = "\x00skip-no-member"

// workspacePatternGroups returns, for a go.work workspace at root: (1) the
// per-member pattern groups (each loaded in module mode), and (2) rootPatterns —
// the equivalent flat pattern list for a single ModeWorkspace load FROM the root,
// in which any "./parent/..." pattern that spans multiple members (no single
// member owns it) is rewritten to its import-path form so workspace mode resolves
// it without a root module to anchor the relative dir.
func workspacePatternGroups(root string, patterns []string) ([]patternGroup, []string, bool) {
	if !workspace.HasGoWork(root) {
		return nil, nil, false
	}
	mods, err := workspace.Modules(root)
	if err != nil {
		return nil, nil, false
	}
	// Expand satellite parent-prefixes ("./cmd/...", "./adapters/...",
	// "./examples/...") that span multiple members into their per-member patterns
	// so the satellite packages are ACTUALLY scanned. Without this a pattern no
	// single member owns hits splitWorkspacePattern's skip and match-zeroes —
	// silently dropping the ./cmd/… ./adapters/… ./examples/… coverage that
	// security/governance archtest rules declare over those satellites (#1565
	// fidelity, F5). A prefix with zero members under it still falls through to
	// the skip (genuine match-zero).
	expanded := make([]string, 0, len(patterns))
	for _, p := range patterns {
		if parts, ok := expandParentPrefix(mods, p); ok {
			expanded = append(expanded, parts...)
		} else {
			expanded = append(expanded, p)
		}
	}
	patterns = expanded
	groups := make([]patternGroup, 0, len(mods))
	groupByDir := map[string]int{}
	add := func(dir, pattern string) {
		if i, ok := groupByDir[dir]; ok {
			groups[i].patterns = append(groups[i].patterns, pattern)
			return
		}
		groupByDir[dir] = len(groups)
		groups = append(groups, patternGroup{dir: dir, patterns: []string{pattern}})
	}
	rootPatterns := make([]string, 0, len(patterns))
	for _, pattern := range patterns {
		moduleDir, modulePattern := splitWorkspacePattern(mods, pattern)
		if moduleDir == skipPatternDir {
			// Unmatched satellite parent-prefix ("./cmd/...", "./adapters/...") —
			// drop it so it match-zeroes (see splitWorkspacePattern).
			continue
		}
		add(filepath.Join(root, moduleDir), modulePattern)
		if moduleDir == "." {
			// No member owns the pattern: use the member-relative form for the
			// root-anchored ModeWorkspace load.
			rootPatterns = append(rootPatterns, modulePattern)
		} else {
			// Into-member relative pattern resolves from the root in workspace mode.
			rootPatterns = append(rootPatterns, pattern)
		}
	}
	return groups, rootPatterns, true
}

// splitWorkspacePattern maps a root-relative pattern to (owning member dir,
// member-relative pattern). Multi-member satellite prefixes ("./cmd/...",
// "./adapters/...", "./examples/...") are expanded to their members by
// workspacePatternGroups BEFORE split is called (expandParentPrefix), so they are
// actually scanned, never silently skipped (F5, SATELLITE-PARENT-PREFIX-SCAN-01).
func splitWorkspacePattern(mods []workspace.Module, pattern string) (string, string) {
	// "." / "./..." referred to the pre-#1565 ROOT module — the platform CORE
	// (kernel/runtime/pkg at the repo root). The core moved to ./framework, so map
	// the bare-root patterns onto the framework member, preserving "scan the
	// platform core" semantics.
	if (pattern == "." || pattern == "./...") && hasFrameworkMember(mods) {
		return frameworkModuleDir, pattern
	}
	if dir, modulePattern, ok := matchWorkspaceMember(mods, pattern); ok {
		return dir, modulePattern
	}
	// No member matched a root-relative parent prefix. Reaching here means the prefix
	// has NO member under it at all — a genuine empty match (e.g. a typo
	// "./nonexistent/..."). Signal SKIP: workspacePatternGroups drops the pattern
	// entirely (→ match-zero). A member that matched but whose subdir is missing took
	// the matched branch above and is NOT skipped — it loads and surfaces a
	// packages.Error, preserving typo diagnostics. Only skip when a framework core
	// member exists (the real post-#1565 workspace); a synthetic single-module set
	// falls through to the root-anchored (".") form so its in-module subtrees load.
	if strings.HasPrefix(pattern, "./") && hasFrameworkMember(mods) {
		return skipPatternDir, ""
	}
	return ".", pattern
}

// hasFrameworkMember reports whether the workspace contains the core framework
// module (the post-#1565 successor to the root "." module).
func hasFrameworkMember(mods []workspace.Module) bool {
	for _, m := range mods {
		if filepath.ToSlash(filepath.Clean(m.Dir)) == frameworkModuleDir {
			return true
		}
	}
	return false
}

// matchWorkspaceMember finds the workspace member that owns pattern (longest dir /
// import-path prefix wins), returning (member-dir, member-relative pattern, true).
// ok is false when no member owns the pattern.
func matchWorkspaceMember(mods []workspace.Module, pattern string) (string, string, bool) {
	bestDir, bestPattern, bestScore := "", "", -1
	consider := func(score int, dir, modulePattern string) {
		if score > bestScore {
			bestScore, bestDir, bestPattern = score, dir, modulePattern
		}
	}
	for _, m := range mods {
		dir := filepath.ToSlash(filepath.Clean(m.Dir))
		if dir == "." || dir == "" {
			continue
		}
		prefix := "./" + dir
		switch {
		case pattern == prefix:
			consider(len(dir), dir, ".")
		case strings.HasPrefix(pattern, prefix+"/"):
			consider(len(dir), dir, "."+strings.TrimPrefix(pattern, prefix))
		}
		if pattern == m.ImportPath || strings.HasPrefix(pattern, m.ImportPath+"/") {
			consider(len(m.ImportPath), dir, pattern)
		}
	}
	return bestDir, bestPattern, bestScore >= 0
}

// expandParentPrefix expands a root-relative parent-prefix package pattern that
// spans MULTIPLE workspace members — e.g. "./cmd/...", "./adapters/...",
// "./examples/..." — into one "./<member-dir>/..." pattern per member living
// strictly under that prefix dir (cmd/ holds cmd/gocell + cmd/corebundle;
// adapters/ holds adapters/postgres, adapters/redis, …).
//
// Such a prefix has no single owning module, so post-#1565 (no root module to
// anchor "./cmd/...") it cannot be loaded as written: a ModeModule load errors
// ("directory prefix cmd does not contain main module") and a workspace loader
// that merely match-zeroes it SILENTLY DROPS the satellite coverage that
// governance / security archtest rules declare over ./cmd/… ./adapters/… and
// ./examples/…. Expanding to the real members restores that coverage — each
// "./<member>/..." resolves as a normal workspace member in ModeWorkspace.
//
// Returns (expansions, true) iff pattern is "./<dir>/..." and ≥1 member lives
// strictly under <dir>. Returns (nil, false) for everything else and the caller
// keeps the pattern verbatim: a member owning <dir> exactly (normal single-member
// resolution handles it), no member under <dir> (a genuine match-zero), a
// single-module fixture (its sole "." member owns nothing under a subdir), or a
// non-recursive pattern. Expansions are sorted for deterministic load order.
func expandParentPrefix(mods []workspace.Module, pattern string) ([]string, bool) {
	if !strings.HasPrefix(pattern, "./") || !strings.HasSuffix(pattern, "/...") {
		return nil, false
	}
	dir := strings.TrimSuffix(strings.TrimPrefix(pattern, "./"), "/...")
	if dir == "" || dir == "." {
		return nil, false
	}
	prefix := dir + "/"
	var out []string
	for _, m := range mods {
		md := filepath.ToSlash(filepath.Clean(m.Dir))
		if md == dir {
			// A single member owns the prefix exactly → not a multi-member parent
			// prefix; the caller's normal single-member resolution handles it.
			return nil, false
		}
		if strings.HasPrefix(md, prefix) {
			out = append(out, "./"+md+"/...")
		}
	}
	if len(out) == 0 {
		return nil, false
	}
	sort.Strings(out)
	return out, true
}

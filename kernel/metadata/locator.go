// Package metadata: Locator abstracts metadata YAML discovery from parser
// internals. It is the single funnel through which all path-prefix knowledge
// about GoCell's filesystem topology must flow.
//
// Two modes:
//   - Conventional — walks the hardcoded 5-pattern layout plus 2
//     workspace-level singletons (actors.yaml, journeys/status-board.yaml)
//     (cells/*/cell.yaml, cells/*/slices/*/slice.yaml,
//     contracts/{kind}/.../contract.yaml, journeys/J-*.yaml,
//     assemblies/*/assembly.yaml). examples/ subtree
//     follows the same patterns with one extra prefix.
//   - Manifest — reads .gocell/manifest.yaml and walks the modules:
//     entries it declares. Single-module entry covers Operator-SDK external
//     repo; multi-module entries cover Workspace mode aggregation.
//
// Auto-detection: NewLocator without WithLocatorMode probes the root for
// .gocell/manifest.yaml; presence selects Manifest mode, absence selects
// Conventional. Override with WithLocatorMode(LocatorConventional) for CI
// determinism.
//
// ref: bufbuild/buf buf.yaml v2 modules: schema — single manifest expressing
// both single-module and workspace-aggregate forms.
// ref: kubernetes-sigs/kubebuilder PROJECT — manifest-driven resource
// registration.
// ref: kubernetes-sigs/kustomize kustomization.yaml resources: field.
//
// Funnel: LOCATOR-DISCOVERY-FUNNEL-01 (Hard downstream, Medium upstream).
// All path-prefix string comparisons against {cells/, contracts/, journeys/,
// assemblies/, examples/} must live inside locator_conventional.go or
// locator_manifest.go. fs.WalkDir / filepath.Walk / fs.ReadDir callsites in
// kernel/metadata/** must reside inside Locator.discoverConventional or
// Locator.discoverManifest. The upstream Medium ceiling is the Go language
// limit: cross-package sealed-interface-with-private-constructor is
// structurally unreachable — see SPAN-SETATTR-HOLDER-SEAL-01 (#851) won't-do
// for the precedent.
package metadata

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// SourceKind classifies a metadata YAML file by its semantic bucket.
type SourceKind int

const (
	// SourceUnknown is the zero value; never emitted by Locator.
	SourceUnknown SourceKind = iota
	// SourceCell marks a cell.yaml file.
	SourceCell
	// SourceSlice marks a slice.yaml file.
	SourceSlice
	// SourceContract marks a contract.yaml file.
	SourceContract
	// SourceJourney marks a journeys/J-*.yaml file.
	SourceJourney
	// SourceAssembly marks an assembly.yaml file.
	SourceAssembly
	// SourceActors marks the workspace-level actors.yaml singleton.
	SourceActors
	// SourceStatusBoard marks the workspace-level journeys/status-board.yaml
	// singleton.
	SourceStatusBoard
)

// String returns the kebab-case name of the SourceKind, used in errors and
// diagnostics.
func (k SourceKind) String() string {
	switch k {
	case SourceCell:
		return "cell"
	case SourceSlice:
		return "slice"
	case SourceContract:
		return "contract"
	case SourceJourney:
		return "journey"
	case SourceAssembly:
		return "assembly"
	case SourceActors:
		return "actors"
	case SourceStatusBoard:
		return "status-board"
	default:
		return "unknown"
	}
}

// MetadataSource is one YAML file discovered by Locator.
//
// Path is forward-slash relative to the locator root.
//
// CellID is populated for SourceCell / SourceSlice when the locator can derive
// it from layout (conventional walk path parts, or manifest includes that
// happen to follow conventional layout). When the locator cannot derive
// CellID (manifest with non-conventional layout), CellID stays empty —
// parser will then require slice.yaml to declare belongsToCell explicitly.
//
// To avoid empty CellID in manifest mode, callers have two options:
//
//	(a) Read the `id` field from the corresponding cell.yaml (already parsed
//	    into ProjectMeta.Cells by Parser — use ProjectMeta.CellForSlice to
//	    derive it after a full parse pass).
//	(b) Declare `belongsToCell` explicitly in each slice.yaml; Parser will
//	    propagate that value and governance rules will validate consistency.
type MetadataSource struct {
	Path   string
	Kind   SourceKind
	CellID string
}

// LocatorMode selects how Locator discovers metadata.
type LocatorMode int

const (
	// LocatorAuto selects Conventional unless .gocell/manifest.yaml exists at
	// root, in which case Manifest mode is used. If the manifest file is found
	// but fails to parse, Locator returns an error and does NOT fall back to
	// Conventional mode. Use WithLocatorMode(LocatorConventional) to bypass
	// manifest detection explicitly.
	LocatorAuto LocatorMode = iota
	// LocatorConventional walks the hardcoded 5-pattern layout. Used when no
	// manifest file is present, or explicitly via WithLocatorMode.
	LocatorConventional
	// LocatorManifest reads .gocell/manifest.yaml and walks the modules:
	// list it declares.
	LocatorManifest
)

// String returns the kebab-case name of LocatorMode.
func (m LocatorMode) String() string {
	switch m {
	case LocatorAuto:
		return "auto"
	case LocatorConventional:
		return "conventional"
	case LocatorManifest:
		return "manifest"
	default:
		return "unknown"
	}
}

// ParseLocatorMode converts a CLI flag value to a LocatorMode. Empty string
// resolves to LocatorAuto. Unknown values return an error.
func ParseLocatorMode(s string) (LocatorMode, error) {
	switch s {
	case "", "auto":
		return LocatorAuto, nil
	case "conventional":
		return LocatorConventional, nil
	case "manifest":
		return LocatorManifest, nil
	default:
		return 0, fmt.Errorf("metadata: unknown locator mode %q (want: auto|conventional|manifest)", s)
	}
}

// DefaultManifestPath is the forward-slash path Locator probes during auto
// mode and reads during Manifest mode unless WithManifestPath overrides it.
const DefaultManifestPath = ".gocell/manifest.yaml"

// LocatorOption configures NewLocator / NewLocatorFS.
type LocatorOption func(*Locator)

// WithLocatorMode overrides the auto-detected mode. Use this in CI to lock
// the locator behavior against accidental manifest detection.
func WithLocatorMode(m LocatorMode) LocatorOption {
	return func(l *Locator) { l.requestedMode = m }
}

// WithManifestPath sets an explicit manifest path. Defaults to
// .gocell/manifest.yaml.
//
// Passing an empty string is a no-op (default path unchanged).
// Non-empty paths containing ".." segments or absolute paths cause
// NewLocator / NewLocatorFS to return an error.
func WithManifestPath(p string) LocatorOption {
	return func(l *Locator) {
		if p != "" {
			l.manifestPath = p
		}
	}
}

// Locator discovers metadata YAML files from a filesystem root and emits one
// MetadataSource per file, sorted by path for deterministic test fixtures.
type Locator struct {
	fsys          fs.FS
	osRoot        *os.Root // non-nil for disk-backed (NewLocator); nil for fs.FS-backed (NewLocatorFS)
	root          string   // on-disk root for diagnostics; "" for in-memory fsys
	requestedMode LocatorMode
	resolvedMode  LocatorMode
	manifestPath  string
	manifestSpec  *ManifestSpec // nil when resolved mode is Conventional
}

// NewLocator constructs a Locator backed by the on-disk root directory.
//
// The filesystem is rooted via os.OpenRoot(root).FS(), which confines EVERY
// access (Stat / WalkDir / ReadFile) to the directory tree at root. Per the
// os.Root contract, it FOLLOWS symlinks that resolve within root and REJECTS
// those that escape it (absolute targets, or `..`/symlink chains leaving root)
// with "path escapes from parent" at the syscall layer. This is the
// root-confinement that prevents a manifest module path (or any subpath) that
// is a symlink escaping the workspace from making discovery read cells/contracts
// outside the repo (#1592). Discovery additionally skips symlink *entries*
// during WalkDir (see discoverConventional / matchManifestGlob), so in practice
// no symlink is traversed; the GoCell layout has no symlinks at all, so neither
// behavior changes discovery output.
//
// The root path itself may be a symlinked directory (e.g. macOS /var ->
// /private/var); os.OpenRoot follows the root's own symlink to open it, then
// confines all SUBSEQUENT accesses within the opened directory. Resolution uses
// filepath.Abs (not EvalSymlinks) — confinement comes from os.Root, not from
// pre-resolving root.
//
// Callers must Close the returned Locator to release the underlying directory
// file descriptor (Parser.Parse does this for the Locator it constructs).
func NewLocator(root string, opts ...LocatorOption) (*Locator, error) {
	if root == "" {
		return nil, errors.New("metadata: NewLocator requires non-empty root")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("metadata: NewLocator resolve abs root: %w", err)
	}
	osRoot, err := os.OpenRoot(abs)
	if err != nil {
		return nil, fmt.Errorf("metadata: NewLocator open root: %w", err)
	}
	l := &Locator{
		fsys:         osRoot.FS(),
		osRoot:       osRoot,
		root:         abs,
		manifestPath: DefaultManifestPath,
	}
	for _, opt := range opts {
		opt(l)
	}
	if err := validateManifestRelativePath(l.manifestPath); err != nil {
		_ = l.Close()
		return nil, fmt.Errorf("metadata: NewLocator manifest path: %w", err)
	}
	if err := l.resolveMode(); err != nil {
		_ = l.Close()
		return nil, err
	}
	return l, nil
}

// Close releases the os.Root directory handle held by a disk-backed Locator
// (NewLocator). It is a no-op for an fs.FS-backed Locator (NewLocatorFS) and is
// safe to call more than once. Parser.Parse / ParseFS close the Locator they
// construct; callers that use NewLocator directly should defer Close to release
// the underlying directory file descriptor.
//
// Close errors are non-retryable (closing a read-only directory fd), so
// deferred callers may safely ignore them: `defer func() { _ = loc.Close() }()`.
func (l *Locator) Close() error {
	if l.osRoot == nil {
		return nil
	}
	r := l.osRoot
	l.osRoot = nil
	return r.Close()
}

// NewLocatorFS constructs a Locator backed by an arbitrary fs.FS. Used by tests
// to feed fstest.MapFS fixtures and by callers that already hold an fs.FS handle
// (e.g., embed.FS, virtual FS).
//
// Unlike NewLocator, NewLocatorFS does NOT add root confinement: it uses the
// caller's fsys verbatim. If a caller passes os.DirFS(dir) (which follows
// symlinks), symlink-escape confinement is the CALLER's responsibility — either
// use NewLocator (os.OpenRoot-confined) for untrusted disk roots, or guarantee
// safety another way (conventional Discover never recurses into symlink entries;
// tools/workspace additionally symlink-guards its go.work `use` dirs). MapFS /
// embed.FS have no OS symlinks, so the test/virtual path is unaffected.
func NewLocatorFS(fsys fs.FS, opts ...LocatorOption) (*Locator, error) {
	if fsys == nil {
		return nil, errors.New("metadata: NewLocatorFS requires non-nil fsys")
	}
	l := &Locator{
		fsys:         fsys,
		manifestPath: DefaultManifestPath,
	}
	for _, opt := range opts {
		opt(l)
	}
	if err := validateManifestRelativePath(l.manifestPath); err != nil {
		return nil, fmt.Errorf("metadata: NewLocatorFS manifest path: %w", err)
	}
	if err := l.resolveMode(); err != nil {
		return nil, err
	}
	return l, nil
}

// validateManifestRelativePath rejects absolute paths and paths with ".."
// segments. It uses the same rules as validateManifestModulePath in
// locator_manifest.go so the two share the same security boundary.
func validateManifestRelativePath(p string) error {
	if filepath.IsAbs(p) {
		return fmt.Errorf("absolute path not allowed: %s", p)
	}
	for _, seg := range strings.Split(filepath.ToSlash(p), "/") {
		if seg == ".." {
			return fmt.Errorf("path escape not allowed: %s contains parent reference", p)
		}
	}
	return nil
}

// Mode returns the resolved locator mode (after auto-detection).
func (l *Locator) Mode() LocatorMode { return l.resolvedMode }

// FS returns the underlying fs.FS so callers that need to read file contents
// (parser unmarshalFile) can share the same handle.
func (l *Locator) FS() fs.FS { return l.fsys }

// Root returns the absolute disk root used by the locator, or empty string
// when the locator was constructed via NewLocatorFS.
func (l *Locator) Root() string { return l.root }

// IsInExamplesSubtree reports whether p is a YAML / Go file under the GoCell
// monorepo's examples/ subtree. This is a conventional-layout query
// exposed here (rather than open-coded in governance rules) so that the
// path-prefix literal "examples/" stays inside the Locator funnel —
// see LOCATOR-DISCOVERY-FUNNEL-01. External-repo callers under Manifest
// mode without an examples/ subtree always get false; this function is
// intended for gocell-self-repo skip-style rules.
func IsInExamplesSubtree(p string) bool {
	return strings.HasPrefix(filepath.ToSlash(p), "examples/")
}

// IsConventionalAssemblyPath reports whether p is an assembly YAML file under
// the conventional assemblies/<id>/ directory (e.g.
// "assemblies/corebundle/assembly.yaml"). Exposed here so the "assemblies/"
// path-prefix literal stays inside the Locator funnel —
// see LOCATOR-DISCOVERY-FUNNEL-01.A2a.
//
// Returns false for examples/, Manifest-mode custom paths, and an empty
// string. Callers (e.g. deriveAssembly) check for empty file before calling
// and apply convention-default behavior for that case.
//
// Intended for internal derivation logic only (deriveAssembly entrypoint
// heuristic); governance rules should consume AssemblyMeta.File via
// path.Dir instead of calling this directly.
func IsConventionalAssemblyPath(p string) bool {
	return strings.HasPrefix(filepath.ToSlash(p), "assemblies/")
}

// resolveMode applies the requested mode (or auto-detection) and pre-loads
// the manifest if Manifest mode is selected. Emits a structured slog.Info
// once the mode is decided so CI logs make the active layout explicit (CI
// debugging value when external-repo runs silently switch into manifest
// mode); emits slog.Error before returning a manifest-load failure so
// operators get a structured record alongside the wrapped error.
func (l *Locator) resolveMode() error {
	switch l.requestedMode {
	case LocatorConventional:
		l.resolvedMode = LocatorConventional
		l.logResolved()
		return nil
	case LocatorManifest:
		spec, err := loadManifest(l.fsys, l.manifestPath)
		if err != nil {
			slog.Error("metadata: locator manifest load failed",
				slog.String("root", l.root),
				slog.String("manifest_path", l.manifestPath),
				slog.String("requested_mode", l.requestedMode.String()),
				slog.Any("err", err))
			return fmt.Errorf("metadata: locator: explicit manifest mode but manifest load failed: %w", err)
		}
		l.manifestSpec = spec
		l.resolvedMode = LocatorManifest
		l.logResolved()
		return nil
	case LocatorAuto:
		// Probe for manifest presence.
		if _, err := fs.Stat(l.fsys, l.manifestPath); err == nil {
			spec, lerr := loadManifest(l.fsys, l.manifestPath)
			if lerr != nil {
				slog.Error("metadata: locator manifest load failed (auto-detected)",
					slog.String("root", l.root),
					slog.String("manifest_path", l.manifestPath),
					slog.String("requested_mode", l.requestedMode.String()),
					slog.Any("err", lerr))
				return fmt.Errorf("metadata: locator: manifest %s detected but load failed: %w", l.manifestPath, lerr)
			}
			l.manifestSpec = spec
			l.resolvedMode = LocatorManifest
			l.logResolved()
			return nil
		} else if !errors.Is(err, fs.ErrNotExist) {
			slog.Error("metadata: locator manifest probe failed",
				slog.String("root", l.root),
				slog.String("manifest_path", l.manifestPath),
				slog.String("requested_mode", l.requestedMode.String()),
				slog.Any("err", err))
			return fmt.Errorf("metadata: locator: probe manifest %s: %w", l.manifestPath, err)
		}
		l.resolvedMode = LocatorConventional
		l.logResolved()
		return nil
	default:
		return fmt.Errorf("metadata: locator: invalid mode %d", l.requestedMode)
	}
}

// logResolved emits a single structured slog.Info noting the active
// locator mode. Aligns with the observability rule "Info: 生命周期" —
// surfaces the layout decision in CI logs so external-repo operators
// can confirm whether manifest detection succeeded.
func (l *Locator) logResolved() {
	modules := 0
	if l.manifestSpec != nil {
		modules = len(l.manifestSpec.Modules)
	}
	slog.Info("metadata: locator mode resolved",
		slog.String("mode", l.resolvedMode.String()),
		slog.String("root", l.root),
		slog.String("manifest_path", l.manifestPath),
		slog.Int("manifest_modules", modules),
		slog.String("requested_mode", l.requestedMode.String()))
}

// Discover walks the locator root and emits one MetadataSource per metadata
// YAML file found, sorted by Path for deterministic output.
func (l *Locator) Discover() ([]MetadataSource, error) {
	var sources []MetadataSource
	var err error
	switch l.resolvedMode {
	case LocatorConventional:
		sources, err = l.discoverConventional()
	case LocatorManifest:
		sources, err = l.discoverManifest()
	default:
		return nil, fmt.Errorf("metadata: locator: unresolved mode %d", l.resolvedMode)
	}
	if err != nil {
		return nil, err
	}
	sort.Slice(sources, func(i, j int) bool { return sources[i].Path < sources[j].Path })
	return sources, nil
}

package governance

// rules_misc_advisory.go consolidates five rule clusters whose home was
// historically a single-rule-per-file layout and who collectively cover the
// "advisory / cell-state guard" theme:
//
//   - ADV-01..06 (formerly rules_advisory.go) — journey/status-board /
//     waiver / event-subscriber drift advisories.
//   - OUTGUARD-01 (formerly rules_outbox.go) — L2+ cells must declare a
//     durabilityMode.
//   - SLICE-CONSISTENCY-01 (formerly rules_slice.go) — slice level ≤
//     parent cell level.
//   - FMT-19 implementation (formerly rules_wrapper.go) — kernel/wrapper
//     package-state scanner. The strict-mode orchestrator entry lives in
//     rules_misc_strict.go and calls validateFMT19 cross-file.
//   - DOC-NAME-01 implementation (formerly rules_docs.go) — active doc
//     legacy-literal scanner. Same strict-mode cross-file pattern as FMT-19.

import (
	"bufio"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/ghbvf/gocell/kernel/cellvocab"
	"github.com/ghbvf/gocell/kernel/metadata"
)

// =============================================================================
// ADV-01..06 (formerly rules_advisory.go)
// =============================================================================

// validateADV01 checks that every platform journey has a corresponding entry
// in the status board. Journeys under examples/ are exempt — example
// projects manage their own readiness tracking and should not pollute the
// platform status-board.yaml (consistent with CONTRACT-CONSISTENCY-EMIT-01
// and JOURNEY-CONTRACT-EXISTENCE-01 examples-exemption posture).
func (v *Validator) validateADV01() []ValidationResult {
	var results []ValidationResult

	// Build a set of journey IDs present in the status board.
	sbJourneys := make(map[string]bool, len(v.project.StatusBoard))
	for _, entry := range v.project.StatusBoard {
		sbJourneys[entry.JourneyID] = true
	}

	for _, j := range v.project.Journeys {
		if metadata.IsExamplePath(journeyFile(j)) {
			continue
		}
		if !sbJourneys[j.ID] {
			results = append(results, v.newWarning(
				codeADV01, IssueRefNotFound,
				journeyFile(j),
				"id",
				fmt.Sprintf("journey %q has no entry in status-board.yaml", j.ID),
				"add an entry for this journey to journeys/status-board.yaml, or remove the journey if it is retired",
			))
		}
	}
	return results
}

// validateADV03 checks that waivers reference contracts that appear in the slice's contractUsages.
func (v *Validator) validateADV03() []ValidationResult {
	var results []ValidationResult
	for _, s := range v.project.Slices {
		// Build set of contracts used by this slice.
		usedContracts := make(map[string]bool, len(s.ContractUsages))
		for _, cu := range s.ContractUsages {
			usedContracts[cu.Contract] = true
		}
		for i, w := range s.Verify.Waivers {
			if w.Contract != "" && !usedContracts[w.Contract] {
				results = append(results, v.newWarning(
					codeADV03, IssueRefNotFound,
					sliceFile(s),
					fmt.Sprintf("verify.waivers[%d].contract", i),
					fmt.Sprintf("waiver for contract %q has no matching contractUsage in slice %q", w.Contract, s.ID),
					"add a contractUsage for this contract to the slice, or remove the stale waiver entry",
				))
			}
		}
	}
	return results
}

// validateADV04 checks that status-board entries reference existing journeys.
// status-board.yaml's root is a YAML sequence (no "entries" wrapper), so the
// field path uses the locator's root-index form "[i].journeyId".
func (v *Validator) validateADV04() []ValidationResult {
	var results []ValidationResult
	for i, entry := range v.project.StatusBoard {
		if _, ok := v.project.Journeys[entry.JourneyID]; !ok {
			results = append(results, v.newWarning(
				codeADV04, IssueRefNotFound,
				"journeys/status-board.yaml",
				fmt.Sprintf("[%d].journeyId", i),
				fmt.Sprintf("status-board entry references unknown journey %q", entry.JourneyID),
				"create the referenced journey under journeys/, or remove this status-board entry",
			))
		}
	}
	return results
}

// validateADV05 checks that active event contracts have at least one subscriber.
// An event contract with lifecycle "active" and an empty (or nil) subscribers list
// is a dead event — it will never be consumed and its producer is publishing to
// no one. The contract must either be wired with subscribers or moved out of active.
//
// Only lifecycle "active" is checked. Draft contracts are allowed to be unwired
// during the design phase — subscribers are not required until the contract
// transitions to active. Deprecated contracts are on their way out and are also
// exempt. Non-event contracts (http, command, projection) are not checked.
func (v *Validator) validateADV05() []ValidationResult {
	var results []ValidationResult
	for _, c := range v.project.Contracts {
		if c.Kind != "event" {
			continue
		}
		if c.Lifecycle != "active" {
			continue
		}
		if len(c.Endpoints.Subscribers) == 0 {
			// Field anchors at lifecycle, a real contract.yaml field that locates:
			// endpoints.subscribers is a DERIVED field (yaml:"-", hand-write
			// forbidden), so pointing there yields no position and misleads the
			// user toward a field they cannot edit. The remediation (add a
			// subscribing slice / actorSubscribers / deprecate) lives in the
			// message + fix; lifecycle is the locatable anchor (and the deprecate
			// path's target).
			results = append(results, v.newWarning(
				codeADV05, IssueForbidden,
				contractFile(c),
				"lifecycle",
				fmt.Sprintf(advHintADV05EmptySubscribers, c.ID),
				advHintADV05EmptySubscribersFix,
			))
		}
	}
	return results
}

// =============================================================================
// OUTGUARD-01 (formerly rules_outbox.go)
// =============================================================================

// validateOUTGUARD01 checks that L2+ cells declare a durabilityMode in their
// cell.yaml. L2+ cells use the transactional outbox pattern and must
// explicitly declare "demo" or "durable"; runtime CheckNotNoop then rejects
// noop outbox implementations when an assembly resolves to DurabilityDurable.
// L0/L1 cells may omit the field (advisory only); when present its value is
// still validated so a typo cannot slip through.
//
// durabilityMode is advisory cell.yaml metadata consumed by governance and
// the devtools catalog; it is not threaded into the runtime BaseCell path.
// The cell.yaml ↔ runtime DurabilityMode Init-time reconciliation is not yet
// enforced at runtime.
//
// ref: K8s apimachinery validation — required field checks
// ref: kernel/cell/durability.go — DurabilityMode, CheckNotNoop
func (v *Validator) validateOUTGUARD01() []ValidationResult {
	var results []ValidationResult
	for _, c := range v.project.Cells {
		if !isL2OrHigher(c.ConsistencyLevel) {
			// L0/L1 may omit durabilityMode (advisory only);
			// only validate value when explicitly set.
			if c.DurabilityMode != "" && !isValidDurabilityMode(c.DurabilityMode) {
				results = append(results, v.newError(
					codeOUTGUARD01, IssueInvalid,
					cellFile(c),
					"durabilityMode",
					fmt.Sprintf(
						"cell %q has invalid durabilityMode %q; must be \"demo\" or \"durable\"",
						c.ID, c.DurabilityMode,
					),
					"set durabilityMode to demo or durable in the cell.yaml "+durabilityModeHintSuffix,
				))
			}
			continue
		}
		if c.DurabilityMode == "" {
			results = append(results, v.newError(
				codeOUTGUARD01, IssueRequired,
				cellFile(c),
				"durabilityMode",
				fmt.Sprintf(
					"cell %q declares %s consistency but has no durabilityMode; "+
						"L2+ cells must declare durabilityMode: demo or durable",
					c.ID, c.ConsistencyLevel,
				),
				"add durabilityMode: demo or durabilityMode: durable to the cell.yaml "+durabilityModeHintSuffix,
			))
			continue
		}
		if !isValidDurabilityMode(c.DurabilityMode) {
			results = append(results, v.newError(
				codeOUTGUARD01, IssueInvalid,
				cellFile(c),
				"durabilityMode",
				fmt.Sprintf(
					"cell %q has invalid durabilityMode %q; must be \"demo\" or \"durable\"",
					c.ID, c.DurabilityMode,
				),
				"set durabilityMode to demo or durable in the cell.yaml "+durabilityModeHintSuffix,
			))
		}
	}
	return results
}

// isValidDurabilityMode returns true for recognized durability mode values.
func isValidDurabilityMode(mode string) bool {
	switch mode {
	case "demo", "durable":
		return true
	default:
		return false
	}
}

// isL2OrHigher returns true if the consistency level string is L2, L3, or L4.
func isL2OrHigher(level string) bool {
	switch level {
	case "L2", "L3", "L4":
		return true
	default:
		return false
	}
}

// =============================================================================
// SLICE-CONSISTENCY-02 — contractUsages role → minimum consistencyLevel
// =============================================================================

// validateSliceConsistencyContractUsages enforces the lower-bound side of the
// slice consistency contract: when a slice declares any contractUsage with
// role=publish, the effective consistency level must be L2 or higher (the
// L2 OutboxFact invariant requires durable atomic publication).
//
// Pair with SLICE-CONSISTENCY-01, which enforces the upper bound (slice ≤ cell).
// SLICE-CONSISTENCY-02 is metadata-only — codegen funnel + parser strict mode
// project slice.yaml.consistencyLevel into Go (sliceMeta literal), so a
// publish role + L0/L1 declaration is the only remaining drift class this
// rule catches.
//
// AI-robust evaluation: Medium. Role values are kernel/cellvocab.ContractRole
// const enum; the rule's string comparison is const-equivalent. New slices
// with role=publish auto-enroll.
//
// Other roles (serve / call / subscribe / handle / provide / read / invoke)
// do NOT enforce a lower bound — empirically valid forms span L0..L3 (e.g.
// auditcore subscribes user events at L2; configreceive subscribes at L3).
// SLICE-CONSISTENCY-02 keeps narrow truth: only publish is durably bound.
func (v *Validator) validateSliceConsistencyContractUsages() []ValidationResult {
	var results []ValidationResult
	for _, s := range v.project.Slices {
		if !hasPublishRole(s.ContractUsages) {
			continue
		}
		// The parser now rejects empty ConsistencyLevel for real YAML files.
		// In-memory ProjectMeta fixtures (e.g. governance unit tests) may
		// reach this point without a parser — report an error so they don't
		// silently bypass the publish lower-bound check.
		if s.ConsistencyLevel == "" {
			results = append(results, v.newError(
				codeSLICECONSISTENCY02, IssueInvalid,
				sliceFile(s),
				"consistencyLevel",
				fmt.Sprintf(
					"slice %q has role=publish but consistencyLevel is empty; "+
						"in-memory ProjectMeta must declare consistencyLevel for governance check",
					s.ID,
				),
				"set consistencyLevel to L2|L3|L4",
			))
			continue
		}
		level, err := cellvocab.ParseLevel(s.ConsistencyLevel)
		if err != nil {
			// Invalid level is already flagged by SLICE-CONSISTENCY-01.
			continue
		}
		if level < cellvocab.L2 {
			results = append(results, v.newError(
				codeSLICECONSISTENCY02, IssueInvalid,
				sliceFile(s),
				"consistencyLevel",
				fmt.Sprintf(
					"slice %q declares contractUsages with role=publish but consistencyLevel=%q; "+
						"publishing events requires the L2 OutboxFact invariant (transactional outbox)",
					s.ID, s.ConsistencyLevel,
				),
				"raise consistencyLevel to L2|L3|L4",
			))
		}
	}
	return results
}

// hasPublishRole reports whether any contractUsage in usages declares
// role=publish (cellvocab.RolePublish).
func hasPublishRole(usages []metadata.ContractUsage) bool {
	for _, u := range usages {
		if u.Role == string(cellvocab.RolePublish) {
			return true
		}
	}
	return false
}

// =============================================================================
// SLICE-CONSISTENCY-01 (formerly rules_slice.go)
// =============================================================================

// validateSliceConsistency checks that if a slice declares an explicit
// consistencyLevel, it must be ≤ the parent cell's consistencyLevel
// (a slice can downgrade but never upgrade beyond its cell's contract).
//
// Rule: SLICE-CONSISTENCY-01
// Rationale: slice.consistencyLevel allows a cell to host slices with weaker
// guarantees (e.g., a L2 cell with an L1 slice that doesn't emit events);
// upgrading would silently break the cell-level contract.
//
// Pair with SLICE-CONSISTENCY-02, which enforces the lower bound (publish role → ≥L2).
func (v *Validator) validateSliceConsistency() []ValidationResult {
	var results []ValidationResult
	for _, s := range v.project.Slices {
		if s.ConsistencyLevel == "" {
			// Parser rejects empty consistencyLevel; this branch handles in-memory
			// ProjectMeta fixtures constructed without a parser (e.g., governance unit tests).
			continue
		}
		sliceLevel, err := cellvocab.ParseLevel(s.ConsistencyLevel)
		if err != nil {
			results = append(results, v.newError(
				codeSLICECONSISTENCY01, IssueInvalid,
				sliceFile(s),
				"consistencyLevel",
				fmt.Sprintf(
					"slice %q declares consistencyLevel %q which is not valid (must be L0-L4)",
					s.ID, s.ConsistencyLevel,
				),
				"set consistencyLevel to L0, L1, L2, L3, or L4",
			))
			continue
		}
		parentCell, ok := v.project.Cells[s.BelongsToCell]
		if !ok {
			// REF-01 already catches missing parent cell; skip here
			continue
		}
		cellLevel, err := cellvocab.ParseLevel(parentCell.ConsistencyLevel)
		if err != nil {
			// FMT-03 already catches invalid cell consistencyLevel; skip here
			continue
		}
		if sliceLevel > cellLevel {
			results = append(results, v.newError(
				codeSLICECONSISTENCY01, IssueInvalid,
				sliceFile(s),
				"consistencyLevel",
				fmt.Sprintf(
					"slice %q declares consistencyLevel %q which is stronger than parent cell %q (%q); "+
						"a slice can downgrade but not upgrade",
					s.ID, s.ConsistencyLevel, parentCell.ID, parentCell.ConsistencyLevel,
				),
				"set slice consistencyLevel to the cell's level or lower",
			))
		}
	}
	return results
}

// =============================================================================
// FMT-19 implementation (formerly rules_wrapper.go)
// =============================================================================

// FMT-19 WRAPPER-NO-PACKAGE-STATE — enforces that kernel/wrapper/*.go
// contains no mutable package-level variables of interface or pointer
// type. Immutable zero-value sentinels (NoopTracer{}, noopSpan{}) and
// compile-time interface checks (var _ Tracer = NoopTracer{}) are
// allowed; any `var x Tracer` / `var mu sync.Mutex` is rejected. Guards
// the round-4 invariant that kernel/wrapper is a pure value+rules layer.
//
// Strict-only (surface only under ValidateStrict(true)) to avoid
// disrupting the base Validate() path for rapid iteration. Strict-mode
// orchestrator is in rules_misc_strict.go and calls validateFMT19
// cross-file; impl lives here.
//
// Historical sibling FMT-18 (SPEC-CONTRACT-SYNC) was removed in
// PR-V1-CODEGEN-FULL-MIGRATION (W4); the literal-presence half migrated
// to import-graph-level archtest gates (CELLS-NO-WRAPPER-CONTRACTSPEC-
// IMPORT-01 / NO-MANUAL-CONTRACTSPEC-LITERAL-01 / EVENT-SUBSCRIPTION-
// CONTRACTGEN-COVERAGE-01), and the /internal/v1 caller-clients half
// was later reclaimed at the YAML governance layer by FMT-31
// (rules_fmt.go).

// FMT-19 AST rewrite (PR246-FU1 finding ③):
//
//   - Accept rule ①: `var _ Type = expr` (blank-identifier compile-time
//     interface/typecheck — all Names must be '_').
//   - Accept rule ②: `var name [Type] = CompositeLit{}` where the initializer
//     is a composite literal with zero Elts and a plain struct type (identifier
//     or selector expression). Slice/map/chan/pointer composite literals are
//     rejected even when empty — they are reference types.
//   - Reject everything else structurally (no hard-coded type whitelist).
//
// The pre-FU1 line-regex + fmt19KnownValueTypes whitelist missed grouped
// `var (...)` blocks, no-initializer vars, multi-name declarations, and
// mutable container types (map/chan/slice); the AST rewrite closes all
// five evasion classes by scanning the syntax tree directly.
func (v *Validator) validateFMT19() []ValidationResult {
	dir := filepath.Join(v.root, "kernel", "wrapper")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return []ValidationResult{
			v.newError(codeFMT19, IssueInvalid,
				"kernel/wrapper/", "",
				fmt.Sprintf("FMT-19: failed to read kernel/wrapper/: %v", err),
				"ensure the kernel/wrapper directory exists"),
		}
	}

	fset := token.NewFileSet()
	var out []ValidationResult
	for _, entry := range entries {
		if !shouldScanWrapperFile(entry) {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		out = append(out, v.scanWrapperPackageStateFile(fset, path)...)
	}
	return out
}

func shouldScanWrapperFile(entry os.DirEntry) bool {
	name := entry.Name()
	return !entry.IsDir() &&
		strings.HasSuffix(name, ".go") &&
		!strings.HasSuffix(name, "_test.go")
}

func (v *Validator) scanWrapperPackageStateFile(fset *token.FileSet, path string) []ValidationResult {
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		return []ValidationResult{v.newError(codeFMT19, IssueInvalid,
			path, "",
			fmt.Sprintf("FMT-19: failed to parse %s: %v", path, err),
			"fix the Go syntax error in this file")}
	}

	var out []ValidationResult
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			continue
		}
		for _, spec := range gen.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			if reason, forbidden := classifyWrapperVarSpec(vs); forbidden {
				nameList := formatVarSpecNames(vs)
				out = append(out, v.newError(codeFMT19, IssueInvalid,
					path, "",
					fmt.Sprintf("FMT-19: %s:%d forbids package-level var %s — %s "+
						"(kernel/wrapper must stay stateless: round-4 constructor-injection invariant)",
						path, fset.Position(vs.Pos()).Line, nameList, reason),
					"remove the mutable package-level variable and pass state via constructor"))
			}
		}
	}
	return out
}

// classifyWrapperVarSpec returns (violationReason, forbidden). Accept rules:
//
//	① all Names are blank — compile-time interface check, any RHS allowed.
//	② single Name + single Value that, after unwrapping any number of
//	   *ast.ParenExpr layers, is a composite literal with zero Elts on a
//	   plain struct type (identifier or selector expression).
//
// Everything else is forbidden — the kernel/wrapper package may only hold
// blank-ident interface checks and zero-value sentinels.
func classifyWrapperVarSpec(vs *ast.ValueSpec) (string, bool) {
	if allBlank(vs.Names) {
		return "", false
	}
	if len(vs.Names) > 1 {
		return "multi-name declaration forbidden (use separate var blocks or move to constants)", true
	}
	if len(vs.Values) == 0 {
		return "no initializer — implicit zero-value may be a mutable reference (map/chan/slice/interface)", true
	}
	cl, ok := unwrapCompositeLit(vs.Values[0])
	if !ok {
		return "initializer is not a composite literal — only zero-value `Type{}` sentinels allowed at package scope", true
	}
	if len(cl.Elts) > 0 {
		return "initializer is a non-empty composite literal — only zero-value (empty) sentinels allowed", true
	}
	// Reject slice/map/chan/pointer composite literals (still reference types even when empty).
	if !isPlainStructCompositeType(cl.Type) {
		return "initializer is a composite of a reference/container type — only plain struct zero-value sentinels allowed", true
	}
	return "", false
}

// unwrapCompositeLit strips any number of *ast.ParenExpr layers around expr
// and returns the inner *ast.CompositeLit if found. Returns (nil, false)
// for any other expression shape (idents, calls, unary expressions like
// `&T{}`, function literals).
func unwrapCompositeLit(expr ast.Expr) (*ast.CompositeLit, bool) {
	for {
		paren, ok := expr.(*ast.ParenExpr)
		if !ok {
			break
		}
		expr = paren.X
	}
	cl, ok := expr.(*ast.CompositeLit)
	return cl, ok
}

func allBlank(names []*ast.Ident) bool {
	if len(names) == 0 {
		return false
	}
	for _, n := range names {
		if n.Name != "_" {
			return false
		}
	}
	return true
}

// isPlainStructCompositeType reports whether expr is an ast type that, when
// used as a CompositeLit's Type, names a plain struct (ident or pkg.ident)
// rather than a reference/container type (map, slice, chan, pointer, array).
//
// At top-level VAR specs, CompositeLit.Type is always set by the parser:
// both `var x T = T{}` and `var x = T{}` record the type on the
// CompositeLit. The nil case only arises for nested implicit composite
// literals (e.g. inner `{}` in `[]T{{}, {}}`); those have non-empty Elts
// in the outer literal and never reach this helper. nil is therefore
// rejected defensively rather than accepted — fewer paths, no implicit
// trust in the upstream zero-Elts check.
func isPlainStructCompositeType(expr ast.Expr) bool {
	if expr == nil {
		return false
	}
	switch expr.(type) {
	case *ast.Ident, *ast.SelectorExpr:
		return true
	default:
		return false
	}
}

func formatVarSpecNames(vs *ast.ValueSpec) string {
	if len(vs.Names) == 0 {
		return "<anon>"
	}
	names := make([]string, 0, len(vs.Names))
	for _, n := range vs.Names {
		names = append(names, n.Name)
	}
	return strings.Join(names, ", ")
}

// =============================================================================
// DOC-NAME-01 implementation (formerly rules_docs.go)
// =============================================================================

const (
	// docNamingGuardRelPath is the repo-relative path to the naming-guard config
	// consumed by DOC-NAME-01.
	docNamingGuardRelPath = "docs/architecture/naming-guard.yaml"

	// docNamingMaxFileBytes caps the size of a DOC-NAME-01 scan target. Larger
	// include targets are skipped to bound memory use (the scanner reads the
	// whole file). 10 MiB comfortably covers any real documentation file.
	docNamingMaxFileBytes = 10 << 20

	// durabilityModeHintSuffix is user-facing guidance appended to OUTGUARD-01
	// error messages to steer authors toward the correct durabilityMode value.
	durabilityModeHintSuffix = "(use demo for examples/tests, durable for production assemblies)"
)

type docNamingGuardConfig struct {
	Include      []string               `yaml:"include"`
	Exclude      []string               `yaml:"exclude"`
	Replacements []docNamingReplacement `yaml:"replacements"`
}

type docNamingReplacement struct {
	Literal     string `yaml:"literal"`
	Replacement string `yaml:"replacement"`
}

// validateDOCNAME01 scans active documentation for legacy literals declared in
// docs/architecture/naming-guard.yaml. It is strict-only: non-strict validation
// remains silent so historical documents do not become warnings by accident.
func (v *Validator) validateDOCNAME01() []ValidationResult {
	if v.root == "" {
		return nil
	}

	cfg, ok, results := v.loadDocNamingGuard()
	if !ok || len(results) > 0 {
		return results
	}

	targets, targetResults := v.docNamingTargets(cfg)
	results = append(results, targetResults...)
	if len(results) > 0 {
		return results
	}

	for _, rel := range targets {
		data, err := v.readFile(filepath.Join(v.root, filepath.FromSlash(rel)))
		if err != nil {
			results = append(results, newErrorAt(
				codeDOCNAME01, IssueInvalid,
				rel, metadata.Position{},
				"content",
				fmt.Sprintf(advHintDOCNAME01CannotReadDoc, err),
				advHintDOCNAME01CannotReadDocFix,
			))
			continue
		}
		results = append(results, v.scanDocNamingLiterals(rel, string(data), cfg.Replacements)...)
	}
	return results
}

func (v *Validator) loadDocNamingGuard() (docNamingGuardConfig, bool, []ValidationResult) {
	var cfg docNamingGuardConfig
	data, err := v.readFile(filepath.Join(v.root, filepath.FromSlash(docNamingGuardRelPath)))
	if errors.Is(err, os.ErrNotExist) {
		return cfg, false, []ValidationResult{newErrorAt(
			codeDOCNAME01, IssueRequired,
			docNamingGuardRelPath, metadata.Position{},
			"",
			advHintDOCNAME01GuardRequired,
			advHintDOCNAME01GuardRequiredFix,
		)}
	}
	if err != nil {
		return cfg, false, []ValidationResult{newErrorAt(
			codeDOCNAME01, IssueInvalid,
			docNamingGuardRelPath, metadata.Position{},
			"",
			fmt.Sprintf(advHintDOCNAME01CannotReadGuard, err),
			advHintDOCNAME01CannotReadGuardFix,
		)}
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return cfg, false, []ValidationResult{newErrorAt(
			codeDOCNAME01, IssueInvalid,
			docNamingGuardRelPath, metadata.Position{},
			"",
			fmt.Sprintf(advHintDOCNAME01CannotParseGuard, err),
			advHintDOCNAME01CannotParseGuardFix,
		)}
	}

	var results []ValidationResult
	if len(cfg.Include) == 0 {
		results = append(results, newErrorAt(
			codeDOCNAME01, IssueRequired,
			docNamingGuardRelPath, metadata.Position{},
			"include",
			advHintDOCNAME01MissingInclude,
			advHintDOCNAME01MissingIncludeFix,
		))
	}
	if len(cfg.Replacements) == 0 {
		results = append(results, newErrorAt(
			codeDOCNAME01, IssueRequired,
			docNamingGuardRelPath, metadata.Position{},
			"replacements",
			advHintDOCNAME01MissingReplacements,
			advHintDOCNAME01MissingReplacementsFix,
		))
	}
	for i, repl := range cfg.Replacements {
		if repl.Literal == "" || repl.Replacement == "" {
			results = append(results, newErrorAt(
				codeDOCNAME01, IssueRequired,
				docNamingGuardRelPath, metadata.Position{},
				fmt.Sprintf("replacements[%d]", i),
				advHintDOCNAME01InvalidReplacement,
				advHintDOCNAME01InvalidReplacementFix,
			))
		}
	}
	return cfg, true, results
}

func (v *Validator) docNamingTargets(cfg docNamingGuardConfig) ([]string, []ValidationResult) {
	seen := map[string]struct{}{}
	var results []ValidationResult

	for _, include := range cfg.Include {
		results = append(results, v.collectDocNamingInclude(include, cfg.Exclude, seen)...)
	}

	targets := make([]string, 0, len(seen))
	for rel := range seen {
		targets = append(targets, rel)
	}
	sort.Strings(targets)
	return targets, results
}

func (v *Validator) collectDocNamingInclude(include string, exclude []string, seen map[string]struct{}) []ValidationResult {
	include = strings.TrimSpace(include)
	if include == "" {
		return nil
	}

	includeSlash := filepath.ToSlash(include)
	switch {
	case strings.HasSuffix(includeSlash, "/**"):
		return v.walkDocNamingInclude(includeSlash, exclude, seen)
	case hasGlobMeta(includeSlash):
		return v.globDocNamingInclude(includeSlash, exclude, seen)
	default:
		v.addDocNamingTarget(filepath.Join(v.root, filepath.FromSlash(includeSlash)), exclude, seen)
		return nil
	}
}

func (v *Validator) walkDocNamingInclude(include string, exclude []string, seen map[string]struct{}) []ValidationResult {
	baseRel := strings.TrimSuffix(include, "/**")
	baseAbs := filepath.Join(v.root, filepath.FromSlash(baseRel))
	// Don't walk a tree rooted outside the project (addDocNamingTarget would
	// reject each escaping file anyway, but skipping the walk avoids
	// enumerating directories outside the root).
	if !IsWithinRoot(v.root, baseAbs) {
		return nil
	}
	info, statErr := os.Stat(baseAbs)
	if statErr != nil || !info.IsDir() {
		return nil
	}
	err := filepath.WalkDir(baseAbs, func(abs string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		v.addDocNamingTarget(abs, exclude, seen)
		return nil
	})
	if err == nil {
		return nil
	}
	return []ValidationResult{newErrorAt(
		codeDOCNAME01, IssueInvalid,
		baseRel, metadata.Position{},
		"",
		fmt.Sprintf(advHintDOCNAME01CannotWalk, include, err),
		advHintDOCNAME01CannotWalkFix,
	)}
}

func (v *Validator) globDocNamingInclude(include string, exclude []string, seen map[string]struct{}) []ValidationResult {
	matches, err := filepath.Glob(filepath.Join(v.root, filepath.FromSlash(include)))
	if err != nil {
		return []ValidationResult{newErrorAt(
			codeDOCNAME01, IssueInvalid,
			docNamingGuardRelPath, metadata.Position{},
			"include",
			fmt.Sprintf(advHintDOCNAME01InvalidPattern, include, err),
			advHintDOCNAME01InvalidPatternFix,
		)}
	}
	for _, match := range matches {
		// Defense-in-depth symmetric with walkDocNamingInclude: skip matches
		// outside the root before addDocNamingTarget (which also guards). A glob
		// pattern containing `..` can match parent-dir entries.
		if !IsWithinRoot(v.root, match) {
			continue
		}
		v.addDocNamingTarget(match, exclude, seen)
	}
	return nil
}

func (v *Validator) addDocNamingTarget(abs string, exclude []string, seen map[string]struct{}) {
	// Single choke point for read-eligibility. Reject include targets that
	// escape the project root (a hostile naming-guard.yaml could otherwise read
	// arbitrary files on the CI worker via `include: ../../...`). Mirrors the
	// IsWithinRoot guard already used by resolveCrossFileSchemaRef and rules_ref.go.
	if !IsWithinRoot(v.root, abs) {
		return
	}
	info, err := os.Stat(abs)
	if err != nil || info.IsDir() {
		return
	}
	// Skip oversize targets: scanDocNamingLiterals reads the whole file, so an
	// include pointing at a multi-GB file would exhaust memory (DoS).
	if info.Size() > docNamingMaxFileBytes {
		return
	}
	rel, err := filepath.Rel(v.root, abs)
	if err != nil {
		return
	}
	rel = filepath.ToSlash(rel)
	if docNamingExcluded(rel, exclude) {
		return
	}
	seen[rel] = struct{}{}
}

func (v *Validator) scanDocNamingLiterals(file, content string, replacements []docNamingReplacement) []ValidationResult {
	var results []ValidationResult
	sc := bufio.NewScanner(strings.NewReader(content))
	// Max token aligned with the file-size cap (docNamingMaxFileBytes): a target
	// is already bounded to that size, so a single long line within it must not
	// trip bufio.ErrTooLong and turn a best-effort scan into an IssueInvalid error.
	sc.Buffer(make([]byte, 1024), docNamingMaxFileBytes)

	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := sc.Text()
		for _, repl := range replacements {
			for col := findDocLiteral(line, repl.Literal, 0); col >= 0; col = findDocLiteral(line, repl.Literal, col+len(repl.Literal)) {
				results = append(results, newErrorAt(
					codeDOCNAME01, IssueForbidden,
					file, metadata.Position{Line: lineNo, Column: col + 1},
					"content",
					fmt.Sprintf(advHintDOCNAME01LegacyLiteral, repl.Literal, repl.Replacement),
					advHintDOCNAME01LegacyLiteralFix,
				))
			}
		}
	}
	if err := sc.Err(); err != nil {
		results = append(results, newErrorAt(
			codeDOCNAME01, IssueInvalid,
			file, metadata.Position{},
			"content",
			fmt.Sprintf(advHintDOCNAME01CannotScan, err),
			advHintDOCNAME01CannotScanFix,
		))
	}
	return results
}

func findDocLiteral(line, literal string, start int) int {
	if literal == "" || start >= len(line) {
		return -1
	}
	for {
		idx := strings.Index(line[start:], literal)
		if idx < 0 {
			return -1
		}
		idx += start
		end := idx + len(literal)
		if docNameBoundary(line, idx-1) && docNameBoundary(line, end) {
			return idx
		}
		start = end
		if start >= len(line) {
			return -1
		}
	}
}

func docNameBoundary(s string, idx int) bool {
	if idx < 0 || idx >= len(s) {
		return true
	}
	return !isDocNameChar(s[idx])
}

func isDocNameChar(c byte) bool {
	return (c >= 'a' && c <= 'z') ||
		(c >= 'A' && c <= 'Z') ||
		(c >= '0' && c <= '9') ||
		c == '-' ||
		c == '_'
}

func docNamingExcluded(rel string, patterns []string) bool {
	for _, pattern := range patterns {
		if docNamingPatternMatch(rel, pattern) {
			return true
		}
	}
	return false
}

func docNamingPatternMatch(rel, pattern string) bool {
	rel = filepath.ToSlash(rel)
	pattern = strings.TrimSpace(filepath.ToSlash(pattern))
	if pattern == "" {
		return false
	}
	if before, ok := strings.CutSuffix(pattern, "/**"); ok {
		base := before
		return rel == base || strings.HasPrefix(rel, base+"/")
	}
	if ok, err := path.Match(pattern, rel); err == nil && ok {
		return true
	}
	return rel == pattern
}

func hasGlobMeta(pattern string) bool {
	return strings.ContainsAny(pattern, "*?[")
}

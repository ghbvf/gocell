package governance

// RuleCode is the named type for governance rule code literals. All rule code
// constants are declared below as RuleCode-typed values, making bare string
// literals non-assignable to the Code field of ValidationResult without an
// explicit conversion. Archtest INV-2
// (GOVERNANCE-RULE-CODE-CONST-SINGLE-SOURCE-01) enforces that every call to
// newResult / newScopedResult, and every ValidationResult CompositeLit inside
// kernel/governance, uses one of these package-scope RuleCode constants rather
// than an ad-hoc string literal.
//
// rulecodes.go is the single source of truth for governance rule code
// literals emitted by all three registration roots:
//
//   - Validator (rules() + strictRules())
//   - DependencyChecker (Check / CheckFailFast)
//   - CheckContractHealth (and the CH-aligned rules in rules_http.go)
//
// Every "<SERIES>-<NN>" literal embedded in kernel/governance/*.go must come
// from one of the codeXxx constants below; archtest
// GOVERNANCE-RULE-CODE-CONST-SINGLE-SOURCE-01 (tools/archtest/
// governance_rules_invariants_test.go) enforces this. The constants are
// package-private — rule codes are an internal registration concept and not
// part of any public API. RuleCode itself is exported so external consumers
// (cmd/gocell, tools) can create ValidationResult values with the correct type.
//
// Total: 85 constants across 12 series, matching goldenRuleIDs() in
// rule_inventory_test.go. FMT-18 and ADV-02 are retired; the numbering gaps
// are intentional.

// RuleCode is a named string type that identifies a single governance rule.
// Exported so cmd/ and tools/ can reference the type when constructing
// ValidationResult values.
type RuleCode string

const (
	// REF — referential integrity (rules_ref.go, rules_fmt.go (REF-12)).
	codeREF01 RuleCode = "REF-01"
	codeREF02 RuleCode = "REF-02"
	codeREF03 RuleCode = "REF-03"
	codeREF04 RuleCode = "REF-04"
	codeREF05 RuleCode = "REF-05"
	codeREF06 RuleCode = "REF-06"
	codeREF07 RuleCode = "REF-07"
	codeREF08 RuleCode = "REF-08"
	codeREF09 RuleCode = "REF-09"
	codeREF10 RuleCode = "REF-10"
	codeREF11 RuleCode = "REF-11"
	codeREF12 RuleCode = "REF-12"
	codeREF13 RuleCode = "REF-13"
	codeREF14 RuleCode = "REF-14"
	codeREF15 RuleCode = "REF-15"
	codeREF16 RuleCode = "REF-16"
	codeREF17 RuleCode = "REF-17"

	// TOPO — topology legality (rules_topo.go).
	codeTOPO01 RuleCode = "TOPO-01"
	codeTOPO02 RuleCode = "TOPO-02"
	codeTOPO03 RuleCode = "TOPO-03"
	codeTOPO04 RuleCode = "TOPO-04"
	codeTOPO05 RuleCode = "TOPO-05"
	codeTOPO06 RuleCode = "TOPO-06"
	codeTOPO07 RuleCode = "TOPO-07"
	codeTOPO08 RuleCode = "TOPO-08"
	codeTOPO09 RuleCode = "TOPO-09"

	// VERIFY — verify closure (rules_verify.go; VERIFY-06 strict-only).
	codeVERIFY01 RuleCode = "VERIFY-01"
	codeVERIFY02 RuleCode = "VERIFY-02"
	codeVERIFY03 RuleCode = "VERIFY-03"
	codeVERIFY04 RuleCode = "VERIFY-04"
	codeVERIFY05 RuleCode = "VERIFY-05"
	codeVERIFY06 RuleCode = "VERIFY-06"

	// FMT — format compliance (rules_fmt.go, rules_misc_strict.go,
	// rules_misc_advisory.go). FMT-18 retired; numbering gap intentional.
	codeFMT01 RuleCode = "FMT-01"
	codeFMT02 RuleCode = "FMT-02"
	codeFMT03 RuleCode = "FMT-03"
	codeFMT04 RuleCode = "FMT-04"
	codeFMT05 RuleCode = "FMT-05"
	codeFMT06 RuleCode = "FMT-06"
	codeFMT07 RuleCode = "FMT-07"
	codeFMT08 RuleCode = "FMT-08"
	codeFMT09 RuleCode = "FMT-09"
	codeFMT10 RuleCode = "FMT-10"
	codeFMT11 RuleCode = "FMT-11"
	codeFMT12 RuleCode = "FMT-12"
	codeFMT13 RuleCode = "FMT-13"
	codeFMT14 RuleCode = "FMT-14"
	codeFMT15 RuleCode = "FMT-15"
	codeFMT16 RuleCode = "FMT-16"
	codeFMT17 RuleCode = "FMT-17"
	codeFMT19 RuleCode = "FMT-19"
	codeFMT20 RuleCode = "FMT-20"
	codeFMT21 RuleCode = "FMT-21"
	codeFMT22 RuleCode = "FMT-22"
	codeFMT23 RuleCode = "FMT-23"
	codeFMT24 RuleCode = "FMT-24"
	codeFMT25 RuleCode = "FMT-25"
	codeFMT26 RuleCode = "FMT-26"
	codeFMT27 RuleCode = "FMT-27"
	codeFMT28 RuleCode = "FMT-28"
	codeFMT29 RuleCode = "FMT-29"
	codeFMT30 RuleCode = "FMT-30"
	codeFMT31 RuleCode = "FMT-31"
	codeFMT32 RuleCode = "FMT-32"
	codeFMTA1 RuleCode = "FMT-A1"
	codeFMTC1 RuleCode = "FMT-C1"

	// ADV — advisory warnings & dead-event detection (rules_misc_advisory.go).
	// 5 active constants (ADV-01/03/04/05/06); ADV-02 retired, gap intentional.
	// ADV-05/06 are SeverityError; ADV-01/03/04 are SeverityWarning.
	codeADV01 RuleCode = "ADV-01"
	codeADV03 RuleCode = "ADV-03"
	codeADV04 RuleCode = "ADV-04"
	codeADV05 RuleCode = "ADV-05"
	codeADV06 RuleCode = "ADV-06"

	// CH — contract-health (contracthealth.go, rules_http.go).
	// Registered via Validator.CheckContractHealth, not rules().
	codeCH01 RuleCode = "CH-01"
	codeCH02 RuleCode = "CH-02"
	codeCH03 RuleCode = "CH-03"
	codeCH04 RuleCode = "CH-04"
	codeCH05 RuleCode = "CH-05"
	codeCH06 RuleCode = "CH-06"

	// DEP — dependency-graph checks (depcheck.go). Registered via
	// DependencyChecker.Check / CheckFailFast, not rules().
	codeDEP01 RuleCode = "DEP-01"
	codeDEP02 RuleCode = "DEP-02"
	codeDEP03 RuleCode = "DEP-03"

	// JOURNEY — journey lifecycle & cross-file consistency (rules_journey.go).
	// JOURNEY-CONTRACT-EXISTENCE-01 is the inverse direction of REF-07:
	// REF-07 checks journey.contracts[] → contracts/ existence; JOURNEY-CONTRACT-
	// EXISTENCE-01 checks contracts/ (active, non-examples) → at least one
	// journey.contracts[] reference. JOURNEY-STATUS-LIFECYCLE-01 enforces
	// status-board[i].state × J-*.yaml.lifecycle matrix.
	codeJOURNEYCONTRACTEXISTENCE01 RuleCode = "JOURNEY-CONTRACT-EXISTENCE-01"
	codeJOURNEYSTATUSLIFECYCLE01   RuleCode = "JOURNEY-STATUS-LIFECYCLE-01"

	// Misc single-rule clusters.
	codeOUTGUARD01                RuleCode = "OUTGUARD-01"
	codeSLICECONSISTENCY01        RuleCode = "SLICE-CONSISTENCY-01"
	codeDOCNAME01                 RuleCode = "DOC-NAME-01"
	codeCONTRACTCONSISTENCYEMIT01 RuleCode = "CONTRACT-CONSISTENCY-EMIT-01"
)

package governance

// rules_registry.go holds allRules — the single source of truth for every
// governance rule (ADR 202605041430 §M3-RULE-ENGINE). engine.go iterates it;
// command entry points (ValidateStrict, the check command) select phases.
//
// Each entry is a Rule value: static classification (Code / Phase) plus a
// compiler-checked Detect function value. Detect functions live alongside
// their former rule cluster (rules_ref.go, rules_topo.go, ...) and return
// []ValidationResult built via the locator constructors.
//
// Invariant: the set of Rule.Code values here equals goldenRuleIDs() and every
// Code is unique. Uniqueness and completeness are archtest-locked
// (GOVERNANCE-RULES-REGISTRATION-GUARD-01, retargeted to this slice in Step 9).
//
// VERIFY-06 takes a ctx parameter; its Detect closure reads v.runCtx which
// run() stamps at the top of each invocation (Step 3).
var allRules = []Rule{
	// -------------------------------------------------------------------------
	// PhaseBase — run on every `gocell validate` invocation
	// -------------------------------------------------------------------------

	// REF — referential integrity
	{Code: codeREF01, Phase: PhaseBase, Detect: (*Validator).validateREF01},
	{Code: codeREF02, Phase: PhaseBase, Detect: (*Validator).validateREF02},
	{Code: codeREF03, Phase: PhaseBase, Detect: (*Validator).validateREF03},
	{Code: codeREF04, Phase: PhaseBase, Detect: (*Validator).validateREF04},
	{Code: codeREF05, Phase: PhaseBase, Detect: (*Validator).validateREF05},
	{Code: codeREF06, Phase: PhaseBase, Detect: (*Validator).validateREF06},
	{Code: codeREF07, Phase: PhaseBase, Detect: (*Validator).validateREF07},
	{Code: codeREF08, Phase: PhaseBase, Detect: (*Validator).validateREF08},
	{Code: codeREF09, Phase: PhaseBase, Detect: (*Validator).validateREF09},
	{Code: codeREF10, Phase: PhaseBase, Detect: (*Validator).validateREF10},
	{Code: codeREF11, Phase: PhaseBase, Detect: (*Validator).validateREF11},
	{Code: codeREF12, Phase: PhaseBase, Detect: (*Validator).validateREF12},
	{Code: codeREF13, Phase: PhaseBase, Detect: (*Validator).validateREF13},
	{Code: codeREF14, Phase: PhaseBase, Detect: (*Validator).validateREF14},
	{Code: codeREF15, Phase: PhaseBase, Detect: (*Validator).validateREF15},
	{Code: codeREF16, Phase: PhaseBase, Detect: (*Validator).validateREF16},
	{Code: codeREF17, Phase: PhaseBase, Detect: (*Validator).validateREF17},
	{Code: codeREF18, Phase: PhaseBase, Detect: (*Validator).validateREF18},

	// TOPO — topology legality
	{Code: codeTOPO01, Phase: PhaseBase, Detect: (*Validator).validateTOPO01},
	{Code: codeTOPO02, Phase: PhaseBase, Detect: (*Validator).validateTOPO02},
	{Code: codeTOPO03, Phase: PhaseBase, Detect: (*Validator).validateTOPO03},
	{Code: codeTOPO04, Phase: PhaseBase, Detect: (*Validator).validateTOPO04},
	{Code: codeTOPO05, Phase: PhaseBase, Detect: (*Validator).validateTOPO05},
	{Code: codeTOPO06, Phase: PhaseBase, Detect: (*Validator).validateTOPO06},
	{Code: codeTOPO07, Phase: PhaseBase, Detect: (*Validator).validateTOPO07},
	{Code: codeTOPO08, Phase: PhaseBase, Detect: (*Validator).validateTOPO08},
	{Code: codeTOPO09, Phase: PhaseBase, Detect: (*Validator).validateTOPO09},

	// VERIFY — verify closure (VERIFY-06 is PhaseStrict below)
	{Code: codeVERIFY01, Phase: PhaseBase, Detect: (*Validator).validateVERIFY01},
	{Code: codeVERIFY02, Phase: PhaseBase, Detect: (*Validator).validateVERIFY02},
	{Code: codeVERIFY03, Phase: PhaseBase, Detect: (*Validator).validateVERIFY03},
	{Code: codeVERIFY04, Phase: PhaseBase, Detect: (*Validator).validateVERIFY04},
	{Code: codeVERIFY05, Phase: PhaseBase, Detect: (*Validator).validateVERIFY05},

	// FMT — format compliance (base subset; FMT-16/17/19 are PhaseStrict below)
	{Code: codeFMT01, Phase: PhaseBase, Detect: (*Validator).validateFMT01},
	{Code: codeFMT02, Phase: PhaseBase, Detect: (*Validator).validateFMT02},
	{Code: codeFMT03, Phase: PhaseBase, Detect: (*Validator).validateFMT03},
	{Code: codeFMT04, Phase: PhaseBase, Detect: (*Validator).validateFMT04},
	{Code: codeFMT05, Phase: PhaseBase, Detect: (*Validator).validateFMT05},
	{Code: codeFMT06, Phase: PhaseBase, Detect: (*Validator).validateFMT06},
	{Code: codeFMT07, Phase: PhaseBase, Detect: (*Validator).validateFMT07},
	{Code: codeFMT08, Phase: PhaseBase, Detect: (*Validator).validateFMT08},
	{Code: codeFMT09, Phase: PhaseBase, Detect: (*Validator).validateFMT09},
	{Code: codeFMT10, Phase: PhaseBase, Detect: (*Validator).validateFMT10},
	{Code: codeFMT11, Phase: PhaseBase, Detect: (*Validator).validateFMT11},
	{Code: codeFMT12, Phase: PhaseBase, Detect: (*Validator).validateFMT12},
	{Code: codeFMT13, Phase: PhaseBase, Detect: (*Validator).validateFMT13},
	{Code: codeFMT14, Phase: PhaseBase, Detect: (*Validator).validateFMT14},
	{Code: codeFMT15, Phase: PhaseBase, Detect: (*Validator).validateFMT15},
	{Code: codeFMT20, Phase: PhaseBase, Detect: (*Validator).validateFMTRequestStrict01},
	{Code: codeFMT21, Phase: PhaseBase, Detect: (*Validator).validateFMTContractDirIDMatch01},
	{Code: codeFMT22, Phase: PhaseBase, Detect: (*Validator).validateStatusBoardStateEnum01},
	{Code: codeFMT23, Phase: PhaseBase, Detect: (*Validator).validateContractDeprecatedCleanup01},
	{Code: codeFMT24, Phase: PhaseBase, Detect: (*Validator).validateFMT24},
	{Code: codeFMT25, Phase: PhaseBase, Detect: (*Validator).validateFMTInputConstraint01},
	{Code: codeFMT26, Phase: PhaseBase, Detect: (*Validator).validateFMT26},
	{Code: codeFMT27, Phase: PhaseBase, Detect: (*Validator).validateFMT27},
	{Code: codeFMT28, Phase: PhaseBase, Detect: (*Validator).validateFMT28},
	{Code: codeFMT29, Phase: PhaseBase, Detect: (*Validator).validateFMT29},
	{Code: codeFMT30, Phase: PhaseBase, Detect: (*Validator).validateFMT30},
	{Code: codeFMT31, Phase: PhaseBase, Detect: (*Validator).validateFMT31},
	{Code: codeFMT32, Phase: PhaseBase, Detect: (*Validator).validateFMT32},
	{Code: codeFMT33, Phase: PhaseBase, Detect: (*Validator).validateFMT33},
	{Code: codeFMT34, Phase: PhaseBase, Detect: (*Validator).validateFMT34},
	{Code: codeFMT35, Phase: PhaseBase, Detect: (*Validator).validateFMT35},
	{Code: codeFMT36, Phase: PhaseBase, Detect: (*Validator).validateFMT36},
	{Code: codeFMT37, Phase: PhaseBase, Detect: (*Validator).validateFMT37},
	{Code: codeFMT38, Phase: PhaseBase, Detect: (*Validator).validateFMT38},
	{Code: codeFMT39, Phase: PhaseBase, Detect: (*Validator).validateFMT39},
	{Code: codeFMT40, Phase: PhaseBase, Detect: (*Validator).validateFMT40},
	{Code: codeFMTA1, Phase: PhaseBase, Detect: (*Validator).validateFMTA1},
	{Code: codeFMTC1, Phase: PhaseBase, Detect: (*Validator).validateFMTC1},

	// ADV — advisory warnings (ADV-01/03/04 are SeverityWarning)
	{Code: codeADV01, Phase: PhaseBase, Detect: (*Validator).validateADV01},
	{Code: codeADV03, Phase: PhaseBase, Detect: (*Validator).validateADV03},
	{Code: codeADV04, Phase: PhaseBase, Detect: (*Validator).validateADV04},
	// ADV-05 is SeverityWarning (M3 reclassification): a dead active event is
	// advisory, not blocking.
	{Code: codeADV05, Phase: PhaseBase, Detect: (*Validator).validateADV05},

	// OUTGUARD — outbox durability
	{Code: codeOUTGUARD01, Phase: PhaseBase, Detect: (*Validator).validateOUTGUARD01},

	// SLICE-CONSISTENCY
	{Code: codeSLICECONSISTENCY01, Phase: PhaseBase, Detect: (*Validator).validateSliceConsistency},
	{Code: codeSLICECONSISTENCY02, Phase: PhaseBase, Detect: (*Validator).validateSliceConsistencyContractUsages},

	// CELL-LIFECYCLE — cell/slice maturity lifecycle membership + slice≤cell.
	{Code: codeCELLLIFECYCLE01, Phase: PhaseBase, Detect: (*Validator).validateCELLLIFECYCLE01},

	// CONTRACT-CONSISTENCY-EMIT
	{Code: codeCONTRACTCONSISTENCYEMIT01, Phase: PhaseBase, Detect: (*Validator).validateCONTRACTCONSISTENCYEMIT01},

	// FRAMEWORK-OWNED-CONTRACT-SCOPED — framework owner eligibility + fail-closed lifecycle
	{Code: codeFRAMEWORKOWNEDCONTRACTSCOPED01, Phase: PhaseBase, Detect: (*Validator).validateFRAMEWORKOWNEDCONTRACTSCOPED01},

	// JOURNEY
	{Code: codeJOURNEYCONTRACTEXISTENCE01, Phase: PhaseBase, Detect: (*Validator).validateJOURNEYCONTRACTEXISTENCE01},
	{Code: codeJOURNEYSTATUSLIFECYCLE01, Phase: PhaseBase, Detect: (*Validator).validateJOURNEYSTATUSLIFECYCLE01},

	// CONTRACT-ENDPOINT-TEST-MAPPING
	{Code: codeCONTRACTENDPOINTTESTMAPPING01, Phase: PhaseBase, Detect: (*Validator).validateCONTRACTENDPOINTTESTMAPPING01},

	// PROJECTION-CONSISTENCY
	{Code: codePROJECTIONCONSISTENCY01, Phase: PhaseBase, Detect: (*Validator).validateProjectionConsistency},
	{Code: codePROJECTIONPROVIDENEEDSWRITECU01, Phase: PhaseBase, Detect: (*Validator).validatePROJECTIONPROVIDENEEDSWRITECU01},
	{Code: codePROJECTIONSAGASOURCENEEDSPROJECTION01, Phase: PhaseBase, Detect: (*Validator).validatePROJECTIONSAGASOURCENEEDSPROJECTION01},

	// SAGA — saga contract format (rules_saga.go)
	{Code: codeSAGACONTRACTBLOCKPRESENT01, Phase: PhaseBase, Detect: (*Validator).validateSAGACONTRACTBLOCKPRESENT01},
	{Code: codeSAGACONTRACTSTEPSNONEMPTY01, Phase: PhaseBase, Detect: (*Validator).validateSAGACONTRACTSTEPSNONEMPTY01},
	{Code: codeSAGACONTRACTSTEPNAMEVALID01, Phase: PhaseBase, Detect: (*Validator).validateSAGACONTRACTSTEPNAMEVALID01},
	{Code: codeSAGACONTRACTSTEPNAMEUNIQUE01, Phase: PhaseBase, Detect: (*Validator).validateSAGACONTRACTSTEPNAMEUNIQUE01},
	{Code: codeSAGACONTRACTSTEPSCHEMAREF01, Phase: PhaseBase, Detect: (*Validator).validateSAGACONTRACTSTEPSCHEMAREF01},
	{Code: codeSAGACONTRACTCOMPENSATIONORDER01, Phase: PhaseBase, Detect: (*Validator).validateSAGACONTRACTCOMPENSATIONORDER01},
	{Code: codeSAGACONTRACTCONSISTENCYL301, Phase: PhaseBase, Detect: (*Validator).validateSAGACONTRACTCONSISTENCYL301},
	{Code: codeSAGACONTRACTRETRYTIMEOUT01, Phase: PhaseBase, Detect: (*Validator).validateSAGACONTRACTRETRYTIMEOUT01},
	{Code: codeSAGACELLLEVELL3DECLARE01, Phase: PhaseBase, Detect: (*Validator).validateSAGACELLLEVELL3DECLARE01},

	// COMMAND — command contract format (rules_command.go)
	{Code: codeCOMMANDCONTRACTSCHEMAREF01, Phase: PhaseBase, Detect: (*Validator).validateCOMMANDCONTRACTSCHEMAREF01},
	{Code: codeCOMMANDCONTRACTCONSISTENCYLEVEL01, Phase: PhaseBase, Detect: (*Validator).validateCOMMANDCONTRACTCONSISTENCYLEVEL01},

	// -------------------------------------------------------------------------
	// PhaseStrict — run only with `gocell validate --strict`
	// -------------------------------------------------------------------------

	// VERIFY-06: ctx-bound subprocess; reads v.runCtx set by run() at the top
	// of each invocation (Step 3 of M3-RULE-ENGINE Batch 2-3).
	// Must be first in PhaseStrict so fail-fast stops on VERIFY-06 before FMT rules.
	{
		Code:   codeVERIFY06,
		Phase:  PhaseStrict,
		Detect: func(v *Validator) []ValidationResult { return v.validateVERIFY06(v.runCtx) },
	},

	// FMT strict-only rules
	{Code: codeFMT16, Phase: PhaseStrict, Detect: (*Validator).validateFMT16},
	{Code: codeFMT17, Phase: PhaseStrict, Detect: (*Validator).validateFMT17},
	{Code: codeFMT19, Phase: PhaseStrict, Detect: (*Validator).validateFMT19},

	// DOC-NAME-01: legacy literal scanning (strict-only)
	{Code: codeDOCNAME01, Phase: PhaseStrict, Detect: (*Validator).validateDOCNAME01},

	// -------------------------------------------------------------------------
	// PhaseDep — cell dependency graph checks (`gocell validate` includes these)
	// -------------------------------------------------------------------------

	{Code: codeDEP01, Phase: PhaseDep, Detect: (*Validator).checkDEP01},
	{Code: codeDEP02, Phase: PhaseDep, Detect: (*Validator).checkDEP02},
	{Code: codeDEP03, Phase: PhaseDep, Detect: (*Validator).checkDEP03},

	// -------------------------------------------------------------------------
	// PhaseHealth — contract-health invariants (`gocell check contract-health`)
	// -------------------------------------------------------------------------

	{Code: codeCH01, Phase: PhaseHealth, Detect: (*Validator).checkCH01},
	{Code: codeCH02, Phase: PhaseHealth, Detect: (*Validator).checkCH02},
	{Code: codeCH03, Phase: PhaseHealth, Detect: (*Validator).checkCH03},
	{Code: codeCH04, Phase: PhaseHealth, Detect: (*Validator).checkCH04},
	{Code: codeCH05, Phase: PhaseHealth, Detect: (*Validator).checkCH05},
	{Code: codeCH06, Phase: PhaseHealth, Detect: (*Validator).checkCH06},
	{Code: codeCH07, Phase: PhaseHealth, Detect: (*Validator).checkCH07},
}

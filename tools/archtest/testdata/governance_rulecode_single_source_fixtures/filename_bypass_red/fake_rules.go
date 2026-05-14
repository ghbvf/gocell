package filename_bypass_red

// codeBad is intentionally declared outside rulecodes.go to verify that
// collectRuleCodeConsts excludes it via the filename guard.
// Before the fix this const would be accepted, making single-source
// validation name-only.
const codeBad RuleCode = "FMT-98"

package governance

import "fmt"

// validateOUTGUARD01 checks that cells with L2+ consistency level declare
// a durabilityMode in their cell.yaml. L2+ cells use the transactional outbox
// pattern and should explicitly declare "demo" or "durable" mode so that
// runtime CheckNotNoop can enforce the correct behaviour.
//
// Missing durabilityMode on L2+ cells is SeverityError because the runtime
// CheckNotNoop is a hard gate — if the author didn't declare intent, CI should
// catch it before runtime does. Invalid values are also SeverityError.
//
// ref: K8s apimachinery validation — required field checks
// ref: kernel/cell/durability.go — DurabilityMode, CheckNotNoop
func (v *Validator) validateOUTGUARD01() []ValidationResult {
	var results []ValidationResult
	for _, c := range v.project.Cells {
		if !isL2OrHigher(c.ConsistencyLevel) {
			continue
		}
		if c.DurabilityMode == "" {
			results = append(results, ValidationResult{
				Code:      "OUTGUARD-01",
				Severity:  SeverityError,
				IssueType: IssueRequired,
				File:      cellFile(c.ID),
				Field:     "durabilityMode",
				Message: fmt.Sprintf(
					"cell %q declares %s consistency but has no durabilityMode; "+
						"set durabilityMode to \"demo\" or \"durable\" so CheckNotNoop "+
						"can enforce outbox durability at runtime",
					c.ID, c.ConsistencyLevel),
			})
			continue
		}
		if !isValidDurabilityMode(c.DurabilityMode) {
			results = append(results, ValidationResult{
				Code:      "OUTGUARD-01",
				Severity:  SeverityError,
				IssueType: IssueInvalid,
				File:      cellFile(c.ID),
				Field:     "durabilityMode",
				Message: fmt.Sprintf(
					"cell %q has invalid durabilityMode %q; must be \"demo\" or \"durable\"",
					c.ID, c.DurabilityMode),
			})
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

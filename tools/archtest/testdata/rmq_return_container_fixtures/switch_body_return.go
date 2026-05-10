// Negative fixture for RMQ-PUBLISHER-FAILURE-HANDLING-01-D.
// Error return inside a switch case body — Wave 1 does not recurse into
// SwitchStmt; Wave 4 adds the SwitchStmt + CaseClause case branches.
package fixture

func (p *P) Publish() error {
	switch mode := compute(); mode {
	case 1:
		if err := op(); err != nil {
			return err
		}
	default:
	}
	return nil
}

func compute() int { return 0 }

func op() error { return nil }

type P struct{}

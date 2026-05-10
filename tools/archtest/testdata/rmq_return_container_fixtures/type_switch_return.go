// Negative fixture for RMQ-PUBLISHER-FAILURE-HANDLING-01-D.
// Error return inside a type-switch case body — Wave 1 does not recurse
// into TypeSwitchStmt; Wave 4 adds the TypeSwitchStmt + CaseClause branches.
package fixture

func (p *P) Publish() error {
	var x interface{} = 42
	switch x.(type) {
	case int:
		if err := op(); err != nil {
			return err
		}
	default:
	}
	return nil
}

func op() error { return nil }

type P struct{}

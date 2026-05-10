// Negative fixture for RMQ-PUBLISHER-FAILURE-HANDLING-01-D.
// Error return inside a range-loop body — checkPublishStmtViolations does
// not recurse into RangeStmt in Wave 1, so the violation is silently
// skipped. Wave 4 fixes this.
package fixture

func (p *P) Publish() error {
	for _, item := range items {
		if err := opOn(item); err != nil {
			return err
		}
	}
	return nil
}

var items = []int{}

func opOn(_ int) error { return nil }

type P struct{}

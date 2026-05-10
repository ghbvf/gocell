// Negative fixture for RMQ-PUBLISHER-FAILURE-HANDLING-01-D.
// Error return inside a for-loop body MUST be reported as missing
// RecordPublishFailure, but the Wave 1 checkPublishStmtViolations switch
// does not recurse into ForStmt so the violation is silently skipped.
// Wave 4 extends the switch and this fixture begins yielding 1 violation.
package fixture

func (p *P) Publish() error {
	for i := 0; i < 10; i++ {
		if err := op(); err != nil {
			return err
		}
	}
	return nil
}

func op() error { return nil }

type P struct{}

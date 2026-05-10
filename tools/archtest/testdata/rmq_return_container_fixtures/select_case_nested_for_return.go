// Negative fixture for RMQ-PUBLISHER-FAILURE-HANDLING-01-D.
// Error return inside a select case body's nested for-loop. The outer
// SelectStmt IS recursed in Wave 1 (via EachNode[CommClause]), but the
// inner ForStmt is NOT recursed, so the if-block's error return inside
// the for-body is silently skipped. Wave 4 adds the ForStmt branch and
// this fixture begins yielding 1 violation.
package fixture

func (p *P) Publish(ctx ctxLike) error {
	select {
	case <-ctx.Tick():
		for i := 0; i < 3; i++ {
			if err := op(); err != nil {
				return err
			}
		}
	}
	return nil
}

type ctxLike interface{ Tick() <-chan struct{} }

func op() error { return nil }

type P struct{}

// Negative fixture for RMQ-PUBLISHER-FAILURE-HANDLING-01-D.
// Two nested SelectStmts: in Wave 1, EachNode[CommClause] walks the outer
// SelectStmt's full subtree, finding the inner SelectStmt's CommClause
// AND re-finding it via the inner SelectStmt's own EachNode call when
// the outer's CommClause body is recursed via checkPublishStmtViolations.
// The single error return is therefore double-attributed (count = 2).
//
// Wave 4 changes SelectStmt to direct-child Body.List iteration so the
// nested select's CommClause is reached only via the recursive descent,
// not via subtree-walk leakage. Then count = 1 as expected.
package fixture

func (p *P) Publish(ctx ctxLike) error {
	select {
	case <-ctx.Tick():
		select {
		case <-ctx.Tick():
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

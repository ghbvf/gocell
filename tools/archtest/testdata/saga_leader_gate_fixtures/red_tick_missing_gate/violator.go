//go:build archtest_fixture

package redtickmissinggate

import "context"

type coord struct{}

func (c *coord) driveOne(context.Context) error { return nil }

// tickOnce drives without ever calling acquireLead — A2 must flag this.
func (c *coord) tickOnce(ctx context.Context) {
	_ = c.driveOne(ctx)
}

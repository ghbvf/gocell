//go:build archtest_fixture

package reddriveoutsidetick

import "context"

type coord struct{}

func (c *coord) acquireLead(context.Context) (func(), bool) { return func() {}, true }

func (c *coord) driveOne(context.Context) error { return nil }

// tickOnce drives only after passing the gate — the driveOne here is allowed.
func (c *coord) tickOnce(ctx context.Context) {
	if _, ok := c.acquireLead(ctx); ok {
		_ = c.driveOne(ctx)
	}
}

// rogue calls driveOne without consulting the gate — A1 must flag this.
func (c *coord) rogue(ctx context.Context) {
	_ = c.driveOne(ctx)
}

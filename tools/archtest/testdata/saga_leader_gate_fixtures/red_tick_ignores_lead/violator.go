//go:build archtest_fixture

package redtickignoreslead

import "context"

type claimed struct{}

type coord struct{}

func (c *coord) driveOne(context.Context, claimed) error { return nil }
func (c *coord) acquireLead(context.Context, claimed) (func(), func(), bool) {
	return func() {}, func() {}, true
}

// tickOnce calls acquireLead but ignores the lead verdict: it captures (and even
// references) lead, then drives unconditionally with no `if !lead { continue }`
// guard. A2 passes (acquireLead is called); A3 must flag the missing gate.
func (c *coord) tickOnce(ctx context.Context, ci claimed) {
	release, orphan, lead := c.acquireLead(ctx, ci)
	_ = lead
	_ = orphan
	_ = c.driveOne(ctx, ci)
	release()
}

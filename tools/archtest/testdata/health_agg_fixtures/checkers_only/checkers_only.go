// Package checkersonly declares Checkers() but neither Worker() nor Close().
// HEALTH-AGG-01 MUST flag Bad as a violation.
package checkersonly

import "context"

type Bad struct{}

func (*Bad) Checkers() map[string]func(context.Context) error { return nil }

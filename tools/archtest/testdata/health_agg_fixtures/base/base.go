// Package base provides a fake ManagedResource implementation that fixtures
// embed to test promoted-method detection in HEALTH-AGG-01.
package base

import "context"

type PGResource struct{}

func (*PGResource) Checkers() map[string]func(context.Context) error { return nil }
func (*PGResource) Worker() func(context.Context) error              { return nil }
func (*PGResource) Close() error                                     { return nil }

//go:build archtest_fixture

// Package projcarrierfixture is a deliberate PROJECTION-EVENT-CARRIER-TYPED-01
// negative fixture loaded only when the archtest_fixture build tag is set.
//
// It declares projection-carrier-shaped exported symbols whose carrier
// parameter is the bare kernel/outbox.Entry — exactly the coupling
// PROJECTION-EVENT-CARRIER-TYPED-01 forbids. The scanner
// (scanProjectionCarrierViolations with restrictToProjectionPkgs=false) must
// report every BadXxx case and must NOT report the GoodXxx control.
//
// 本 fixture 含 3 个违规载体形态（命名 func type / interface 方法 / func-typed
// 参数）+ 2 个合法对照（接口载体 / 非载体的 outbox.Entry 用法）。修改本文件请同步
// 更新 tools/archtest/projection_event_carrier_typed_test.go 的
// expectedProjCarrierFixtureViolations 常量。
//
// The build tag excludes this package from `go build ./...` and `go test
// ./...` so it never pollutes real-repo scans; it is loaded explicitly via
//
//	archtest.Run(t, archtest.Fixture(archtest.FixtureOpts{Tests: false},
//	    []string{"./tools/archtest/internal/projcarrierfixture"}), rule)
package projcarrierfixture

import (
	"context"

	"github.com/ghbvf/gocell/framework/kernel/outbox"
)

// carrierIface stands in for the post-migration ProjectionEvent interface — a
// non-outbox.Entry carrier the GoodXxx controls accept.
type carrierIface interface {
	EventID() string
	Payload() []byte
}

// --- VIOLATIONS (must be caught) ---

// BadApply is a named func type whose event parameter is the bare
// outbox.Entry — violation #1 (named-func-type carrier position).
type BadApply func(ctx context.Context, event outbox.Entry) error

// BadSource is an interface whose Replay method takes a func(outbox.Entry)
// callback — violation #2 (func-typed-parameter carrier position).
type BadSource interface {
	Replay(ctx context.Context, fromOffset int64, fn func(outbox.Entry) error) error
}

// BadCursor is an interface whose Position method takes a bare outbox.Entry —
// violation #3 (interface-method carrier position).
type BadCursor interface {
	Position(entry outbox.Entry) (int64, error)
}

// --- GREEN CONTROLS (must NOT be caught) ---

// GoodApply accepts the interface carrier, not outbox.Entry.
type GoodApply func(ctx context.Context, event carrierIface) error

// GoodCursor accepts the interface carrier, not outbox.Entry.
type GoodCursor interface {
	Position(event carrierIface) (int64, error)
}

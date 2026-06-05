//go:build archtest_fixture

// Package projectioncursorenrollfixture is a synthetic fixture for
// PROJECTION-CURSOR-CONFORMANCE-ENROLL-01. It declares two projection.Cursor
// implementations:
//
//   - enrolledCursor: enrolled in projectiontest.RunCursorConformance by the
//     fixture's _test.go (the enroll archtest's RED fixture must NOT flag it).
//   - unenrolledCursor: deliberately NOT enrolled (the rule must flag it).
//
// Gated by the archtest_fixture build tag: invisible to normal `go build` /
// `go test` and to prodscan; loaded only via Run(t, Fixture(...)). Mirrors
// internal/projectionreplayenrollfixture.
//
// DO NOT use this package in production code.
package projectioncursorenrollfixture

import (
	"errors"
	"sync"

	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/projection"
)

// enrolledCursor is a correct in-memory Cursor that the fixture _test.go enrolls
// in projectiontest.RunCursorConformance. It resolves a 1-based position per
// entry ID and returns a permanent error for any entry it has not seen.
type enrolledCursor struct {
	mu   sync.RWMutex
	pos  map[string]int64
	next int64
}

func newEnrolledCursor() *enrolledCursor {
	return &enrolledCursor{pos: make(map[string]int64)}
}

// add assigns the next 1-based position to e (the fixture _test.go seed hook).
func (c *enrolledCursor) add(e projection.ProjectionEvent) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.next++
	c.pos[e.EventID()] = c.next
}

func (c *enrolledCursor) Position(e projection.ProjectionEvent) (int64, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if p, ok := c.pos[e.EventID()]; ok {
		return p, nil
	}
	return 0, outbox.NewPermanentError(errors.New("enrolledCursor: entry not seen"))
}

// unenrolledCursor is a second Cursor impl that is deliberately NOT enrolled —
// the enroll archtest must flag it.
type unenrolledCursor struct{}

func (unenrolledCursor) Position(_ projection.ProjectionEvent) (int64, error) {
	return 1, nil
}

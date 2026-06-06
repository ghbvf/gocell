//go:build archtest_fixture

// Package projectionreplayenrollfixture is a synthetic fixture for
// PROJECTION-REPLAY-SOURCE-CONFORMANCE-ENROLL-01. It declares two
// projection.ReplaySource implementations:
//
//   - enrolledReplaySource: enrolled in projectiontest.RunReplaySourceConformance
//     by the fixture's _test.go (the enroll archtest's RED fixture must NOT flag it).
//   - unenrolledReplaySource: deliberately NOT enrolled (the rule must flag it).
//
// Gated by the archtest_fixture build tag: invisible to normal `go build` /
// `go test` and to prodscan; loaded only via Run(t, Fixture(...)). Mirrors
// internal/projectioncheckpointenrollfixture.
//
// DO NOT use this package in production code.
package projectionreplayenrollfixture

import (
	"context"
	"sync"

	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/projection"
)

// enrolledReplaySource is a correct in-memory ReplaySource that the fixture
// _test.go enrolls in projectiontest.RunReplaySourceConformance.
type enrolledReplaySource struct {
	mu      sync.RWMutex
	entries []outbox.Entry
}

func newEnrolledReplaySource() *enrolledReplaySource {
	return &enrolledReplaySource{}
}

func (s *enrolledReplaySource) Append(e outbox.Entry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = append(s.entries, e)
}

func (s *enrolledReplaySource) Head(_ context.Context) (int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return int64(len(s.entries)), nil
}

func (s *enrolledReplaySource) Replay(ctx context.Context, fromOffset int64, fn func(projection.ProjectionEvent) error) error {
	s.mu.RLock()
	snapshot := make([]outbox.Entry, len(s.entries))
	copy(snapshot, s.entries)
	s.mu.RUnlock()
	for i, e := range snapshot {
		pos := int64(i + 1)
		if pos <= fromOffset {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := fn(e); err != nil {
			return err
		}
	}
	return nil
}

// unenrolledReplaySource is a second ReplaySource impl that is deliberately NOT
// enrolled — the enroll archtest must flag it.
type unenrolledReplaySource struct {
	mu      sync.RWMutex
	entries []outbox.Entry
}

func (s *unenrolledReplaySource) Head(_ context.Context) (int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return int64(len(s.entries)), nil
}

func (s *unenrolledReplaySource) Replay(ctx context.Context, fromOffset int64, fn func(projection.ProjectionEvent) error) error {
	s.mu.RLock()
	snapshot := make([]outbox.Entry, len(s.entries))
	copy(snapshot, s.entries)
	s.mu.RUnlock()
	for i, e := range snapshot {
		pos := int64(i + 1)
		if pos <= fromOffset {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := fn(e); err != nil {
			return err
		}
	}
	return nil
}

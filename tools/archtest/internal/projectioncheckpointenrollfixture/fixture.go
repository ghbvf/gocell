//go:build archtest_fixture

// Package projectioncheckpointenrollfixture is a synthetic fixture for
// PROJECTION-CHECKPOINT-CONFORMANCE-ENROLL-01. It declares two
// projection.CheckpointStore implementations:
//
//   - enrolledStore: enrolled in projectiontest.RunCheckpointConformance by the
//     fixture's _test.go (the enroll archtest's RED fixture must NOT flag it).
//   - unenrolledStore: deliberately NOT enrolled (the rule must flag it).
//
// The enroll archtest's RED fixture test runs the REAL scanner over this package
// and asserts the rule flags unenrolledStore but not enrolledStore — giving the
// scanner genuine negative AND positive coverage (vs the prior in-memory map copy).
//
// Gated by the archtest_fixture build tag: invisible to normal `go build` /
// `go test` and to prodscan; loaded only via Run(t, Fixture(...)). Mirrors
// internal/projectioncheckpointtxfixture.
//
// DO NOT use this package in production code.
package projectioncheckpointenrollfixture

import (
	"context"
	"sync"

	"github.com/ghbvf/gocell/framework/kernel/projection"
)

// enrolledStore is a correct in-memory CheckpointStore that the fixture _test.go
// enrolls in projectiontest.RunCheckpointConformance.
type enrolledStore struct {
	mu      sync.RWMutex
	offsets map[string]int64
}

func newEnrolledStore() *enrolledStore {
	return &enrolledStore{offsets: make(map[string]int64)}
}

func (s *enrolledStore) LoadOffset(_ context.Context, cellID, projectionID string) (int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.offsets[cellID+"/"+projectionID], nil
}

func (s *enrolledStore) SaveOffset(_ context.Context, cellID, projectionID string, offset int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.offsets[cellID+"/"+projectionID] = offset
	return nil
}

// unenrolledStore is a second CheckpointStore impl that is deliberately NOT
// enrolled — the enroll archtest must flag it.
type unenrolledStore struct {
	mu      sync.RWMutex
	offsets map[string]int64
}

func (s *unenrolledStore) LoadOffset(_ context.Context, cellID, projectionID string) (int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.offsets[cellID+"/"+projectionID], nil
}

func (s *unenrolledStore) SaveOffset(_ context.Context, cellID, projectionID string, offset int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.offsets[cellID+"/"+projectionID] = offset
	return nil
}

// compile-time interface checks: both types implement CheckpointStore so the
// archtest's impl discovery finds them.
var (
	_ projection.CheckpointStore = (*enrolledStore)(nil)
	_ projection.CheckpointStore = (*unenrolledStore)(nil)
)

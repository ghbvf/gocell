//go:build archtest_fixture

// Package sagaownercheckpointenrollfixture is a synthetic fixture for
// SAGA-OWNER-CHECKPOINT-CONFORMANCE-ENROLL-01. It declares an
// OwnerCheckpointStore implementation that is deliberately NOT enrolled in
// projectiontest.RunOwnerCheckpointConformance. The rule must flag it.
//
// The fixture is loaded only via Run(t, Fixture(...)) (archtest_fixture build
// tag), so it never enters a normal build or the production scan.
//
// DO NOT use this package in production code.
package sagaownercheckpointenrollfixture

import (
	"context"
	"sync"

	"github.com/ghbvf/gocell/kernel/projection"
)

// unenrolledOwnerStore implements projection.OwnerCheckpointStore but is NOT
// enrolled in projectiontest.RunOwnerCheckpointConformance.
// SAGA-OWNER-CHECKPOINT-CONFORMANCE-ENROLL-01 must flag this type.
type unenrolledOwnerStore struct {
	mu      sync.RWMutex
	offsets map[string]int64
	owners  map[string]string
}

func newUnenrolledOwnerStore() *unenrolledOwnerStore {
	return &unenrolledOwnerStore{
		offsets: make(map[string]int64),
		owners:  make(map[string]string),
	}
}

func (s *unenrolledOwnerStore) LoadOffset(_ context.Context, cellID, projectionID string) (int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.offsets[cellID+"/"+projectionID], nil
}

func (s *unenrolledOwnerStore) AdvanceIfOwner(_ context.Context, cellID, projectionID, ownerToken string, offset int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := cellID + "/" + projectionID
	existing := s.owners[key]
	if existing != "" && existing != ownerToken && s.offsets[key] >= offset {
		return projection.ErrStaleOwner
	}
	s.offsets[key] = offset
	s.owners[key] = ownerToken
	return nil
}

// compile-time interface check: this type must implement OwnerCheckpointStore
// so the archtest's impl discovery finds it.
var _ projection.OwnerCheckpointStore = (*unenrolledOwnerStore)(nil)

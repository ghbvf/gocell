//go:build archtest_fixture

// Package funcfieldholder is a RED fixture for SAGA-JOURNAL-HOLDER-SEAL-01
// rule 3 (F3): a struct persists a Heartbeater-shaped func value as a field.
// Holding such a callable is the func-value equivalent of a journal.Heartbeater
// field — it lets a centralized heartbeat loop be reconstructed from a heartbeat
// func handed in from outside runtime/saga — and must be flagged. A non-
// heartbeat func field is the negative control. Loaded only via RunTypedFixture.
package funcfieldholder

import (
	"context"
	"time"

	"github.com/ghbvf/gocell/pkg/idutil"
)

// HeartbeatFuncHolder persists a heartbeat-shaped func — the func-value path the
// holder seal (rule 3) must close.
type HeartbeatFuncHolder struct {
	beat func(ctx context.Context, instanceID, leaseID idutil.SafeID, leaseDuration time.Duration) (bool, error)
}

// namedHeartbeatFunc is a named func type of the heartbeat shape; a field of
// this type must also be flagged (Unalias/Underlying resolution).
type namedHeartbeatFunc func(ctx context.Context, instanceID, leaseID idutil.SafeID, leaseDuration time.Duration) (bool, error)

// NamedFuncHolder persists the named heartbeat-shaped func type.
type NamedFuncHolder struct {
	beat namedHeartbeatFunc
}

// PlainFuncHolder holds a non-heartbeat func — must NOT be flagged (negative
// control proving the shape match is specific, not "any func field").
type PlainFuncHolder struct {
	cb func(ctx context.Context) error
}

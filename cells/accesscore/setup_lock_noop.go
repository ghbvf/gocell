package accesscore

import (
	"context"

	"github.com/ghbvf/gocell/cells/accesscore/internal/ports"
)

// NoopSetupLock is the public typed marker memstore composition roots wire to
// satisfy the mandatory WithSetupLock option. Memstore mode does not need a
// separate cross-process lock because memTxRunner.RunInTx holds store.mu for
// the entire transaction closure (cells/accesscore/internal/mem/store.go
// memTxRunner.RunInTx), which by itself serializes all in-process goroutines
// equivalently to PG SELECT FOR UPDATE held until commit.
//
// PG composition roots MUST use accesspg.NewSetupLock(deps) instead. Wiring
// NoopSetupLock in PG mode is an upstream-Soft misconfiguration — the type
// system here cannot distinguish "right shape per mode". The Hard upgrade
// (TxRunner+SetupLock paired adapter factory) is tracked by backlog entry
// ADMINPROVISION-SETUPLOCK-PAIRED-CTOR-HARD-02.
type NoopSetupLock struct{}

// Compile-time assertion: NoopSetupLock implements ports.SetupLock.
var _ ports.SetupLock = NoopSetupLock{}

// Acquire returns nil. The actual serialization happens in the ambient
// memTxRunner.RunInTx that holds store.mu for the whole closure.
func (NoopSetupLock) Acquire(context.Context) error { return nil }

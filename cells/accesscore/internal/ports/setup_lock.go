package ports

import "context"

// SetupLock serializes the admin provisioning path across concurrent processes
// (e.g. multi-pod deployments that both call POST /setup/admin before any admin
// exists). Implementations must be acquired inside an open transaction context
// so the lock lifetime is bound to the surrounding transaction.
//
// The PG implementation uses pg_advisory_xact_lock, which releases automatically
// at tx commit or rollback — no explicit Release call is needed.
//
// Mem-mode callers inject nil; the existing sync.Mutex in adminprovision.Provisioner
// remains the intra-process serializer.
type SetupLock interface {
	// Acquire obtains the advisory lock within the current transaction. It
	// blocks until the lock is available. The lock is released automatically
	// when the surrounding transaction commits or rolls back.
	//
	// Must be called inside txRunner.RunInTx (i.e. ctx must carry a live pgx.Tx
	// via kernel/persistence.TxCtxKey).
	Acquire(ctx context.Context) error
}

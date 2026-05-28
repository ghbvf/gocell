package green

import "context"

// Adapter is the canonical handler-adapter form: holds *Service and
// invokes the contract-mapped service method. The B1 entry-resolver
// scans this file for `a.S.<Method>` calls on a *Service receiver to
// derive the contract's entry method set.
type Adapter struct{ S *Service }

func (a Adapter) Delete(ctx context.Context, sessionID, callerUserID string) error {
	return a.S.Logout(ctx, sessionID, callerUserID)
}

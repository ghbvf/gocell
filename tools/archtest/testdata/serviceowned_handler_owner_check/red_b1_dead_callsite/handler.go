package red_b1_dead_callsite

import "context"

type Adapter struct{ S *Service }

func (a Adapter) Delete(ctx context.Context, sessionID, callerUserID string) error {
	return a.S.Logout(ctx, sessionID, callerUserID)
}

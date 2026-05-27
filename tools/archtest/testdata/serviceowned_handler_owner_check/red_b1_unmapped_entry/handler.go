package red_b1_unmapped_entry

import "context"

// Adapter calls SomeOtherOp — NOT Logout. The B1 entry resolver derives
// entries = {SomeOtherOp}, which does NOT reach auth.CheckOwner (defined
// only in Logout). B1 must fire.
type Adapter struct{ S *Service }

func (a Adapter) Delete(ctx context.Context, sessionID string) error {
	return a.S.SomeOtherOp(ctx, sessionID)
}

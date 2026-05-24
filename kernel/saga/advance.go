package saga

import "time"

// advance.go — RED stubs. Real implementation lands in the GREEN commit.

func AdvanceSaga(inst *Instance, to Status, now time.Time) error { return nil }

func AdvanceStep(inst *Instance, stepCount int, now time.Time) error { return nil }

package saga

// Status — RED stub (intentionally non-functional). Real implementation lands
// in the GREEN commit.
type Status uint8

const (
	StatusPending Status = iota + 1
	StatusRunning
	StatusCompensating
	StatusSucceeded
	StatusFailed
	StatusCompensated
	StatusExpired
)

func (s Status) Valid() bool                        { return false }
func (s Status) String() string                     { return "" }
func (s Status) IsTerminal() bool                    { return false }
func (s Status) CanTransitionTo(target Status) bool  { return false }
func (s Status) ValidTransitions() []Status          { return nil }
func Transition(from, to Status) error               { return nil }

package saga

import (
	"time"

	"github.com/ghbvf/gocell/pkg/idutil"
)

// Instance — RED stub. Real implementation lands in the GREEN commit.
type Instance struct {
	ID           idutil.SafeID
	DefinitionID idutil.SafeID
	Status       Status
	CurrentStep  int
	StartedAt    time.Time
	UpdatedAt    *time.Time
	CompletedAt  *time.Time
}

func NewInstance(id, definitionID idutil.SafeID, now time.Time) Instance {
	return Instance{}
}

func (i *Instance) ValidateNew() error { return nil }

package cellvocab

import (
	"fmt"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// Levels lists the canonical consistency level strings in ascending rank
// order. Index = rank.
//
// Single source of truth shared by:
//
//   - cellvocab.Level / ParseLevel / Level.String — typed enum for runtime
//     business code;
//   - kernel/metadata assembly derivation — string-keyed rank lookup needed
//     during YAML parsing, before any cellvocab.Level value is bound.
//
// The taxonomy (L0..L4) is fixed by the GoCell constitution (sync/async ×
// local/cross-cell/device); adding a new level is not anticipated.
var Levels = [...]string{"L0", "L1", "L2", "L3", "L4"}

// Rank returns the rank index of s in Levels, or -1 if s is not a known
// level. The string form is canonical because metadata derivation runs
// before cellvocab.Level values are bound.
func Rank(s string) int {
	for i, lvl := range Levels {
		if s == lvl {
			return i
		}
	}
	return -1
}

// At returns the canonical string for rank r, or "" if r is out of range.
func At(r int) string {
	if r < 0 || r >= len(Levels) {
		return ""
	}
	return Levels[r]
}

// Level represents the consistency level (L0-L4) of a Cell or Contract.
type Level int

const (
	L0 Level = iota // LocalOnly
	L1              // LocalTx
	L2              // OutboxFact
	L3              // WorkflowEventual
	L4              // DeviceLatent
)

// String returns the string representation of a Level (e.g. "L0", "L2").
// Backed by cellvocab.At — single source of truth shared with kernel/metadata
// derivation.
func (l Level) String() string {
	if s := At(int(l)); s != "" {
		return s
	}
	return fmt.Sprintf("Level(%d)", int(l))
}

// ParseLevel parses a string like "L0" or "L3" into a Level.
// Returns errcode.ErrValidationFailed for unrecognized input.
func ParseLevel(s string) (Level, error) {
	if r := Rank(s); r >= 0 {
		return Level(r), nil
	}
	return 0, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
		"invalid consistency level",
		errcode.WithInternal(fmt.Sprintf(internalValueQuotedFmt, s)))
}

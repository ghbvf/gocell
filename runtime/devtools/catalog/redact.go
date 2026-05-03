// Package catalog — redact.go: privacy redaction for status board entries.
package catalog

import (
	"github.com/ghbvf/gocell/kernel/metadata"
)

// redactStatusBoard converts metadata.StatusBoardEntry slice to catalog wire
// form, clearing risk and blocker fields for entries in speculative states
// (draft or planned). Entries in operational states (todo/doing/blocked/ready)
// are returned unchanged. This prevents internal planning narratives from
// leaking into publicly embedded consumers (e.g. gocell-web bundle) while
// still exposing journey presence and delivery state.
func redactStatusBoard(entries []metadata.StatusBoardEntry) []StatusBoardEntry {
	out := make([]StatusBoardEntry, len(entries))
	for i, e := range entries {
		out[i] = StatusBoardEntry{
			JourneyID: e.JourneyID,
			State:     e.State,
			Risk:      e.Risk,
			Blocker:   e.Blocker,
			UpdatedAt: e.UpdatedAt,
		}
		if e.State == "draft" || e.State == "planned" {
			out[i].Risk = ""
			out[i].Blocker = ""
		}
	}
	return out
}

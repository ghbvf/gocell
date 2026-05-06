// Package levelrank holds the canonical ordering of consistency levels
// (L0..L4). It is the single source of truth shared by:
//
//   - kernel/cell.Level / ParseLevel / Level.String — typed enum for runtime
//     business code;
//   - kernel/metadata assembly derivation — string-keyed rank lookup needed
//     during YAML parsing, before any kernel/cell type is bound.
//
// kernel/cell already imports kernel/metadata (cell types depend on parsed
// metadata), so kernel/metadata cannot import kernel/cell. This sub-package
// breaks the cycle: kernel/cell and kernel/metadata both depend on
// kernel/cell/levelrank, while levelrank itself depends on neither.
//
// The taxonomy (L0..L4) is fixed by the GoCell constitution (sync/async ×
// local/cross-cell/device); adding a new level is not anticipated. The
// single-source-of-truth design here is about removing duplication between
// kernel/cell.Level constants and kernel/metadata derivation, not about
// preparing for taxonomy growth.
package levelrank

// Levels lists the canonical consistency level strings in ascending rank
// order. Index = rank.
var Levels = [...]string{"L0", "L1", "L2", "L3", "L4"}

// Rank returns the rank index of s in Levels, or -1 if s is not a known
// level. The string form is canonical because metadata derivation runs
// before kernel/cell.Level values are bound.
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

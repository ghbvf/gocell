// Package testdata — fixture with ghost (non-existent) slice references.
// After the subscribe single-source flip, only route markers reference slices
// via cell.go; subscribe slice references come from slice.yaml CUs directly.
package testcell

// +cell:listener:ref=cell.PrimaryListener,prefix=/api/v1
type GhostSliceCell struct {
	// +slice:route:slice=ghost,subPath=/items
	Items interface{}

	// +slice:route:slice=phantomslice,subPath=/other
	Other interface{}
}

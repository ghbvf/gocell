// Package testdata — fixture with route+listener markers for merge_test.go.
// Subscribe markers are no longer valid (subscribe single-source flip to
// slice.yaml contractUsages); this fixture contains only route/listener markers.
package testcell

// +cell:listener:ref=cell.PrimaryListener,prefix=/api/v1
type MarkerCell struct {
	// +slice:route:slice=sliceA,subPath=/items
	Items interface{}
}

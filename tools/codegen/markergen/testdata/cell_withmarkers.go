// Package testdata — fixture with full marker set for merge_test.go.
package testcell

// +cell:listener:ref=cell.PrimaryListener,prefix=/api/v1
type MarkerCell struct {
	// +slice:route:slice=sliceA,subPath=/items
	Items interface{}

	// +slice:subscribe:slice=sliceB,topic=event.foo.v1,handler=HandleFoo,group=markergroup
	Sub interface{}
}

// Package testdata — fixture: cell.go that still has a slice:subscribe marker
// (which is now an unknown marker after the subscribe single-source flip).
package testcell

// +cell:listener:ref=cell.PrimaryListener,prefix=/api/v1
type CellWithSubscribeMarker struct {
	// +slice:route:slice=sliceA,subPath=/items
	Items interface{}

	// +slice:subscribe:slice=sliceB,topic=event.foo.v1,handler=HandleFoo,group=mygroup
	Sub interface{}
}

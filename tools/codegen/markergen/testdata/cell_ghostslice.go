// Package testdata — fixture with ghost (non-existent) slice references.
package testcell

// +cell:listener:ref=cell.PrimaryListener,prefix=/api/v1
type GhostSliceCell struct {
	// +slice:route:slice=ghost,subPath=/items
	Items interface{}

	// +slice:subscribe:slice=phantomslice,topic=event.foo.v1,handler=HandleFoo,group=ghostgroup
	Sub interface{}
}

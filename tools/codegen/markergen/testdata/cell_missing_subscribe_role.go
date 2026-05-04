// Package testdata — fixture where slice referenced by slice:subscribe does not
// declare role=subscribe in contractUsages (K05-01a).
package testcell

// +cell:listener:ref=cell.PrimaryListener,prefix=/api/v1
type MissingSubscribeRoleCell struct {
	// +slice:subscribe:slice=sliceB,topic=event.foo.v1,handler=HandleFoo,group=mygroup
	Sub interface{}
}

// Package testdata — fixture where slice:route / slice:subscribe are placed on
// the type declaration (K05-04: must be on a named struct field).
package testcell

// +cell:listener:ref=cell.PrimaryListener,prefix=/api/v1
// +slice:route:slice=sliceA,subPath=/items
type RouteOnTypeCell struct{}

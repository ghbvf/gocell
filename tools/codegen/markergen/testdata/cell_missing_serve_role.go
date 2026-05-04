// Package testdata — fixture where slice referenced by slice:route does not
// declare role=serve in contractUsages (K05-01a).
package testcell

// +cell:listener:ref=cell.PrimaryListener,prefix=/api/v1
type MissingServeRoleCell struct {
	// +slice:route:slice=sliceA,subPath=/items
	Items interface{}
}

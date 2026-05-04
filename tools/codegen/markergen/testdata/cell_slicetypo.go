// Package testdata — fixture with typo "slcie=" field in route marker.
package testcell

// +cell:listener:ref=cell.PrimaryListener,prefix=/api/v1
type SliceTypoCell struct {
	// +slice:route:slcie=ordercreate,subPath=/orders
	Orders interface{}
}

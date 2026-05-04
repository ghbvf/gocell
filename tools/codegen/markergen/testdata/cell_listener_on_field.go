// Package testdata — fixture where cell:listener is placed on a struct field
// (K05-04: must be on type declaration).
package testcell

type ListenerOnFieldCell struct {
	// +cell:listener:ref=cell.PrimaryListener,prefix=/api/v1
	BadField interface{}
}

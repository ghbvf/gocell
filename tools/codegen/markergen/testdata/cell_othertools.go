// Package testdata — fixture with only non-GoCell markers (should be ignored).
package testcell

// +kubebuilder:object:root=true
// +protoc-gen-go: some value
type ExternalMarkerCell struct {
	// +json:name=myField
	Field interface{}
}

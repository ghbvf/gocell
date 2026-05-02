package a

import (
	"testing"

	"example.com/synth/testhelper"
)

func TestA(t *testing.T) {
	if testhelper.Echo(A()) == "" {
		t.Fatal("empty")
	}
}

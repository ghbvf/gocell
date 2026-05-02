package a_test

import (
	"testing"

	"example.com/synth/xtesthelper"
)

func TestA_External(t *testing.T) {
	if xtesthelper.Greet("world") == "" {
		t.Fatal("empty greeting")
	}
}

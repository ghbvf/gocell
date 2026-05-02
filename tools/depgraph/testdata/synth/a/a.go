package a

import (
	"example.com/synth/b"
	"example.com/synth/d"
)

func A() string { return b.B() + "/" + d.D() }

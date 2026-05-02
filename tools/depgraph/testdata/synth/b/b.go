package b

import "example.com/synth/c"

func B() string { return "b/" + c.C() }

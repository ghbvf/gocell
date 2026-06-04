// Package satcell is the SATELLITE module's cell package. Its import path
// (example.test/wsmm/satellite/cells/satcell) is a string subpath of the core
// module; it must classify as LayerCells within the satellite module (not
// LayerUnknown of the core module). It imports the core module to create a real
// satellite -> core edge.
package satcell

import "example.test/wsmm/cells/corecell"

// Echo references the core module, producing a real cross-module import edge.
var Echo = corecell.Greeting

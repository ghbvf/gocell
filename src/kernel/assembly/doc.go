// Package assembly provides the CoreAssembly that orchestrates Cell lifecycle
// (register, init, start, stop, health). Cells are started in registration
// order (FIFO) and stopped in reverse registration order (LIFO).
package assembly

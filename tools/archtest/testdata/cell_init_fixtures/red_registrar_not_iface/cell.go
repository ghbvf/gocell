// Package red_registrar_not_iface is a RED fixture for
// TestKernelCell_RegistrarDefinedHere: it declares a "Registrar" symbol that
// is a struct (not an interface) which scanPassForRegistrarLocality must flag.
package red_registrar_not_iface

// Registrar is a struct — not an interface — so the locality check must
// emit a diagnostic on the "must be an interface type" branch.
type Registrar struct{}

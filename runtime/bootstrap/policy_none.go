package bootstrap

import "github.com/go-chi/chi/v5"

// policyNone is a no-op Policy that installs no middleware.
type policyNone struct{}

func (policyNone) Describe() string { return "none" }
func (policyNone) Apply(_ *chi.Mux) {}

// PolicyNone returns a cell.Policy that installs no middleware on the mux.
// Use it for listeners that are network-isolated and require no authentication
// (e.g., a health listener bound to loopback behind a k8s probe path).
func PolicyNone() *policyNone { return &policyNone{} }

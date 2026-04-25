package bootstrap

import "github.com/ghbvf/gocell/kernel/cell"

// PolicyNone returns a cell.Policy that installs no middleware on the mux.
// Use it for listeners that are network-isolated and require no authentication
// (e.g., a health listener bound to loopback behind a k8s probe path).
func PolicyNone() cell.Policy {
	return cell.Policy{Name: "none"}
}

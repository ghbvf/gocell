package main

import (
	"github.com/ghbvf/gocell/runtime/bootstrap"
)

// mkTopo builds a validated bootstrap.Topology for tests, panicking on an
// invalid combination. It mirrors bootstrap.NewTopology ("" storageBackend →
// "memory") and is safe to call from table-literal initializers where a
// *testing.T is not in scope. The dev adapter mode is the empty string ""
// (there is no "dev" literal — that value never passed validation).
func mkTopo(adapterMode, storageBackend string, singlePod bool) bootstrap.Topology {
	topo, err := bootstrap.NewTopology(adapterMode, storageBackend, singlePod)
	if err != nil {
		panic(err)
	}
	return topo
}

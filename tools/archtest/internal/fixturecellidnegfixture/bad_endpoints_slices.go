//go:build archtest_fixture

package fixturecellidnegfixture

import "github.com/ghbvf/gocell/kernel/metadata"

// BadEndpointsSlices uses bare string literals as elements of
// EndpointsMeta slice fields (Clients, Invokers, Readers) — each
// element must be flagged by A1 (slice element positions).
var BadEndpointsSlices = &metadata.ContractMeta{
	ID:   "http.sliceclient.v1",
	Kind: "http",
	Endpoints: metadata.EndpointsMeta{
		Clients:  []string{"bareliteralclient"},
		Invokers: []string{"bareliteralinvoker"},
		Readers:  []string{"bareliteralreader"},
	},
}

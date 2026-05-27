//go:build archtest_fixture

package fixturecellidnegfixture

import "github.com/ghbvf/gocell/kernel/metadata"

// BadEndpointsServer uses bare string literals in EndpointsMeta direct
// fields (Server, Publisher, Handler, Provider) — each must be flagged
// by A1 (direct field positions).
var BadEndpointsServer = &metadata.ContractMeta{
	ID:   "http.service.v1",
	Kind: "http",
	Endpoints: metadata.EndpointsMeta{
		Server:    "bareliteralserver",
		Publisher: "bareliteralpublisher",
		Handler:   "bareliteralhandler",
		Provider:  "bareliteralprovider",
	},
}

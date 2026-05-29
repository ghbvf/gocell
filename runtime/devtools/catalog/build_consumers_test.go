package catalog

import (
	"testing"

	"github.com/ghbvf/gocell/kernel/metadata"
)

// TestContractConsumers_ByKind is a white-box test of the kind→consumer-field
// resolution used by the catalog dependency-graph builder. It locks the grpc
// case (mirrors http: consumers = endpoints.clients) alongside the other kinds
// so a future edit cannot silently drop the grpc branch.
func TestContractConsumers_ByKind(t *testing.T) {
	cases := []struct {
		kind string
		ep   metadata.EndpointsMeta
		want []string
	}{
		{"http", metadata.EndpointsMeta{Clients: []string{"a", "b"}}, []string{"a", "b"}},
		{"event", metadata.EndpointsMeta{Subscribers: []string{"a"}}, []string{"a"}},
		{"command", metadata.EndpointsMeta{Invokers: []string{"a"}}, []string{"a"}},
		{"projection", metadata.EndpointsMeta{Readers: []string{"a"}}, []string{"a"}},
		{"grpc", metadata.EndpointsMeta{Clients: []string{"a", "b"}}, []string{"a", "b"}},
		{"websocket", metadata.EndpointsMeta{}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			got := contractConsumers(&metadata.ContractMeta{Kind: tc.kind, Endpoints: tc.ep})
			if len(got) != len(tc.want) {
				t.Fatalf("kind %q: got %v, want %v", tc.kind, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("kind %q: got %v, want %v", tc.kind, got, tc.want)
				}
			}
		})
	}
}

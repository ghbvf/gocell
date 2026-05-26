package contractpath_test

import (
	"strings"
	"testing"

	"github.com/ghbvf/gocell/pkg/contractpath"
)

func TestContractIDToPackagePath(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		contractID string
		want       string
	}{
		{
			name:       "ordinary contract",
			contractID: "event.session.created.v1",
			want:       "generated/contracts/event/session/created/v1",
		},
		{
			name:       "http contract no internal",
			contractID: "http.order.create.v1",
			want:       "generated/contracts/http/order/create/v1",
		},
		{
			name:       "internal segment rewritten mid-path",
			contractID: "http.config.internal.get.v1",
			want:       "generated/contracts/http/config/internalapi/get/v1",
		},
		{
			name:       "internal segment rewritten at second position",
			contractID: "http.internal.devicecommands.list.v1",
			want:       "generated/contracts/http/internalapi/devicecommands/list/v1",
		},
		{
			name:       "multiple internal segments each rewritten",
			contractID: "http.internal.foo.internal.v1",
			want:       "generated/contracts/http/internalapi/foo/internalapi/v1",
		},
		{
			name:       "hyphenated domain segment",
			contractID: "event.order-created.v1",
			want:       "generated/contracts/event/order-created/v1",
		},
		{
			name:       "deep domain path",
			contractID: "event.config.entry-upserted.v1",
			want:       "generated/contracts/event/config/entry-upserted/v1",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := contractpath.ContractIDToPackagePath(tc.contractID)
			if got != tc.want {
				t.Errorf("ContractIDToPackagePath(%q) = %q, want %q", tc.contractID, got, tc.want)
			}
		})
	}
}

// TestSegments asserts the segments helper agrees with ContractIDToPackagePath
// by inversion: joining the segments with "/" under "generated/contracts/"
// must reproduce the joined-string output, so the two callsites (joined-string
// importers in tools/codegen vs []string importer in kernel/governance) cannot
// diverge on internal→internalapi rewriting.
func TestSegmentsAgreesWithJoined(t *testing.T) {
	t.Parallel()
	ids := []string{
		"event.session.created.v1",
		"http.order.create.v1",
		"http.config.internal.get.v1",
		"http.internal.devicecommands.list.v1",
		"http.internal.foo.internal.v1",
	}
	for _, id := range ids {
		joined := "generated/contracts/" + strings.Join(contractpath.Segments(id), "/")
		if got := contractpath.ContractIDToPackagePath(id); got != joined {
			t.Errorf("Segments/ContractIDToPackagePath disagree for %q: joined=%q vs path=%q", id, joined, got)
		}
	}
}

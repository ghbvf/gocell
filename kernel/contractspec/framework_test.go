package contractspec_test

import (
	"strings"
	"testing"

	"github.com/ghbvf/gocell/kernel/cellvocab"
	"github.com/ghbvf/gocell/kernel/contractspec"
)

// TestNewFrameworkHTTP verifies that NewFrameworkHTTP produces a ContractSpec
// with the correct field values for each input combination, and that the
// resulting spec passes Validate() for the valid case.
func TestNewFrameworkHTTP(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		id        string
		method    string
		path      string
		wantValid bool
	}{
		{
			name:      "valid health livez",
			id:        "http.framework.health.livez.v1",
			method:    "GET",
			path:      "/healthz",
			wantValid: true,
		},
		{
			name:      "valid metrics endpoint",
			id:        "http.framework.health.metrics.v1",
			method:    "GET",
			path:      "/metrics",
			wantValid: true,
		},
		{
			name:      "valid devtools catalog",
			id:        "http.framework.devtools.catalog.v1",
			method:    "GET",
			path:      "/api/v1/devtools/catalog",
			wantValid: true,
		},
		{
			name:      "POST method",
			id:        "http.framework.test.post.v1",
			method:    "POST",
			path:      "/api/v1/test",
			wantValid: true,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			spec := contractspec.NewFrameworkHTTP(tc.id, tc.method, tc.path)

			if spec.ID != tc.id {
				t.Errorf("ID = %q, want %q", spec.ID, tc.id)
			}
			if spec.Kind != cellvocab.ContractHTTP {
				t.Errorf("Kind = %q, want %q", spec.Kind, cellvocab.ContractHTTP)
			}
			if spec.Transport != "http" {
				t.Errorf("Transport = %q, want %q", spec.Transport, "http")
			}
			if spec.Method != tc.method {
				t.Errorf("Method = %q, want %q", spec.Method, tc.method)
			}
			if spec.Path != tc.path {
				t.Errorf("Path = %q, want %q", spec.Path, tc.path)
			}
			// Topic must be zero for HTTP specs.
			if spec.Topic != "" {
				t.Errorf("Topic = %q, want empty for http kind", spec.Topic)
			}

			if tc.wantValid {
				if err := spec.Validate(); err != nil {
					t.Errorf("Validate() unexpected error: %v", err)
				}
			}
		})
	}
}

// TestNewFrameworkHTTP_BadPrefixPanics verifies that NewFrameworkHTTP panics
// when the id does not start with FrameworkHTTPIDPrefix. The panic is an
// A-class assertion (programmer error at process initialization).
func TestNewFrameworkHTTP_BadPrefixPanics(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		id   string
	}{
		{"no prefix", "http.health.v1"},
		{"empty id", ""},
		{"wrong prefix", "event.framework.v1"},
		{"prefix without dot", "http.framework"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var recovered any
			func() {
				defer func() { recovered = recover() }()
				_ = contractspec.NewFrameworkHTTP(tc.id, "GET", "/x")
			}()
			if recovered == nil {
				t.Errorf("expected panic for id=%q, got none", tc.id)
			}
			// The panic value should be an error wrapping the assertion message.
			if msg, ok := recovered.(interface{ Error() string }); ok {
				if !strings.Contains(msg.Error(), "FrameworkHTTPIDPrefix") {
					t.Errorf("panic message %q should mention FrameworkHTTPIDPrefix", msg.Error())
				}
			}
		})
	}
}

// TestNewEventDerivation verifies that NewEventDerivation produces a
// ContractSpec with the correct field values for each input combination, and
// that the resulting spec passes Validate() for a valid event kind + topic.
func TestNewEventDerivation(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		id        string
		kind      cellvocab.ContractKind
		transport string
		topic     string
		wantValid bool
	}{
		{
			name:      "valid amqp event",
			id:        "event.session.created.v1",
			kind:      cellvocab.ContractEvent,
			transport: "amqp",
			topic:     "session.created.v1",
			wantValid: true,
		},
		{
			name:      "valid internal event",
			id:        "event.config.entry-upserted.v1",
			kind:      cellvocab.ContractEvent,
			transport: "internal",
			topic:     "config.entry-upserted.v1",
			wantValid: true,
		},
		{
			name:      "projection kind — no method or path",
			id:        "projection.session.view.v1",
			kind:      cellvocab.ContractProjection,
			transport: "internal",
			topic:     "",
			wantValid: false, // projection kind skips event validation; topic empty is fine
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			spec := contractspec.NewEventDerivation(tc.id, tc.kind, tc.transport, tc.topic)

			if spec.ID != tc.id {
				t.Errorf("ID = %q, want %q", spec.ID, tc.id)
			}
			if spec.Kind != tc.kind {
				t.Errorf("Kind = %q, want %q", spec.Kind, tc.kind)
			}
			if spec.Transport != tc.transport {
				t.Errorf("Transport = %q, want %q", spec.Transport, tc.transport)
			}
			if spec.Topic != tc.topic {
				t.Errorf("Topic = %q, want %q", spec.Topic, tc.topic)
			}
			// Method and Path must be zero for event derivations.
			if spec.Method != "" {
				t.Errorf("Method = %q, want empty for event derivation", spec.Method)
			}
			if spec.Path != "" {
				t.Errorf("Path = %q, want empty for event derivation", spec.Path)
			}
			// Clients must be nil for event derivations.
			if len(spec.Clients) != 0 {
				t.Errorf("Clients = %v, want empty for event derivation", spec.Clients)
			}

			if tc.wantValid {
				if err := spec.Validate(); err != nil {
					t.Errorf("Validate() unexpected error: %v", err)
				}
			}
		})
	}
}

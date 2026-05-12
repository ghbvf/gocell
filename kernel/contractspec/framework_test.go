package contractspec_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/kernel/cellvocab"
	"github.com/ghbvf/gocell/kernel/contractspec"
)

// assertSpecEqual reports whether got and want match on every public
// ContractSpec field. Per-field comparison preserves "which field drifted"
// readability when a test fails; the helper exists so callers stay below the
// cognitive-complexity ceiling enforced by go-standards.md (≤15).
//
// Sonar / gocyclo accounts for the inline ifs against this helper rather
// than the caller test function — keeping table-driven test bodies trivial.
func assertSpecEqual(t *testing.T, got, want contractspec.ContractSpec) {
	t.Helper()
	if got.ID != want.ID {
		t.Errorf("ID = %q, want %q", got.ID, want.ID)
	}
	if got.Kind != want.Kind {
		t.Errorf("Kind = %q, want %q", got.Kind, want.Kind)
	}
	if got.Transport != want.Transport {
		t.Errorf("Transport = %q, want %q", got.Transport, want.Transport)
	}
	if got.Method != want.Method {
		t.Errorf("Method = %q, want %q", got.Method, want.Method)
	}
	if got.Path != want.Path {
		t.Errorf("Path = %q, want %q", got.Path, want.Path)
	}
	if got.Topic != want.Topic {
		t.Errorf("Topic = %q, want %q", got.Topic, want.Topic)
	}
	if len(got.Clients) != len(want.Clients) {
		t.Errorf("Clients length = %d, want %d", len(got.Clients), len(want.Clients))
	}
}

// TestNewFrameworkHTTP verifies that NewFrameworkHTTP produces a ContractSpec
// with the correct field values for each input combination, and that the
// resulting spec passes Validate(). The bad-prefix panic path is covered
// separately by TestNewFrameworkHTTP_BadPrefixPanics.
func TestNewFrameworkHTTP(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		id     string
		method string
		path   string
		want   contractspec.ContractSpec
	}{
		{
			name:   "valid health livez",
			id:     "http.framework.health.livez.v1",
			method: "GET",
			path:   "/healthz",
			want: contractspec.ContractSpec{
				ID:        "http.framework.health.livez.v1",
				Kind:      cellvocab.ContractHTTP,
				Transport: "http",
				Method:    "GET",
				Path:      "/healthz",
			},
		},
		{
			name:   "valid metrics endpoint",
			id:     "http.framework.health.metrics.v1",
			method: "GET",
			path:   "/metrics",
			want: contractspec.ContractSpec{
				ID:        "http.framework.health.metrics.v1",
				Kind:      cellvocab.ContractHTTP,
				Transport: "http",
				Method:    "GET",
				Path:      "/metrics",
			},
		},
		{
			name:   "valid devtools catalog",
			id:     "http.framework.devtools.catalog.v1",
			method: "GET",
			path:   "/api/v1/devtools/catalog",
			want: contractspec.ContractSpec{
				ID:        "http.framework.devtools.catalog.v1",
				Kind:      cellvocab.ContractHTTP,
				Transport: "http",
				Method:    "GET",
				Path:      "/api/v1/devtools/catalog",
			},
		},
		{
			name:   "POST method",
			id:     "http.framework.test.post.v1",
			method: "POST",
			path:   "/api/v1/test",
			want: contractspec.ContractSpec{
				ID:        "http.framework.test.post.v1",
				Kind:      cellvocab.ContractHTTP,
				Transport: "http",
				Method:    "POST",
				Path:      "/api/v1/test",
			},
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := contractspec.NewFrameworkHTTP(tc.id, tc.method, tc.path)
			assertSpecEqual(t, got, tc.want)
			if err := got.Validate(); err != nil {
				t.Errorf("Validate() unexpected error: %v", err)
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

// TestNewEventDerivation covers the funnel's success path: a valid event /
// projection spec should be returned with no error and match the expected
// shape. The bad-input path is covered by TestNewEventDerivation_Invalid.
func TestNewEventDerivation(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		id        string
		kind      cellvocab.ContractKind
		transport string
		topic     string
		want      contractspec.ContractSpec
	}{
		{
			name:      "valid amqp event",
			id:        "event.session.created.v1",
			kind:      cellvocab.ContractEvent,
			transport: "amqp",
			topic:     "session.created.v1",
			want: contractspec.ContractSpec{
				ID:        "event.session.created.v1",
				Kind:      cellvocab.ContractEvent,
				Transport: "amqp",
				Topic:     "session.created.v1",
			},
		},
		{
			name:      "valid internal event",
			id:        "event.config.entry-upserted.v1",
			kind:      cellvocab.ContractEvent,
			transport: "internal",
			topic:     "config.entry-upserted.v1",
			want: contractspec.ContractSpec{
				ID:        "event.config.entry-upserted.v1",
				Kind:      cellvocab.ContractEvent,
				Transport: "internal",
				Topic:     "config.entry-upserted.v1",
			},
		},
		{
			name:      "valid projection — no topic required by validator",
			id:        "projection.session.view.v1",
			kind:      cellvocab.ContractProjection,
			transport: "internal",
			topic:     "",
			want: contractspec.ContractSpec{
				ID:        "projection.session.view.v1",
				Kind:      cellvocab.ContractProjection,
				Transport: "internal",
				Topic:     "",
			},
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := contractspec.NewEventDerivation(tc.id, tc.kind, tc.transport, tc.topic)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			assertSpecEqual(t, got, tc.want)
		})
	}
}

// TestNewEventDerivation_Invalid verifies the funnel rejects malformed
// inputs by returning a wrapped error (NOT panic — content invariant lives
// in spec.Validate(), not in a panic guard). Each case targets a distinct
// validation path.
func TestNewEventDerivation_Invalid(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		id        string
		kind      cellvocab.ContractKind
		transport string
		topic     string
		wantMsg   string
	}{
		{
			name:      "empty id",
			id:        "",
			kind:      cellvocab.ContractEvent,
			transport: "amqp",
			topic:     "session.created.v1",
			wantMsg:   "ID must not be empty",
		},
		{
			name:      "event kind missing topic",
			id:        "event.session.created.v1",
			kind:      cellvocab.ContractEvent,
			transport: "amqp",
			topic:     "",
			wantMsg:   "event kind requires Topic",
		},
		{
			name:      "unrecognized kind",
			id:        "garbage.kind.v1",
			kind:      cellvocab.ContractKind("garbage"),
			transport: "amqp",
			topic:     "garbage.topic",
			wantMsg:   "not recognized",
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := contractspec.NewEventDerivation(tc.id, tc.kind, tc.transport, tc.topic)
			if err == nil {
				t.Fatalf("expected error, got nil (spec=%+v)", got)
			}
			if !strings.Contains(err.Error(), tc.wantMsg) {
				t.Errorf("error message = %q, want substring %q", err.Error(), tc.wantMsg)
			}
			if !strings.Contains(err.Error(), "NewEventDerivation") {
				t.Errorf("error message = %q, want funnel context %q", err.Error(), "NewEventDerivation")
			}
			// Returned spec must be zero on error (fail-closed contract).
			// assertSpecEqual checks every field including Method/Path/Clients,
			// guarding against future leaks on the error path.
			assertSpecEqual(t, got, contractspec.ContractSpec{})
			// errors.Is contract: underlying validator error is wrapped via %w.
			if errors.Unwrap(err) == nil {
				t.Errorf("error is not wrapping a cause; expected %%w chain")
			}
		})
	}
}

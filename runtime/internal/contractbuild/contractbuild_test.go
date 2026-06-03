package contractbuild_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/kernel/cellvocab"
	"github.com/ghbvf/gocell/kernel/contractspec"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/webhook"
	"github.com/ghbvf/gocell/runtime/internal/contractbuild"
)

// assertSpecEqual reports whether got and want match on every public
// ContractSpec field. Per-field comparison preserves "which field drifted"
// readability when a test fails; the helper exists so callers stay below the
// cognitive-complexity ceiling enforced by go-standards.md (≤15).
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
	if !slices.Equal(got.Clients, want.Clients) {
		t.Errorf("Clients = %v, want %v", got.Clients, want.Clients)
	}
}

// TestNewFrameworkHTTP verifies that NewFrameworkHTTP produces a ContractSpec
// with the correct field values for each input combination, and that the
// resulting spec passes Validate(). The bad-prefix panic path is covered
// separately by TestNewFrameworkHTTP_BadPrefixPanics.
func TestNewFrameworkHTTP(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		id      string
		method  string
		path    string
		clients []string
		want    contractspec.ContractSpec
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
		{
			// Internal control-plane framework endpoint: an /internal/ path requires
			// a non-empty Clients allowlist (ContractSpec.validateHTTP invariant), so
			// the variadic clients carry the caller-cell allowlist into the spec.
			name:    "internal endpoint with caller-cell allowlist",
			id:      "http.framework.example.control.v1",
			method:  "POST",
			path:    "/internal/v1/example/control",
			clients: []string{"controlplane"},
			want: contractspec.ContractSpec{
				ID:        "http.framework.example.control.v1",
				Kind:      cellvocab.ContractHTTP,
				Transport: "http",
				Method:    "POST",
				Path:      "/internal/v1/example/control",
				Clients:   []string{"controlplane"},
			},
		},
		{
			// Admin control-plane framework endpoint (#1505 projection rebuild): an
			// /admin/v1/* path is an ordinary non-internal path, so it carries NO
			// Clients (validateHTTP forbids Clients on non-internal paths). Auth is
			// the AdminListener operator-credential gate, not a caller-cell allowlist.
			name:   "admin endpoint without caller-cell allowlist",
			id:     "http.framework.projection.rebuild.v1",
			method: "POST",
			path:   "/admin/v1/projection/{cell}/{name}/rebuild",
			want: contractspec.ContractSpec{
				ID:        "http.framework.projection.rebuild.v1",
				Kind:      cellvocab.ContractHTTP,
				Transport: "http",
				Method:    "POST",
				Path:      "/admin/v1/projection/{cell}/{name}/rebuild",
			},
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := contractbuild.NewFrameworkHTTP(tc.id, tc.method, tc.path, tc.clients...)
			assertSpecEqual(t, got, tc.want)
			if err := got.Validate(); err != nil {
				t.Errorf("Validate() unexpected error: %v", err)
			}
		})
	}
}

// TestNewFrameworkHTTP_BadPrefixPanics verifies that NewFrameworkHTTP panics
// when the id does not start with the required framework prefix. The panic is
// an A-class assertion (programmer error at process initialization).
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
				_ = contractbuild.NewFrameworkHTTP(tc.id, "GET", "/x")
			}()
			if recovered == nil {
				t.Fatalf("expected panic for id=%q, got none", tc.id)
			}
			// The panic value must be an error (panicregister.Approved wraps an
			// errcode.Assertion). A non-error payload is a contract violation.
			err, ok := recovered.(error)
			if !ok {
				t.Fatalf("panic value should be an error, got %T", recovered)
			}
			if !strings.Contains(err.Error(), "http.framework.") {
				t.Errorf("panic message %q should mention the required prefix", err.Error())
			}
		})
	}
}

// validEventSub returns a fully-populated, Validate-passing outbox.Subscription
// for the success-path table; individual cases mutate one field to exercise a
// distinct branch.
func validEventSub() outbox.Subscription {
	return outbox.Subscription{
		Topic:             "session.created.v1",
		ConsumerGroup:     "accesscore",
		CellID:            "accesscore",
		ContractID:        "event.session.created.v1",
		ContractKind:      string(cellvocab.ContractEvent),
		ContractTransport: "amqp",
	}
}

// TestNewEventDerivation covers the funnel's success path: a valid Subscription
// is projected into the expected ContractSpec. The bad-input path is covered by
// TestNewEventDerivation_Invalid.
func TestNewEventDerivation(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		sub  outbox.Subscription
		want contractspec.ContractSpec
	}{
		{
			name: "valid amqp event",
			sub:  validEventSub(),
			want: contractspec.ContractSpec{
				ID:        "event.session.created.v1",
				Kind:      cellvocab.ContractEvent,
				Transport: "amqp",
				Topic:     "session.created.v1",
			},
		},
		{
			name: "valid internal event",
			sub: outbox.Subscription{
				Topic:             "config.entry-upserted.v1",
				ConsumerGroup:     "configcore",
				CellID:            "configcore",
				ContractID:        "event.config.entry-upserted.v1",
				ContractKind:      string(cellvocab.ContractEvent),
				ContractTransport: "internal",
			},
			want: contractspec.ContractSpec{
				ID:        "event.config.entry-upserted.v1",
				Kind:      cellvocab.ContractEvent,
				Transport: "internal",
				Topic:     "config.entry-upserted.v1",
			},
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := contractbuild.NewEventDerivation(tc.sub)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			assertSpecEqual(t, got, tc.want)
		})
	}
}

// TestNewWebhookDispatch verifies that NewWebhookDispatch produces the expected
// ContractSpec from a valid DispatchSpec and rejects invalid input.
func TestNewWebhookDispatch(t *testing.T) {
	// All wantErr cases below exercise the first layer (spec.Validate()): a
	// DispatchSpec has only three required fields and any empty one fails there.
	// The funnel's second layer (cs.Validate()) is unreachable-as-failure given
	// the hardcoded valid Kind/Transport and the non-empty ID/Topic derived from
	// the already-validated ContractID (see NewWebhookDispatch godoc), so there is
	// no independent layer-2 case to add — unlike NewEventDerivation, whose Kind
	// comes from caller data and therefore needs a badKind case.
	t.Parallel()
	cases := []struct {
		name    string
		spec    webhook.DispatchSpec
		wantErr bool
		want    contractspec.ContractSpec
	}{
		{
			name: "valid dispatch spec",
			spec: webhook.DispatchSpec{
				ContractID: "event.webhook.foo.v1",
				SourceID:   "src",
				CellID:     "mycell",
			},
			wantErr: false,
			want: contractspec.ContractSpec{
				ID:        "event.webhook.foo.v1",
				Kind:      cellvocab.ContractEvent,
				Transport: "amqp",
				Topic:     "event.webhook.foo.v1",
			},
		},
		{
			name: "invalid dispatch spec — empty ContractID",
			spec: webhook.DispatchSpec{
				ContractID: "",
				SourceID:   "src",
				CellID:     "mycell",
			},
			wantErr: true,
		},
		{
			name: "invalid dispatch spec — empty SourceID",
			spec: webhook.DispatchSpec{
				ContractID: "event.webhook.foo.v1",
				SourceID:   "",
				CellID:     "mycell",
			},
			wantErr: true,
		},
		{
			name: "invalid dispatch spec — empty CellID",
			spec: webhook.DispatchSpec{
				ContractID: "event.webhook.foo.v1",
				SourceID:   "src",
				CellID:     "",
			},
			wantErr: true,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := contractbuild.NewWebhookDispatch(tc.spec)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil (spec=%+v)", got)
				}
				// On error the returned spec must be zero (fail-closed contract).
				assertSpecEqual(t, got, contractspec.ContractSpec{})
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			assertSpecEqual(t, got, tc.want)
			if err := got.Validate(); err != nil {
				t.Errorf("Validate() unexpected error: %v", err)
			}
		})
	}
}

// TestNewEventDerivation_Invalid verifies the funnel rejects malformed input by
// returning a wrapped error (NOT panic). Two validation layers are exercised:
// (a) sub.Validate() — the shape gate, rejecting a structurally-invalid
// Subscription (it does NOT gate value provenance — exported fields let a caller
// fabricate a valid-shaped sub; see contractbuild.go); (b) the derived
// ContractSpec.Validate() — defense in depth when a Subscription validates but
// its contract identity is not a valid spec.
func TestNewEventDerivation_Invalid(t *testing.T) {
	t.Parallel()
	missingTopic := validEventSub()
	missingTopic.Topic = ""
	missingContractID := validEventSub()
	missingContractID.ContractID = ""
	badKind := validEventSub() // valid Subscription, but kind is not a real ContractKind
	badKind.ContractKind = "garbage"

	cases := []struct {
		name    string
		sub     outbox.Subscription
		wantMsg string
		// subLayer asserts the error came from sub.Validate (shape gate)
		// rather than the derived spec.Validate (defense in depth).
		subLayer bool
	}{
		{
			name:     "subscription missing topic — shape gate",
			sub:      missingTopic,
			wantMsg:  "Topic must not be empty",
			subLayer: true,
		},
		{
			name:     "subscription missing contractID — shape gate",
			sub:      missingContractID,
			wantMsg:  "ContractID must not be empty",
			subLayer: true,
		},
		{
			name:     "valid subscription, unrecognized kind — spec defense in depth",
			sub:      badKind,
			wantMsg:  "not recognized",
			subLayer: false,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := contractbuild.NewEventDerivation(tc.sub)
			if err == nil {
				t.Fatalf("expected error, got nil (spec=%+v)", got)
			}
			if !strings.Contains(err.Error(), tc.wantMsg) {
				t.Errorf("error message = %q, want substring %q", err.Error(), tc.wantMsg)
			}
			if !strings.Contains(err.Error(), "NewEventDerivation") {
				t.Errorf("error message = %q, want funnel context %q", err.Error(), "NewEventDerivation")
			}
			if gotSubLayer := strings.Contains(err.Error(), "subscription invalid"); gotSubLayer != tc.subLayer {
				t.Errorf("validation layer mismatch: err=%q subLayer=%v want=%v", err.Error(), gotSubLayer, tc.subLayer)
			}
			// Returned spec must be zero on error (fail-closed contract).
			assertSpecEqual(t, got, contractspec.ContractSpec{})
			// errors.Is contract: underlying validator error is wrapped via %w.
			if errors.Unwrap(err) == nil {
				t.Errorf("error is not wrapping a cause; expected %%w chain")
			}
		})
	}
}

package bootstrap

// event_transport_kind_test.go — tests for the sealed EventTransportKind fact.
//
// INVARIANT: EVENT-TRANSPORT-KIND-SEALED-FIELD-FROZEN-01

import (
	"reflect"
	"testing"
)

// ---------------------------------------------------------------------------
// EVENT-TRANSPORT-KIND-SEALED-FIELD-FROZEN-01
// ---------------------------------------------------------------------------

// TestEventTransportKindZeroExportedFields enforces the sealed-construction
// invariant: an EventTransportKind must have zero exported fields so the only
// way to mint one is via InMemoryEventTransport / RealBrokerEventTransport. If
// any field were exported, package-external code could forge a real-broker kind
// via a struct literal (e.g. EventTransportKind{RealBroker: true}), defeating
// the seal that the phase0 broker gate trusts (INVARIANT:
// EVENT-TRANSPORT-KIND-SEALED-FIELD-FROZEN-01).
func TestEventTransportKindZeroExportedFields(t *testing.T) {
	rt := reflect.TypeOf(EventTransportKind{})
	for i := 0; i < rt.NumField(); i++ {
		if f := rt.Field(i); f.IsExported() {
			t.Errorf("EventTransportKind.%s is exported; all fields must be unexported so the only "+
				"mint path is InMemoryEventTransport / RealBrokerEventTransport (sealed)", f.Name)
		}
	}
}

// TestEventTransportKindExpectedUnexportedFields freezes the exact unexported
// field set (anti-drift companion to the exported-zero test above).
func TestEventTransportKindExpectedUnexportedFields(t *testing.T) {
	wantFields := map[string]bool{
		"set":        false,
		"realBroker": false,
	}
	rt := reflect.TypeOf(EventTransportKind{})
	for i := 0; i < rt.NumField(); i++ {
		name := rt.Field(i).Name
		if _, ok := wantFields[name]; !ok {
			t.Errorf("unexpected field %q in EventTransportKind; update EVENT-TRANSPORT-KIND-SEALED-FIELD-FROZEN-01 test", name)
		}
		wantFields[name] = true
	}
	for name, seen := range wantFields {
		if !seen {
			t.Errorf("expected field %q not found in EventTransportKind", name)
		}
	}
}

// ---------------------------------------------------------------------------
// IsRealBroker semantics (unset = fail-closed)
// ---------------------------------------------------------------------------

// TestEventTransportKind_IsRealBroker covers all three states: the zero value
// (unset) must report false so the broker gate fails closed when a composition
// root forgot to declare the kind; in-memory reports false; real-broker true.
func TestEventTransportKind_IsRealBroker(t *testing.T) {
	cases := []struct {
		name string
		kind EventTransportKind
		want bool
	}{
		{name: "zero value (unset) → false (fail-closed)", kind: EventTransportKind{}, want: false},
		{name: "in-memory → false", kind: InMemoryEventTransport(), want: false},
		{name: "real broker → true", kind: RealBrokerEventTransport(), want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.kind.IsRealBroker(); got != tc.want {
				t.Errorf("IsRealBroker() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestEventTransportKind_String covers the diagnostic rendering used by the
// gate's errcode internal attribute.
func TestEventTransportKind_String(t *testing.T) {
	cases := []struct {
		kind EventTransportKind
		want string
	}{
		{kind: EventTransportKind{}, want: "unset"},
		{kind: InMemoryEventTransport(), want: "in-memory"},
		{kind: RealBrokerEventTransport(), want: "real-broker"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			if got := tc.kind.String(); got != tc.want {
				t.Errorf("String() = %q, want %q", got, tc.want)
			}
		})
	}
}

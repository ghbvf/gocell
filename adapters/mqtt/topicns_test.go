package mqtt

import (
	"errors"
	"reflect"
	"testing"

	"github.com/ghbvf/gocell/pkg/errcode"
)

func TestParseTopicNamespace_Valid(t *testing.T) {
	tests := []struct {
		name string
		ns   string
	}{
		{"simple", "devices"},
		{"multi-level", "devices/sensors"},
		{"with-underscores", "my_device"},
		{"with-hyphens", "my-device"},
		{"deep", "a/b/c/d"},
		{"alphanumeric", "dev01"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ns, err := ParseTopicNamespace(tc.ns)
			if err != nil {
				t.Fatalf("ParseTopicNamespace(%q) unexpected error: %v", tc.ns, err)
			}
			if ns.String() != tc.ns {
				t.Errorf("String() = %q, want %q", ns.String(), tc.ns)
			}
		})
	}
}

func TestParseTopicNamespace_Invalid(t *testing.T) {
	tests := []struct {
		name string
		ns   string
	}{
		{"empty", ""},
		{"leading-slash", "/devices"},
		{"trailing-slash", "devices/"},
		{"wildcard-plus", "devices/+"},
		{"wildcard-hash", "devices/#"},
		{"uppercase", "Devices"},
		{"space", "dev ices"},
		{"too-long", func() string {
			b := make([]byte, 129)
			for i := range b {
				b[i] = 'a'
			}
			return string(b)
		}()},
		{"empty-segment", "devices//sensors"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseTopicNamespace(tc.ns)
			if err == nil {
				t.Errorf("ParseTopicNamespace(%q) expected error, got nil", tc.ns)
			}
		})
	}
}

func TestTopicNamespace_PublishOK(t *testing.T) {
	ns, err := ParseTopicNamespace("devices/sensors")
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	tests := []struct {
		name    string
		topic   string
		wantErr bool
	}{
		{"exact-prefix", "devices/sensors", false},
		{"subpath", "devices/sensors/temp", false},
		{"deeper-subpath", "devices/sensors/room1/temp", false},
		{"sibling-ns", "devices/actuators", true},
		{"unrelated", "other/topic", true},
		{"prefix-substring-not-boundary", "devices/sensors2", true},
		{"prefix-without-slash", "devices", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ns.PublishOK(tc.topic)
			if (err != nil) != tc.wantErr {
				t.Errorf("PublishOK(%q) error = %v, wantErr = %v", tc.topic, err, tc.wantErr)
			}
		})
	}
}

func TestTopicNamespace_PublishOK_InvalidPublishTopicCode(t *testing.T) {
	t.Parallel()
	ns, err := ParseTopicNamespace("ns")
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	for _, topic := range []string{"", "ns/+", "ns/#", "ns/a/+"} {
		topic := topic
		t.Run(topic, func(t *testing.T) {
			t.Parallel()
			err := ns.PublishOK(topic)
			if err == nil {
				t.Fatalf("PublishOK(%q) expected error, got nil", topic)
			}
			var ec *errcode.Error
			if !errors.As(err, &ec) {
				t.Fatalf("expected *errcode.Error, got %T: %v", err, err)
			}
			if ec.Code != ErrAdapterMQTTInvalidPublishTopic {
				t.Errorf("code = %s, want %s", ec.Code, ErrAdapterMQTTInvalidPublishTopic)
			}
		})
	}
}

func TestTopicNamespace_SubscribeOK(t *testing.T) {
	ns, err := ParseTopicNamespace("ns")
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	tests := []struct {
		name    string
		filter  string
		wantErr bool
	}{
		{"exact", "ns", false},
		{"subpath", "ns/a/b", false},
		{"single-level-wildcard", "ns/+/x", false},
		{"multi-level-wildcard-tail", "ns/#", false},
		{"multi-level-in-path", "ns/a/#", false},
		{"hash-not-at-tail", "ns/#/x", true},
		{"outside-namespace", "other/#", true},
		{"prefix-not-boundary", "ns2", true},
		{"plus-not-lone-token", "ns/ab+c", true},
		{"hash-not-lone-token", "ns/ab#c", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ns.SubscribeOK(tc.filter)
			if (err != nil) != tc.wantErr {
				t.Errorf("SubscribeOK(%q) error = %v, wantErr = %v", tc.filter, err, tc.wantErr)
			}
		})
	}
}

// TestTopicNamespace_ZeroReceiver_Rejected covers R2 round-2 finding:
// `var ns TopicNamespace; ns.PublishOK("...")` previously returned nil for
// topic == "" or any prefix match against empty namespace, because the
// PublishOK / SubscribeOK methods accept zero-value receivers structurally.
// The fix asserts both methods reject the zero-value receiver with
// ErrAdapterMQTTInvalidTopicNamespace before any other check.
func TestTopicNamespace_ZeroReceiver_Rejected(t *testing.T) {
	t.Parallel()
	var zero TopicNamespace

	cases := []struct {
		name string
		fn   func() error
	}{
		{"PublishOK-empty-topic", func() error { return zero.PublishOK("") }},
		{"PublishOK-nonempty-topic", func() error { return zero.PublishOK("any/topic") }},
		{"SubscribeOK-empty-filter", func() error { return zero.SubscribeOK("") }},
		{"SubscribeOK-nonempty-filter", func() error { return zero.SubscribeOK("any/+/#") }},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.fn()
			if err == nil {
				t.Fatalf("zero-value receiver %s expected error, got nil", tc.name)
			}
			var ec *errcode.Error
			if !errors.As(err, &ec) {
				t.Fatalf("expected *errcode.Error, got %T: %v", err, err)
			}
			if ec.Code != ErrAdapterMQTTInvalidTopicNamespace {
				t.Errorf("zero-value receiver %s: code = %s, want %s",
					tc.name, ec.Code, ErrAdapterMQTTInvalidTopicNamespace)
			}
		})
	}
}

// TestTopicNamespace_EmptyTopicAndFilter_Rejected covers MQTT v5 §4.7.3
// (forbid empty topic name) for PublishOK and the parallel empty-filter
// rejection for SubscribeOK.
func TestTopicNamespace_EmptyTopicAndFilter_Rejected(t *testing.T) {
	t.Parallel()
	ns, err := ParseTopicNamespace("ns")
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	if err := ns.PublishOK(""); err == nil {
		t.Error("PublishOK(empty) must be rejected")
	}
	if err := ns.SubscribeOK(""); err == nil {
		t.Error("SubscribeOK(empty) must be rejected")
	}
}

func TestTopicNamespace_PublishOK_PrefixBoundary(t *testing.T) {
	// "ns" should not match "ns2" or "nsx"
	ns, _ := ParseTopicNamespace("ns")
	if err := ns.PublishOK("ns2"); err == nil {
		t.Error("PublishOK(ns2) should fail: ns is not a prefix of ns2 at boundary")
	}
	if err := ns.PublishOK("nsx/sub"); err == nil {
		t.Error("PublishOK(nsx/sub) should fail: ns is not a prefix of nsx at boundary")
	}
	if err := ns.PublishOK("ns/sub"); err != nil {
		t.Errorf("PublishOK(ns/sub) should succeed: %v", err)
	}
}

// TestTopicNamespace_SubscribeOK_WildcardErrorCodes verifies that wildcard
// placement errors return ErrAdapterMQTTInvalidSubscribeFilter (not the
// namespace-invalid code) and that outside-namespace errors return
// ErrAdapterMQTTTopicOutsideNamespace.
func TestTopicNamespace_SubscribeOK_WildcardErrorCodes(t *testing.T) {
	ns, err := ParseTopicNamespace("ns")
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	tests := []struct {
		name     string
		filter   string
		wantCode errcode.Code
	}{
		{
			name:     "hash-not-at-tail returns InvalidSubscribeFilter",
			filter:   "ns/#/x",
			wantCode: ErrAdapterMQTTInvalidSubscribeFilter,
		},
		{
			name:     "plus-not-lone-token returns InvalidSubscribeFilter",
			filter:   "ns/ab+c",
			wantCode: ErrAdapterMQTTInvalidSubscribeFilter,
		},
		{
			name:     "hash-not-lone-token returns InvalidSubscribeFilter",
			filter:   "ns/ab#c",
			wantCode: ErrAdapterMQTTInvalidSubscribeFilter,
		},
		{
			name:     "outside-namespace returns TopicOutsideNamespace",
			filter:   "other/#",
			wantCode: ErrAdapterMQTTTopicOutsideNamespace,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ns.SubscribeOK(tc.filter)
			if err == nil {
				t.Fatalf("SubscribeOK(%q) expected error, got nil", tc.filter)
			}
			var ec *errcode.Error
			if !errors.As(err, &ec) {
				t.Fatalf("SubscribeOK(%q) error is not *errcode.Error: %T", tc.filter, err)
			}
			if ec.Code != tc.wantCode {
				t.Errorf("SubscribeOK(%q) code = %v, want %v", tc.filter, ec.Code, tc.wantCode)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// publishableTopic / TopicNamespace.Mint tests (B3a)
// ---------------------------------------------------------------------------

// TestTopicNamespace_Mint_Success verifies that Mint returns a non-zero
// publishableTopic with String() == the input topic when the topic passes
// PublishOK validation.
func TestTopicNamespace_Mint_Success(t *testing.T) {
	t.Parallel()
	ns, err := ParseTopicNamespace("ns")
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	pt, err := ns.Mint("ns/foo")
	if err != nil {
		t.Fatalf("Mint(%q) unexpected error: %v", "ns/foo", err)
	}
	if pt.String() != "ns/foo" {
		t.Errorf("publishableTopic.String() = %q, want %q", pt.String(), "ns/foo")
	}
	// Also verify exact-topic (prefix == topic) succeeds.
	pt2, err := ns.Mint("ns")
	if err != nil {
		t.Fatalf("Mint(%q) unexpected error: %v", "ns", err)
	}
	if pt2.String() != "ns" {
		t.Errorf("publishableTopic.String() = %q, want %q", pt2.String(), "ns")
	}
}

// TestTopicNamespace_Mint_PublishOKFailure verifies that Mint of an
// out-of-namespace topic returns the zero publishableTopic and a non-nil error
// with code ErrAdapterMQTTTopicOutsideNamespace.
func TestTopicNamespace_Mint_PublishOKFailure(t *testing.T) {
	t.Parallel()
	ns, err := ParseTopicNamespace("ns1")
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	pt, err := ns.Mint("ns2/x")
	if err == nil {
		t.Fatalf("Mint(out-of-namespace) expected error, got nil")
	}
	if pt != (publishableTopic{}) {
		t.Errorf("Mint(out-of-namespace) returned non-zero publishableTopic: %v", pt)
	}
	var ec *errcode.Error
	if !errors.As(err, &ec) {
		t.Fatalf("expected *errcode.Error, got %T: %v", err, err)
	}
	if ec.Code != ErrAdapterMQTTTopicOutsideNamespace {
		t.Errorf("code = %s, want %s", ec.Code, ErrAdapterMQTTTopicOutsideNamespace)
	}
}

// TestTopicNamespace_Mint_Empty verifies that Mint of an empty string returns
// an error with code ErrAdapterMQTTInvalidPublishTopic (per PublishOK rule for
// empty topics).
func TestTopicNamespace_Mint_Empty(t *testing.T) {
	t.Parallel()
	ns, err := ParseTopicNamespace("ns")
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	_, err = ns.Mint("")
	if err == nil {
		t.Fatal("Mint(empty) expected error, got nil")
	}
	var ec *errcode.Error
	if !errors.As(err, &ec) {
		t.Fatalf("expected *errcode.Error, got %T: %v", err, err)
	}
	if ec.Code != ErrAdapterMQTTInvalidPublishTopic {
		t.Errorf("code = %s, want %s", ec.Code, ErrAdapterMQTTInvalidPublishTopic)
	}
}

// TestTopicNamespace_Mint_Wildcard verifies that Mint of a wildcard topic
// returns an error with code ErrAdapterMQTTInvalidPublishTopic.
func TestTopicNamespace_Mint_Wildcard(t *testing.T) {
	t.Parallel()
	ns, err := ParseTopicNamespace("ns")
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	for _, wc := range []string{"ns/+", "ns/#", "ns/a/+"} {
		wc := wc
		t.Run(wc, func(t *testing.T) {
			t.Parallel()
			_, err := ns.Mint(wc)
			if err == nil {
				t.Fatalf("Mint(%q) expected error, got nil", wc)
			}
			var ec *errcode.Error
			if !errors.As(err, &ec) {
				t.Fatalf("expected *errcode.Error, got %T: %v", err, err)
			}
			if ec.Code != ErrAdapterMQTTInvalidPublishTopic {
				t.Errorf("code = %s, want %s", ec.Code, ErrAdapterMQTTInvalidPublishTopic)
			}
		})
	}
}

// TestTopicNamespace_Mint_ZeroNS verifies that calling Mint on a zero-value
// TopicNamespace returns an error with code ErrAdapterMQTTInvalidTopicNamespace.
func TestTopicNamespace_Mint_ZeroNS(t *testing.T) {
	t.Parallel()
	var ns TopicNamespace
	_, err := ns.Mint("x")
	if err == nil {
		t.Fatal("Mint on zero-value namespace expected error, got nil")
	}
	var ec *errcode.Error
	if !errors.As(err, &ec) {
		t.Fatalf("expected *errcode.Error, got %T: %v", err, err)
	}
	if ec.Code != ErrAdapterMQTTInvalidTopicNamespace {
		t.Errorf("code = %s, want %s", ec.Code, ErrAdapterMQTTInvalidTopicNamespace)
	}
}

// ---------------------------------------------------------------------------
// subscribableFilter / TopicNamespace.MintFilter tests (PR-3 foundation)
// ---------------------------------------------------------------------------

// TestTopicNamespace_MintFilter_Success verifies that MintFilter returns a
// subscribableFilter whose wireFilter is the MQTT v5 "$share/{group}/{filter}"
// shared-subscription form (matchFilter was removed when dispatch moved to
// Subscription Identifiers).
func TestTopicNamespace_MintFilter_Success(t *testing.T) {
	t.Parallel()
	ns, err := ParseTopicNamespace("ns")
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	tests := []struct {
		name     string
		group    string
		filter   string
		wantWire string
	}{
		{"exact", "cg1", "ns", "$share/cg1/ns"},
		{"subpath", "cg1", "ns/a/b", "$share/cg1/ns/a/b"},
		{"single-level-wildcard", "cg2", "ns/+/x", "$share/cg2/ns/+/x"},
		{"multi-level-wildcard", "cg3", "ns/#", "$share/cg3/ns/#"},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f, err := ns.MintFilter(tc.group, tc.filter)
			if err != nil {
				t.Fatalf("MintFilter(%q,%q) unexpected error: %v", tc.group, tc.filter, err)
			}
			if f.wireFilter != tc.wantWire {
				t.Errorf("wireFilter = %q, want %q", f.wireFilter, tc.wantWire)
			}
			if f.String() != tc.wantWire {
				t.Errorf("String() = %q, want %q", f.String(), tc.wantWire)
			}
		})
	}
}

// TestTopicNamespace_MintFilter_EmptyGroup verifies that an empty consumerGroup
// is rejected with ErrAdapterMQTTInvalidSubscribeFilter.
func TestTopicNamespace_MintFilter_EmptyGroup(t *testing.T) {
	t.Parallel()
	ns, err := ParseTopicNamespace("ns")
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	f, err := ns.MintFilter("", "ns/a")
	if err == nil {
		t.Fatal("MintFilter(empty group) expected error, got nil")
	}
	if f != (subscribableFilter{}) {
		t.Errorf("MintFilter(empty group) returned non-zero filter: %v", f)
	}
	var ec *errcode.Error
	if !errors.As(err, &ec) {
		t.Fatalf("expected *errcode.Error, got %T: %v", err, err)
	}
	if ec.Code != ErrAdapterMQTTInvalidSubscribeFilter {
		t.Errorf("code = %s, want %s", ec.Code, ErrAdapterMQTTInvalidSubscribeFilter)
	}
}

// TestTopicNamespace_MintFilter_SubscribeOKFailure verifies that a filter
// rejected by SubscribeOK (wildcard misplacement / outside namespace) returns
// the zero subscribableFilter and a non-nil error.
func TestTopicNamespace_MintFilter_SubscribeOKFailure(t *testing.T) {
	t.Parallel()
	ns, err := ParseTopicNamespace("ns")
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	cases := []struct {
		name     string
		filter   string
		wantCode errcode.Code
	}{
		{"hash-not-at-tail", "ns/#/x", ErrAdapterMQTTInvalidSubscribeFilter},
		{"outside-namespace", "other/#", ErrAdapterMQTTTopicOutsideNamespace},
		{"empty-filter", "", ErrAdapterMQTTInvalidSubscribeFilter},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f, err := ns.MintFilter("cg", tc.filter)
			if err == nil {
				t.Fatalf("MintFilter(%q) expected error, got nil", tc.filter)
			}
			if f != (subscribableFilter{}) {
				t.Errorf("MintFilter(%q) returned non-zero filter: %v", tc.filter, f)
			}
			var ec *errcode.Error
			if !errors.As(err, &ec) {
				t.Fatalf("expected *errcode.Error, got %T: %v", err, err)
			}
			if ec.Code != tc.wantCode {
				t.Errorf("code = %s, want %s", ec.Code, tc.wantCode)
			}
		})
	}
}

// TestTopicNamespace_MintFilter_ZeroNS verifies that MintFilter on a zero-value
// namespace returns ErrAdapterMQTTInvalidTopicNamespace (via SubscribeOK's
// zero-receiver guard).
func TestTopicNamespace_MintFilter_ZeroNS(t *testing.T) {
	t.Parallel()
	var ns TopicNamespace
	_, err := ns.MintFilter("cg", "x")
	if err == nil {
		t.Fatal("MintFilter on zero-value namespace expected error, got nil")
	}
	var ec *errcode.Error
	if !errors.As(err, &ec) {
		t.Fatalf("expected *errcode.Error, got %T: %v", err, err)
	}
	if ec.Code != ErrAdapterMQTTInvalidTopicNamespace {
		t.Errorf("code = %s, want %s", ec.Code, ErrAdapterMQTTInvalidTopicNamespace)
	}
}

// TestSubscribableFilter_FieldFreeze locks the subscribableFilter struct shape:
// exactly one unexported string field (wireFilter). Any drift (rename, type
// change, export, new field) fails this test before it could silently break the
// sealed-construction invariant. (matchFilter was removed when dispatch moved to
// MQTT v5 Subscription Identifiers.)
func TestSubscribableFilter_FieldFreeze(t *testing.T) {
	t.Parallel()
	rt := reflect.TypeOf(subscribableFilter{})
	if rt.NumField() != 1 {
		t.Fatalf("subscribableFilter NumField = %d, want 1", rt.NumField())
	}
	wantNames := []string{"wireFilter"}
	for i, want := range wantNames {
		f := rt.Field(i)
		if f.Name != want {
			t.Errorf("field[%d].Name = %q, want %q", i, f.Name, want)
		}
		if f.Type.Kind() != reflect.String {
			t.Errorf("field[%d].Type = %v, want string", i, f.Type.Kind())
		}
		if f.IsExported() {
			t.Errorf("field[%d] is exported; want unexported", i)
		}
	}
}

// TestPublishableTopic_FieldFreeze locks the publishableTopic struct shape:
// exactly one field named "topic" of type string, unexported. Any accidental
// drift (rename, type change, export, new field) fails this test before it
// could silently break the sealed-construction invariant.
func TestPublishableTopic_FieldFreeze(t *testing.T) {
	t.Parallel()
	rt := reflect.TypeOf(publishableTopic{})
	if rt.NumField() != 1 {
		t.Errorf("publishableTopic NumField = %d, want 1", rt.NumField())
	}
	f := rt.Field(0)
	if f.Name != "topic" {
		t.Errorf("field[0].Name = %q, want %q", f.Name, "topic")
	}
	if f.Type.Kind() != reflect.String {
		t.Errorf("field[0].Type = %v, want string", f.Type.Kind())
	}
	if f.IsExported() {
		t.Error("field[0] is exported; want unexported")
	}
}

package topicns

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/pkg/errcode"
)

func TestParse_Valid(t *testing.T) {
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
		{"len-128", strings.Repeat("a", 128)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ns, err := Parse(tc.ns)
			if err != nil {
				t.Fatalf("Parse(%q) unexpected error: %v", tc.ns, err)
			}
			if ns.String() != tc.ns {
				t.Errorf("String() = %q, want %q", ns.String(), tc.ns)
			}
		})
	}
}

func TestParse_Invalid(t *testing.T) {
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
			_, err := Parse(tc.ns)
			if err == nil {
				t.Errorf("Parse(%q) expected error, got nil", tc.ns)
			}
		})
	}
}

func TestNamespace_PublishOK(t *testing.T) {
	ns, err := Parse("devices/sensors")
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

func TestNamespace_PublishOK_InvalidPublishTopicCode(t *testing.T) {
	t.Parallel()
	ns, err := Parse("ns")
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
			if ec.Code != ErrInvalidPublishTopic {
				t.Errorf("code = %s, want %s", ec.Code, ErrInvalidPublishTopic)
			}
		})
	}
}

func TestNamespace_SubscribeOK(t *testing.T) {
	ns, err := Parse("ns")
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

// TestNamespace_ZeroReceiver_Rejected covers R2 round-2 finding:
// `var ns Namespace; ns.PublishOK("...")` previously returned nil for
// topic == "" or any prefix match against empty namespace, because the
// PublishOK / SubscribeOK methods accept zero-value receivers structurally.
// The fix asserts both methods reject the zero-value receiver with
// ErrInvalidNamespace before any other check.
func TestNamespace_ZeroReceiver_Rejected(t *testing.T) {
	t.Parallel()
	var zero Namespace

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
			if ec.Code != ErrInvalidNamespace {
				t.Errorf("zero-value receiver %s: code = %s, want %s",
					tc.name, ec.Code, ErrInvalidNamespace)
			}
		})
	}
}

// TestNamespace_EmptyTopicAndFilter_Rejected covers MQTT v5 §4.7.3
// (forbid empty topic name) for PublishOK and the parallel empty-filter
// rejection for SubscribeOK.
func TestNamespace_EmptyTopicAndFilter_Rejected(t *testing.T) {
	t.Parallel()
	ns, err := Parse("ns")
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

func TestNamespace_PublishOK_PrefixBoundary(t *testing.T) {
	// "ns" should not match "ns2" or "nsx"
	ns, _ := Parse("ns")
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

// TestNamespace_SubscribeOK_WildcardErrorCodes verifies that wildcard
// placement errors return ErrInvalidSubscribeFilter (not the
// namespace-invalid code) and that outside-namespace errors return
// ErrTopicOutsideNamespace.
func TestNamespace_SubscribeOK_WildcardErrorCodes(t *testing.T) {
	ns, err := Parse("ns")
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
			wantCode: ErrInvalidSubscribeFilter,
		},
		{
			name:     "plus-not-lone-token returns InvalidSubscribeFilter",
			filter:   "ns/ab+c",
			wantCode: ErrInvalidSubscribeFilter,
		},
		{
			name:     "hash-not-lone-token returns InvalidSubscribeFilter",
			filter:   "ns/ab#c",
			wantCode: ErrInvalidSubscribeFilter,
		},
		{
			name:     "outside-namespace returns TopicOutsideNamespace",
			filter:   "other/#",
			wantCode: ErrTopicOutsideNamespace,
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
// PublishableTopic / Namespace.Mint tests (B3a)
// ---------------------------------------------------------------------------

// TestNamespace_Mint_Success verifies that Mint returns a non-zero
// PublishableTopic with String() == the input topic when the topic passes
// PublishOK validation.
func TestNamespace_Mint_Success(t *testing.T) {
	t.Parallel()
	ns, err := Parse("ns")
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	pt, err := ns.Mint("ns/foo")
	if err != nil {
		t.Fatalf("Mint(%q) unexpected error: %v", "ns/foo", err)
	}
	if pt.String() != "ns/foo" {
		t.Errorf("PublishableTopic.String() = %q, want %q", pt.String(), "ns/foo")
	}
	// Also verify exact-topic (prefix == topic) succeeds.
	pt2, err := ns.Mint("ns")
	if err != nil {
		t.Fatalf("Mint(%q) unexpected error: %v", "ns", err)
	}
	if pt2.String() != "ns" {
		t.Errorf("PublishableTopic.String() = %q, want %q", pt2.String(), "ns")
	}
}

// TestNamespace_Mint_PublishOKFailure verifies that Mint of an
// out-of-namespace topic returns the zero PublishableTopic and a non-nil error
// with code ErrTopicOutsideNamespace.
func TestNamespace_Mint_PublishOKFailure(t *testing.T) {
	t.Parallel()
	ns, err := Parse("ns1")
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	pt, err := ns.Mint("ns2/x")
	if err == nil {
		t.Fatalf("Mint(out-of-namespace) expected error, got nil")
	}
	if pt != (PublishableTopic{}) {
		t.Errorf("Mint(out-of-namespace) returned non-zero PublishableTopic: %v", pt)
	}
	var ec *errcode.Error
	if !errors.As(err, &ec) {
		t.Fatalf("expected *errcode.Error, got %T: %v", err, err)
	}
	if ec.Code != ErrTopicOutsideNamespace {
		t.Errorf("code = %s, want %s", ec.Code, ErrTopicOutsideNamespace)
	}
}

// TestNamespace_Mint_Empty verifies that Mint of an empty string returns
// an error with code ErrInvalidPublishTopic (per PublishOK rule for
// empty topics).
func TestNamespace_Mint_Empty(t *testing.T) {
	t.Parallel()
	ns, err := Parse("ns")
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
	if ec.Code != ErrInvalidPublishTopic {
		t.Errorf("code = %s, want %s", ec.Code, ErrInvalidPublishTopic)
	}
}

// TestNamespace_Mint_Wildcard verifies that Mint of a wildcard topic
// returns an error with code ErrInvalidPublishTopic.
func TestNamespace_Mint_Wildcard(t *testing.T) {
	t.Parallel()
	ns, err := Parse("ns")
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
			if ec.Code != ErrInvalidPublishTopic {
				t.Errorf("code = %s, want %s", ec.Code, ErrInvalidPublishTopic)
			}
		})
	}
}

// TestNamespace_Mint_ZeroNS verifies that calling Mint on a zero-value
// Namespace returns an error with code ErrInvalidNamespace.
func TestNamespace_Mint_ZeroNS(t *testing.T) {
	t.Parallel()
	var ns Namespace
	_, err := ns.Mint("x")
	if err == nil {
		t.Fatal("Mint on zero-value namespace expected error, got nil")
	}
	var ec *errcode.Error
	if !errors.As(err, &ec) {
		t.Fatalf("expected *errcode.Error, got %T: %v", err, err)
	}
	if ec.Code != ErrInvalidNamespace {
		t.Errorf("code = %s, want %s", ec.Code, ErrInvalidNamespace)
	}
}

// ---------------------------------------------------------------------------
// Namespace.MintDeadLetter tests (PR-4: $dead/<topic> app-level DLT)
// ---------------------------------------------------------------------------

// TestNamespace_MintDeadLetter_Success verifies that MintDeadLetter
// validates the ORIGINAL topic against the namespace and returns a
// PublishableTopic for the "$dead/<originalTopic>" sink.
func TestNamespace_MintDeadLetter_Success(t *testing.T) {
	t.Parallel()
	ns, err := Parse("ns")
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	pt, err := ns.MintDeadLetter("ns/foo/bar")
	if err != nil {
		t.Fatalf("MintDeadLetter(%q) unexpected error: %v", "ns/foo/bar", err)
	}
	if pt.String() != "$dead/ns/foo/bar" {
		t.Errorf("PublishableTopic.String() = %q, want %q", pt.String(), "$dead/ns/foo/bar")
	}
}

// TestNamespace_MintDeadLetter_ExactNamespace verifies that the
// exact-namespace topic (prefix == topic) is accepted and prefixed.
func TestNamespace_MintDeadLetter_ExactNamespace(t *testing.T) {
	t.Parallel()
	ns, err := Parse("ns")
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	pt, err := ns.MintDeadLetter("ns")
	if err != nil {
		t.Fatalf("MintDeadLetter(%q) unexpected error: %v", "ns", err)
	}
	if pt.String() != "$dead/ns" {
		t.Errorf("PublishableTopic.String() = %q, want %q", pt.String(), "$dead/ns")
	}
}

// TestNamespace_MintDeadLetter_OutsideNamespace verifies that an
// out-of-namespace original topic is rejected fail-closed (the poison topic is
// untrusted broker-delivered input), returning ErrTopicOutsideNamespace.
func TestNamespace_MintDeadLetter_OutsideNamespace(t *testing.T) {
	t.Parallel()
	ns, err := Parse("ns1")
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	pt, err := ns.MintDeadLetter("ns2/x")
	if err == nil {
		t.Fatalf("MintDeadLetter(out-of-namespace) expected error, got nil")
	}
	if pt != (PublishableTopic{}) {
		t.Errorf("MintDeadLetter(out-of-namespace) returned non-zero PublishableTopic: %v", pt)
	}
	var ec *errcode.Error
	if !errors.As(err, &ec) {
		t.Fatalf("expected *errcode.Error, got %T: %v", err, err)
	}
	if ec.Code != ErrTopicOutsideNamespace {
		t.Errorf("code = %s, want %s", ec.Code, ErrTopicOutsideNamespace)
	}
}

// TestNamespace_MintDeadLetter_Wildcard verifies that a wildcard in the
// original topic is rejected (fail-closed) with ErrInvalidPublishTopic
// — a poison topic carrying "+"/"#" must never become a publish target.
func TestNamespace_MintDeadLetter_Wildcard(t *testing.T) {
	t.Parallel()
	ns, err := Parse("ns")
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	for _, wc := range []string{"ns/+", "ns/#", "ns/a/+"} {
		wc := wc
		t.Run(wc, func(t *testing.T) {
			t.Parallel()
			_, err := ns.MintDeadLetter(wc)
			if err == nil {
				t.Fatalf("MintDeadLetter(%q) expected error, got nil", wc)
			}
			var ec *errcode.Error
			if !errors.As(err, &ec) {
				t.Fatalf("expected *errcode.Error, got %T: %v", err, err)
			}
			if ec.Code != ErrInvalidPublishTopic {
				t.Errorf("code = %s, want %s", ec.Code, ErrInvalidPublishTopic)
			}
		})
	}
}

// TestNamespace_MintDeadLetter_Empty verifies that an empty original topic
// returns ErrInvalidPublishTopic.
func TestNamespace_MintDeadLetter_Empty(t *testing.T) {
	t.Parallel()
	ns, err := Parse("ns")
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	_, err = ns.MintDeadLetter("")
	if err == nil {
		t.Fatal("MintDeadLetter(empty) expected error, got nil")
	}
	var ec *errcode.Error
	if !errors.As(err, &ec) {
		t.Fatalf("expected *errcode.Error, got %T: %v", err, err)
	}
	if ec.Code != ErrInvalidPublishTopic {
		t.Errorf("code = %s, want %s", ec.Code, ErrInvalidPublishTopic)
	}
}

// TestNamespace_MintDeadLetter_ZeroNS verifies that MintDeadLetter on a
// zero-value Namespace returns ErrInvalidNamespace.
func TestNamespace_MintDeadLetter_ZeroNS(t *testing.T) {
	t.Parallel()
	var ns Namespace
	_, err := ns.MintDeadLetter("x")
	if err == nil {
		t.Fatal("MintDeadLetter on zero-value namespace expected error, got nil")
	}
	var ec *errcode.Error
	if !errors.As(err, &ec) {
		t.Fatalf("expected *errcode.Error, got %T: %v", err, err)
	}
	if ec.Code != ErrInvalidNamespace {
		t.Errorf("code = %s, want %s", ec.Code, ErrInvalidNamespace)
	}
}

// ---------------------------------------------------------------------------
// SubscribableFilter / Namespace.MintFilter tests (PR-3 foundation)
// ---------------------------------------------------------------------------

// TestNamespace_MintFilter_Success verifies that MintFilter returns a
// SubscribableFilter whose wireFilter is the MQTT v5 "$share/{group}/{filter}"
// shared-subscription form (matchFilter was removed when dispatch moved to
// Subscription Identifiers).
func TestNamespace_MintFilter_Success(t *testing.T) {
	t.Parallel()
	ns, err := Parse("ns")
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

// TestNamespace_MintFilter_EmptyGroup verifies that an empty consumerGroup
// is rejected with ErrInvalidSubscribeFilter.
func TestNamespace_MintFilter_EmptyGroup(t *testing.T) {
	t.Parallel()
	ns, err := Parse("ns")
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	f, err := ns.MintFilter("", "ns/a")
	if err == nil {
		t.Fatal("MintFilter(empty group) expected error, got nil")
	}
	if f != (SubscribableFilter{}) {
		t.Errorf("MintFilter(empty group) returned non-zero filter: %v", f)
	}
	var ec *errcode.Error
	if !errors.As(err, &ec) {
		t.Fatalf("expected *errcode.Error, got %T: %v", err, err)
	}
	if ec.Code != ErrInvalidSubscribeFilter {
		t.Errorf("code = %s, want %s", ec.Code, ErrInvalidSubscribeFilter)
	}
}

// TestNamespace_MintFilter_SubscribeOKFailure verifies that a filter
// rejected by SubscribeOK (wildcard misplacement / outside namespace) returns
// the zero SubscribableFilter and a non-nil error.
func TestNamespace_MintFilter_SubscribeOKFailure(t *testing.T) {
	t.Parallel()
	ns, err := Parse("ns")
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	cases := []struct {
		name     string
		filter   string
		wantCode errcode.Code
	}{
		{"hash-not-at-tail", "ns/#/x", ErrInvalidSubscribeFilter},
		{"outside-namespace", "other/#", ErrTopicOutsideNamespace},
		{"empty-filter", "", ErrInvalidSubscribeFilter},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f, err := ns.MintFilter("cg", tc.filter)
			if err == nil {
				t.Fatalf("MintFilter(%q) expected error, got nil", tc.filter)
			}
			if f != (SubscribableFilter{}) {
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

// TestNamespace_MintFilter_ZeroNS verifies that MintFilter on a zero-value
// namespace returns ErrInvalidNamespace (via SubscribeOK's
// zero-receiver guard).
func TestNamespace_MintFilter_ZeroNS(t *testing.T) {
	t.Parallel()
	var ns Namespace
	_, err := ns.MintFilter("cg", "x")
	if err == nil {
		t.Fatal("MintFilter on zero-value namespace expected error, got nil")
	}
	var ec *errcode.Error
	if !errors.As(err, &ec) {
		t.Fatalf("expected *errcode.Error, got %T: %v", err, err)
	}
	if ec.Code != ErrInvalidNamespace {
		t.Errorf("code = %s, want %s", ec.Code, ErrInvalidNamespace)
	}
}

// TestSubscribableFilter_FieldFreeze locks the SubscribableFilter struct shape:
// exactly one unexported string field (wireFilter). Any drift (rename, type
// change, export, new field) fails this test before it could silently break the
// sealed-construction invariant. (matchFilter was removed when dispatch moved to
// MQTT v5 Subscription Identifiers.)
func TestSubscribableFilter_FieldFreeze(t *testing.T) {
	t.Parallel()
	rt := reflect.TypeOf(SubscribableFilter{})
	if rt.NumField() != 1 {
		t.Fatalf("SubscribableFilter NumField = %d, want 1", rt.NumField())
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

// TestPublishableTopic_FieldFreeze locks the PublishableTopic struct shape:
// exactly one field named "topic" of type string, unexported. Any accidental
// drift (rename, type change, export, new field) fails this test before it
// could silently break the sealed-construction invariant.
func TestPublishableTopic_FieldFreeze(t *testing.T) {
	t.Parallel()
	rt := reflect.TypeOf(PublishableTopic{})
	if rt.NumField() != 1 {
		t.Errorf("PublishableTopic NumField = %d, want 1", rt.NumField())
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

// ---------------------------------------------------------------------------
// FIX 7 coverage additions
// ---------------------------------------------------------------------------

// TestNamespace_MintFilter_InvalidGroup verifies that a non-empty but
// invalid consumer group (containing uppercase, slash, plus, or hash) is
// rejected with ErrInvalidSubscribeFilter and returns a zero SubscribableFilter.
// This exercises the isValidConsumerGroup path, distinct from the empty-group
// path already tested by TestNamespace_MintFilter_EmptyGroup.
func TestNamespace_MintFilter_InvalidGroup(t *testing.T) {
	t.Parallel()
	ns, err := Parse("ns")
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	cases := []struct {
		name  string
		group string
	}{
		{"uppercase", "BAD"},
		{"slash", "bad/group"},
		{"plus", "bad+group"},
		{"hash", "bad#group"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f, err := ns.MintFilter(tc.group, "ns/a")
			if err == nil {
				t.Fatalf("MintFilter(group=%q) expected error, got nil", tc.group)
			}
			if f != (SubscribableFilter{}) {
				t.Errorf("MintFilter(group=%q) returned non-zero filter: %v", tc.group, f)
			}
			var ec *errcode.Error
			if !errors.As(err, &ec) {
				t.Fatalf("expected *errcode.Error, got %T: %v", err, err)
			}
			if ec.Code != ErrInvalidSubscribeFilter {
				t.Errorf("code = %s, want %s", ec.Code, ErrInvalidSubscribeFilter)
			}
		})
	}
}

// TestNamespace_SubscribeOK_BareWildcardOutsideNamespace verifies that bare
// wildcard filters "#" and "+/x" are rejected with ErrTopicOutsideNamespace
// when the namespace is "ns" — the filter head is empty (before the first "/")
// and therefore outside the namespace prefix boundary.
func TestNamespace_SubscribeOK_BareWildcardOutsideNamespace(t *testing.T) {
	t.Parallel()
	ns, err := Parse("ns")
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	cases := []struct {
		name   string
		filter string
	}{
		{"bare-hash", "#"},
		{"bare-plus-subpath", "+/x"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := ns.SubscribeOK(tc.filter)
			if err == nil {
				t.Fatalf("SubscribeOK(%q) expected error, got nil", tc.filter)
			}
			var ec *errcode.Error
			if !errors.As(err, &ec) {
				t.Fatalf("expected *errcode.Error, got %T: %v", err, err)
			}
			if ec.Code != ErrTopicOutsideNamespace {
				t.Errorf("SubscribeOK(%q) code = %s, want %s",
					tc.filter, ec.Code, ErrTopicOutsideNamespace)
			}
		})
	}
}

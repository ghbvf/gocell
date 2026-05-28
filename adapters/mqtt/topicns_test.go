package mqtt

import (
	"errors"
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

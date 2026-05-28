package mqtt

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"testing"

	"github.com/eclipse/paho.golang/autopaho"

	"github.com/ghbvf/gocell/pkg/errcode"
)

func TestClassifyConnackReason(t *testing.T) {
	tests := []struct {
		name       string
		reasonCode byte
		wantClass  connackClass
		wantCode   errcode.Code
	}{
		// Transient codes
		{"server-unavailable-0x88", 0x88, classTransient, ErrAdapterMQTTConnect},
		{"quota-exceeded-0x97", 0x97, classTransient, ErrAdapterMQTTConnect},
		{"unknown-code-default", 0x01, classTransient, ErrAdapterMQTTConnect},

		// Bootstrap fatal codes
		{"malformed-packet-0x81", 0x81, classBootstrapFatal, ErrAdapterMQTTConnectPermanent},
		{"protocol-error-0x82", 0x82, classBootstrapFatal, ErrAdapterMQTTConnectPermanent},
		{"unsupported-0x84", 0x84, classBootstrapFatal, ErrAdapterMQTTConnectPermanent},
		{"client-id-invalid-0x85", 0x85, classBootstrapFatal, ErrAdapterMQTTConnectPermanent},
		{"banned-0x8A", 0x8A, classBootstrapFatal, ErrAdapterMQTTConnectPermanent},
		{"packet-too-large-0x95", 0x95, classBootstrapFatal, ErrAdapterMQTTConnectPermanent},

		// Permanent retain
		{"not-authorized-0x87", 0x87, classPermanentRetain, ErrAdapterMQTTConnectPermanent},
		{"bad-user-or-pass-0x86", 0x86, classPermanentRetain, ErrAdapterMQTTConnectPermanent},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Build a *autopaho.ConnackError wrapped in fmt.Errorf
			connackErr := &autopaho.ConnackError{ReasonCode: tc.reasonCode}
			wrappedErr := fmt.Errorf("connection failed: %w", connackErr)

			gotClass, gotCode := classifyConnackReason(wrappedErr)
			if gotClass != tc.wantClass {
				t.Errorf("classifyConnackReason(0x%02x) class = %v, want %v", tc.reasonCode, gotClass, tc.wantClass)
			}
			if gotCode != tc.wantCode {
				t.Errorf("classifyConnackReason(0x%02x) code = %v, want %v", tc.reasonCode, gotCode, tc.wantCode)
			}
		})
	}
}

func TestClassifyConnackReason_TransportError(t *testing.T) {
	// Plain transport error (no ConnackError) → transient
	plainErr := errors.New("connection refused")
	gotClass, gotCode := classifyConnackReason(plainErr)
	if gotClass != classTransient {
		t.Errorf("plain error: class = %v, want classTransient", gotClass)
	}
	if gotCode != ErrAdapterMQTTConnect {
		t.Errorf("plain error: code = %v, want ErrAdapterMQTTConnect", gotCode)
	}
}

func TestClassifyConnackReason_TLSError(t *testing.T) {
	// x509 unknown authority error → bootstrapFatal
	x509Err := &tls.CertificateVerificationError{
		UnverifiedCertificates: nil,
		Err:                    x509.UnknownAuthorityError{},
	}
	gotClass, gotCode := classifyConnackReason(x509Err)
	if gotClass != classBootstrapFatal {
		t.Errorf("TLS x509 error: class = %v, want classBootstrapFatal", gotClass)
	}
	if gotCode != ErrAdapterMQTTConnectPermanent {
		t.Errorf("TLS x509 error: code = %v, want ErrAdapterMQTTConnectPermanent", gotCode)
	}
}

func TestIsTLSHandshakeError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "CertificateVerificationError",
			err: &tls.CertificateVerificationError{
				UnverifiedCertificates: nil,
				Err:                    x509.UnknownAuthorityError{},
			},
			want: true,
		},
		{
			name: "x509-unknown-authority-direct",
			err:  x509.UnknownAuthorityError{},
			want: true,
		},
		{
			name: "x509-hostname-error",
			err:  x509.HostnameError{},
			want: true,
		},
		{
			name: "plain-error",
			err:  errors.New("not tls"),
			want: false,
		},
		{
			name: "nil",
			err:  nil,
			want: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := isTLSHandshakeError(tc.err)
			if got != tc.want {
				t.Errorf("isTLSHandshakeError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestErrorCodes_DeclaredAsErrcodeCodes(t *testing.T) {
	// Verify all exported codes are valid errcode.Code values (non-empty strings).
	codes := []errcode.Code{
		ErrAdapterMQTTInvalidConfig,
		ErrAdapterMQTTInvalidClientID,
		ErrAdapterMQTTInvalidTopicNamespace,
		ErrAdapterMQTTTopicOutsideNamespace,
		ErrAdapterMQTTConnect,
		ErrAdapterMQTTConnectTimeout,
		ErrAdapterMQTTConnectPermanent,
		ErrAdapterMQTTNeverConnected,
		ErrAdapterMQTTClosed,
		ErrAdapterMQTTPayloadTooLarge,
		ErrAdapterMQTTInvalidSubscribeFilter,
		// PR-2 additions
		ErrAdapterMQTTPubAckTimeout,
		ErrAdapterMQTTPublishNoSubscribers,
		ErrAdapterMQTTPublishRejected,
		ErrAdapterMQTTPublishRateLimited,
		ErrAdapterMQTTPublishCanceled,
		ErrAdapterMQTTPublishFailed,
		ErrAdapterMQTTPublisherCloseTimeout,
	}
	for _, c := range codes {
		if c == "" {
			t.Errorf("error code is empty string: %v", c)
		}
		if string(c)[:len("ERR_ADAPTER_MQTT_")] != "ERR_ADAPTER_MQTT_" {
			t.Errorf("code %q does not have ERR_ADAPTER_MQTT_ prefix", c)
		}
	}
}

func TestClassifyPubackReason(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		code     byte
		wantCode errcode.Code
		wantKind errcode.Kind
	}{
		// 0x00 Success — Ack path, empty sentinel + KindInternal (unused by caller)
		{"success-0x00", 0x00, "", errcode.KindInternal},
		// 0x10 No matching subscribers
		{"no-matching-subscribers-0x10", 0x10, ErrAdapterMQTTPublishNoSubscribers, errcode.KindUnavailable},
		// 0x80 Unspecified error
		{"unspecified-error-0x80", 0x80, ErrAdapterMQTTPublishRejected, errcode.KindInternal},
		// 0x83 Implementation specific error
		{"implementation-specific-0x83", 0x83, ErrAdapterMQTTPublishRejected, errcode.KindInternal},
		// 0x87 Not authorized — retryable (KindUnavailable, aligned with CONNACK 0x87 classPermanentRetain)
		{"not-authorized-0x87", 0x87, ErrAdapterMQTTPublishRejected, errcode.KindUnavailable},
		// 0x90 Topic Name invalid
		{"topic-name-invalid-0x90", 0x90, ErrAdapterMQTTPublishRejected, errcode.KindInvalid},
		// 0x97 Quota exceeded / rate limited
		{"quota-exceeded-0x97", 0x97, ErrAdapterMQTTPublishRateLimited, errcode.KindUnavailable},
		// 0x99 Payload format invalid — reuse ErrAdapterMQTTPayloadTooLarge
		{"payload-format-invalid-0x99", 0x99, ErrAdapterMQTTPayloadTooLarge, errcode.KindInvalid},
		// default — unknown code
		{"unknown-code-0x7f", 0x7F, ErrAdapterMQTTPublishRejected, errcode.KindInternal},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			gotCode, gotKind := classifyPubackReason(tc.code)
			if gotCode != tc.wantCode {
				t.Errorf("classifyPubackReason(0x%02x) code = %q, want %q", tc.code, gotCode, tc.wantCode)
			}
			if gotKind != tc.wantKind {
				t.Errorf("classifyPubackReason(0x%02x) kind = %v, want %v", tc.code, gotKind, tc.wantKind)
			}
		})
	}
}

func TestPubackReasonName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		code byte
		want string
	}{
		{0x00, "Success"},
		{0x10, "NoMatchingSubscribers"},
		{0x80, "UnspecifiedError"},
		{0x83, "ImplementationSpecificError"},
		{0x87, "NotAuthorized"},
		{0x90, "TopicNameInvalid"},
		{0x91, "PacketIdentifierInUse"},
		{0x97, "QuotaExceeded"},
		{0x99, "PayloadFormatInvalid"},
		{0x7F, "Unknown"}, // unrecognized code
		{0xFE, "Unknown"}, // another unrecognized code
	}

	for _, tc := range tests {
		tc := tc
		t.Run(fmt.Sprintf("0x%02x", tc.code), func(t *testing.T) {
			t.Parallel()
			got := pubackReasonName(tc.code)
			if got != tc.want {
				t.Errorf("pubackReasonName(0x%02x) = %q, want %q", tc.code, got, tc.want)
			}
		})
	}
}

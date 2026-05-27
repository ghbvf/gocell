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
		name      string
		reasonCode byte
		wantClass connackClass
		wantCode  errcode.Code
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
		{"keep-alive-0x8A", 0x8A, classBootstrapFatal, ErrAdapterMQTTConnectPermanent},
		{"payload-too-large-0x95", 0x95, classBootstrapFatal, ErrAdapterMQTTConnectPermanent},

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

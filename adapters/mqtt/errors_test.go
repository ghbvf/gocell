package mqtt

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/eclipse/paho.golang/autopaho"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// TestClassifyConnackReason_AllSpecCodes exercises every code in the
// connackReasonTable (MQTT v5 §3.2.2.2) so that a code added to the table
// without a correct class will fail the test.
func TestClassifyConnackReason_AllSpecCodes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		reasonCode byte
		wantClass  connackClass
		wantCode   errcode.Code
	}{
		// §3.2.2.2 full table
		{"0x00-Success", 0x00, classTransient, ErrAdapterMQTTConnect},
		{"0x80-UnspecifiedError", 0x80, classTransient, ErrAdapterMQTTConnect},
		{"0x81-MalformedPacket", 0x81, classBootstrapFatal, ErrAdapterMQTTConnectPermanent},
		{"0x82-ProtocolError", 0x82, classBootstrapFatal, ErrAdapterMQTTConnectPermanent},
		{"0x83-ImplementationSpecificError", 0x83, classTransient, ErrAdapterMQTTConnect},
		{"0x84-UnsupportedProtocolVersion", 0x84, classBootstrapFatal, ErrAdapterMQTTConnectPermanent},
		{"0x85-ClientIdentifierNotValid", 0x85, classBootstrapFatal, ErrAdapterMQTTConnectPermanent},
		{"0x86-BadUserNameOrPassword", 0x86, classPermanentRetain, ErrAdapterMQTTConnectPermanent},
		{"0x87-NotAuthorized", 0x87, classPermanentRetain, ErrAdapterMQTTConnectPermanent},
		{"0x88-ServerUnavailable", 0x88, classTransient, ErrAdapterMQTTConnect},
		{"0x89-ServerBusy", 0x89, classTransient, ErrAdapterMQTTConnect},
		{"0x8A-Banned", 0x8A, classBootstrapFatal, ErrAdapterMQTTConnectPermanent},
		{"0x8C-BadAuthenticationMethod", 0x8C, classPermanentRetain, ErrAdapterMQTTConnectPermanent},
		{"0x90-TopicNameInvalid", 0x90, classBootstrapFatal, ErrAdapterMQTTConnectPermanent},
		{"0x95-PacketTooLarge", 0x95, classBootstrapFatal, ErrAdapterMQTTConnectPermanent},
		{"0x97-QuotaExceeded", 0x97, classTransient, ErrAdapterMQTTConnect},
		{"0x99-PayloadFormatInvalid", 0x99, classBootstrapFatal, ErrAdapterMQTTConnectPermanent},
		{"0x9A-RetainNotSupported", 0x9A, classBootstrapFatal, ErrAdapterMQTTConnectPermanent},
		{"0x9B-QoSNotSupported", 0x9B, classBootstrapFatal, ErrAdapterMQTTConnectPermanent},
		{"0x9C-UseAnotherServer", 0x9C, classTransient, ErrAdapterMQTTConnect},
		{"0x9D-ServerMoved", 0x9D, classBootstrapFatal, ErrAdapterMQTTConnectPermanent},
		{"0x9F-ConnectionRateExceeded", 0x9F, classTransient, ErrAdapterMQTTConnect},
		// TLS handshake → bootstrap fatal
		// (tested separately below as it uses a different error shape)
		// non-ConnackError → transient
		// (tested separately below)
		// unknown non-spec byte → transient
		{"0xFE-unknown-nonspec", 0xFE, classTransient, ErrAdapterMQTTConnect},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			connackErr := &autopaho.ConnackError{ReasonCode: tc.reasonCode}
			wrapped := fmt.Errorf("connection failed: %w", connackErr)
			gotClass, gotCode := classifyConnackReason(wrapped)
			if gotClass != tc.wantClass {
				t.Errorf("classifyConnackReason(0x%02x) class = %v, want %v", tc.reasonCode, gotClass, tc.wantClass)
			}
			if gotCode != tc.wantCode {
				t.Errorf("classifyConnackReason(0x%02x) code = %v, want %v", tc.reasonCode, gotCode, tc.wantCode)
			}
		})
	}
}

func TestClassifyConnackReason_TLSError_BootstrapFatal(t *testing.T) {
	t.Parallel()
	tlsErr := &tls.CertificateVerificationError{
		UnverifiedCertificates: nil,
		Err:                    x509.UnknownAuthorityError{},
	}
	gotClass, gotCode := classifyConnackReason(tlsErr)
	if gotClass != classBootstrapFatal {
		t.Errorf("TLS: class = %v, want classBootstrapFatal", gotClass)
	}
	if gotCode != ErrAdapterMQTTConnectPermanent {
		t.Errorf("TLS: code = %v, want ErrAdapterMQTTConnectPermanent", gotCode)
	}
}

func TestClassifyConnackReason_NonConnackError_Transient(t *testing.T) {
	t.Parallel()
	plainErr := errors.New("dial tcp: connection refused")
	gotClass, gotCode := classifyConnackReason(plainErr)
	if gotClass != classTransient {
		t.Errorf("non-ConnackError: class = %v, want classTransient", gotClass)
	}
	if gotCode != ErrAdapterMQTTConnect {
		t.Errorf("non-ConnackError: code = %v, want ErrAdapterMQTTConnect", gotCode)
	}
}

// TestClassifyPubackReason_AllSpecCodes exercises every code in the pubackReasonTable
// (MQTT v5 §3.4.2.1) including 0x91 (PacketIdentifierInUse) which was previously a
// silent default.
func TestClassifyPubackReason_AllSpecCodes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		code     byte
		wantCode errcode.Code
		wantKind errcode.Kind
	}{
		{"0x00-Success", 0x00, "", errcode.KindInternal},
		{"0x10-NoMatchingSubscribers", 0x10, ErrAdapterMQTTPublishNoSubscribers, errcode.KindUnavailable},
		{"0x80-UnspecifiedError", 0x80, ErrAdapterMQTTPublishRejected, errcode.KindInternal},
		{"0x83-ImplementationSpecificError", 0x83, ErrAdapterMQTTPublishRejected, errcode.KindInternal},
		{"0x87-NotAuthorized", 0x87, ErrAdapterMQTTPublishNotAuthorized, errcode.KindUnavailable},
		{"0x90-TopicNameInvalid", 0x90, ErrAdapterMQTTPublishRejected, errcode.KindInvalid},
		{"0x91-PacketIdentifierInUse", 0x91, ErrAdapterMQTTPublishRejected, errcode.KindInternal}, // was default; now explicit
		{"0x97-QuotaExceeded", 0x97, ErrAdapterMQTTPublishRateLimited, errcode.KindUnavailable},
		{"0x99-PayloadFormatInvalid", 0x99, ErrAdapterMQTTPublishPayloadFormatInvalid, errcode.KindInvalid},
		// unknown byte → default behavior
		{"0xAB-unknown", 0xAB, ErrAdapterMQTTPublishRejected, errcode.KindInternal},
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

// TestClassifySubackReason_AllSpecCodes exercises every code in the subackReasonTable
// (MQTT v5 §3.9.3) including 0x83 and 0x91 which were previously silent defaults.
func TestClassifySubackReason_AllSpecCodes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		code     byte
		wantCode errcode.Code
		wantKind errcode.Kind
	}{
		{"0x00-GrantedQoS0", 0x00, "", errcode.KindInternal},
		{"0x01-GrantedQoS1", 0x01, "", errcode.KindInternal},
		{"0x02-GrantedQoS2", 0x02, "", errcode.KindInternal},
		{"0x80-UnspecifiedError", 0x80, ErrAdapterMQTTSubscribe, errcode.KindInternal},
		{"0x83-ImplementationSpecificError", 0x83, ErrAdapterMQTTSubscribe, errcode.KindInternal}, // was default; now explicit
		{"0x87-NotAuthorized", 0x87, ErrAdapterMQTTSubscribeNotAuthorized, errcode.KindInternal},
		{"0x8F-TopicFilterInvalid", 0x8F, ErrAdapterMQTTSubscribe, errcode.KindInternal},
		{"0x91-PacketIdentifierInUse", 0x91, ErrAdapterMQTTSubscribe, errcode.KindInternal}, // was default; now explicit
		{"0x97-QuotaExceeded", 0x97, ErrAdapterMQTTSubscribeRateLimited, errcode.KindUnavailable},
		{"0x9E-SharedSubscriptionsNotSupported", 0x9E, ErrAdapterMQTTSharedSubsUnsupported, errcode.KindInternal},
		{"0xA1-SubscriptionIdentifiersNotSupported", 0xA1, ErrAdapterMQTTSubscriptionIDsUnsupported, errcode.KindInternal},
		{"0xA2-WildcardSubscriptionsNotSupported", 0xA2, ErrAdapterMQTTSubscribe, errcode.KindInternal},
		// unknown byte → default behavior
		{"0xC0-unknown", 0xC0, ErrAdapterMQTTSubscribe, errcode.KindInternal},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			gotCode, gotKind := classifySubackReason(tc.code)
			if gotCode != tc.wantCode {
				t.Errorf("classifySubackReason(0x%02x) code = %q, want %q", tc.code, gotCode, tc.wantCode)
			}
			if gotKind != tc.wantKind {
				t.Errorf("classifySubackReason(0x%02x) kind = %v, want %v", tc.code, gotKind, tc.wantKind)
			}
		})
	}
}

// TestValidateReasonTable_PanicOnDuplicateCode ensures validateReasonTable panics
// when the same code appears more than once in a connackReason slice.
func TestValidateReasonTable_PanicOnDuplicateCode(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Errorf("validateReasonTable did not panic on duplicate code")
		}
	}()
	bad := []connackReason{
		{0x80, "UnspecifiedError", classTransient},
		{0x80, "UnspecifiedErrorDup", classTransient}, // duplicate
	}
	validateConnackReasonTable(bad)
}

// TestValidateReasonTable_PanicOnClassInvalid ensures validateReasonTable panics
// when any row carries classInvalid (the zero value sentinel).
func TestValidateReasonTable_PanicOnClassInvalid(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Errorf("validateReasonTable did not panic on classInvalid row")
		}
	}()
	bad := []connackReason{
		{0x80, "UnspecifiedError", classInvalid}, // classInvalid is the zero value
	}
	validateConnackReasonTable(bad)
}

// TestValidateAckReasonTable_PanicOnDuplicateCode ensures validateAckReasonTable
// panics when the same code appears more than once in an ackReason slice.
func TestValidateAckReasonTable_PanicOnDuplicateCode(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Errorf("validateAckReasonTable did not panic on duplicate code")
		}
	}()
	bad := []ackReason{
		{0x80, "A", ErrAdapterMQTTSubscribe, errcode.KindInternal},
		{0x80, "B", ErrAdapterMQTTSubscribe, errcode.KindInternal}, // duplicate
	}
	validateAckReasonTable("TEST", bad)
}

// TestValidateAckReasonTable_PanicOnMissingErrCodeForErrorReason ensures
// validateAckReasonTable panics when an error reason row (code >= 0x80) carries
// an empty errCode. The classifiers run only on the error path (publisher.go and
// connection.go both gate reason >= 0x80), so an error row with errCode == ""
// would let that path construct an empty errcode.Code; the init-time guard makes
// that table state a fail-fast instead.
func TestValidateAckReasonTable_PanicOnMissingErrCodeForErrorReason(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Errorf("validateAckReasonTable did not panic on error row with empty errCode")
		}
	}()
	bad := []ackReason{
		{0x80, "UnspecifiedError", "", errcode.KindInternal}, // error code, empty errCode
	}
	validateAckReasonTable("TEST", bad)
}

// TestValidateAckReasonTable_AllowsEmptyErrCodeForSuccessReason ensures the guard
// is asymmetric: a success reason row (code < 0x80) is allowed an empty errCode
// (the production tables use this for 0x00/0x01/0x02), so the validator must not
// panic on it.
func TestValidateAckReasonTable_AllowsEmptyErrCodeForSuccessReason(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("validateAckReasonTable panicked on success row with empty errCode: %v", r)
		}
	}()
	ok := []ackReason{
		{0x00, "Success", "", errcode.KindInternal}, // success code, empty errCode is legal
		{0x10, "NoMatchingSubscribers", ErrAdapterMQTTPublishNoSubscribers, errcode.KindUnavailable},
	}
	validateAckReasonTable("TEST", ok)
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
		ErrAdapterMQTTInvalidPublishTopic,
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
		ErrAdapterMQTTPublishNotAuthorized,
		ErrAdapterMQTTPublishPayloadFormatInvalid,
		ErrAdapterMQTTPublishCanceled,
		ErrAdapterMQTTPublishFailed,
		ErrAdapterMQTTPublisherCloseTimeout,
		// PR-3 additions
		ErrAdapterMQTTSubscribe,
		ErrAdapterMQTTSubscribeNotAuthorized,
		ErrAdapterMQTTSharedSubsUnsupported,
		ErrAdapterMQTTSubscribeRateLimited,
		ErrAdapterMQTTUnmarshalEnvelope,
		// Additional codes missing from original list
		ErrAdapterMQTTConnectCanceled,
		ErrAdapterMQTTAck,
		ErrAdapterMQTTPublisherCloseTimeout,
		ErrAdapterMQTTSubscriberCloseTimeout,
		ErrAdapterMQTTSubscriptionIDsUnsupported,
	}
	for _, c := range codes {
		if c == "" {
			t.Errorf("error code is empty string: %v", c)
		}
		// strings.HasPrefix is bounds-safe for codes shorter than the prefix;
		// a slice index ([:len(prefix)]) would panic on a short code instead of
		// reporting a clean test failure.
		if !strings.HasPrefix(string(c), "ERR_ADAPTER_MQTT_") {
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
		// 0x87 Not authorized — retryable (KindUnavailable, aligned with CONNACK 0x87 classPermanentRetain),
		// dedicated code (not the permanent ErrAdapterMQTTPublishRejected)
		{"not-authorized-0x87", 0x87, ErrAdapterMQTTPublishNotAuthorized, errcode.KindUnavailable},
		// 0x90 Topic Name invalid
		{"topic-name-invalid-0x90", 0x90, ErrAdapterMQTTPublishRejected, errcode.KindInvalid},
		// 0x97 Quota exceeded / rate limited
		{"quota-exceeded-0x97", 0x97, ErrAdapterMQTTPublishRateLimited, errcode.KindUnavailable},
		// 0x99 Payload format invalid — dedicated code, distinct from PayloadTooLarge (size)
		{"payload-format-invalid-0x99", 0x99, ErrAdapterMQTTPublishPayloadFormatInvalid, errcode.KindInvalid},
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

func TestClassifySubackReason(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		code     byte
		wantCode errcode.Code
		wantKind errcode.Kind
	}{
		{"unspecified-0x80", 0x80, ErrAdapterMQTTSubscribe, errcode.KindInternal},
		{"not-authorized-0x87", 0x87, ErrAdapterMQTTSubscribeNotAuthorized, errcode.KindInternal},
		{"topic-filter-invalid-0x8F", 0x8F, ErrAdapterMQTTSubscribe, errcode.KindInternal},
		{"quota-exceeded-0x97", 0x97, ErrAdapterMQTTSubscribeRateLimited, errcode.KindUnavailable},
		{"shared-subs-unsupported-0x9E", 0x9E, ErrAdapterMQTTSharedSubsUnsupported, errcode.KindInternal},
		{"sub-ids-unsupported-0xA1", 0xA1, ErrAdapterMQTTSubscriptionIDsUnsupported, errcode.KindInternal},
		{"wildcard-subs-unsupported-0xA2", 0xA2, ErrAdapterMQTTSubscribe, errcode.KindInternal},
		{"unknown-error-0x83", 0x83, ErrAdapterMQTTSubscribe, errcode.KindInternal},
		{"unknown-error-0xFF", 0xFF, ErrAdapterMQTTSubscribe, errcode.KindInternal},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			gotCode, gotKind := classifySubackReason(tc.code)
			if gotCode != tc.wantCode {
				t.Errorf("classifySubackReason(0x%02x) code = %q, want %q", tc.code, gotCode, tc.wantCode)
			}
			if gotKind != tc.wantKind {
				t.Errorf("classifySubackReason(0x%02x) kind = %v, want %v", tc.code, gotKind, tc.wantKind)
			}
		})
	}
}

func TestSubackReasonName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		code byte
		want string
	}{
		{0x00, "GrantedQoS0"},
		{0x01, "GrantedQoS1"},
		{0x02, "GrantedQoS2"},
		{0x80, "UnspecifiedError"},
		{0x87, "NotAuthorized"},
		{0x8F, "TopicFilterInvalid"},
		{0x97, "QuotaExceeded"},
		{0x9E, "SharedSubscriptionsNotSupported"},
		{0xA1, "SubscriptionIdentifiersNotSupported"},
		{0xA2, "WildcardSubscriptionsNotSupported"},
		{0x7F, "Unknown"},
		{0xFD, "Unknown"},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(fmt.Sprintf("0x%02x", tc.code), func(t *testing.T) {
			t.Parallel()
			got := subackReasonName(tc.code)
			if got != tc.want {
				t.Errorf("subackReasonName(0x%02x) = %q, want %q", tc.code, got, tc.want)
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

// TestBuildConnackOpts_ReasonNameRedaction locks the CONNACK reason-name
// public/internal channel split produced by buildConnackOpts (via
// reasonDetailOptions): the numeric reasonCode is ALWAYS a public detail
// (non-sensitive operator diagnostic), while for auth-related reason codes
// (0x86 BadUserNameOrPassword / 0x87 NotAuthorized / 0x8C BadAuthenticationMethod)
// the human-readable reasonName is demoted to the Internal channel so it cannot
// help an attacker enumerate "credentials wrong vs authz missing". Non-auth
// codes keep reasonName public. Built on autopaho.ConnackError.ReasonCode.
func TestBuildConnackOpts_ReasonNameRedaction(t *testing.T) {
	tests := []struct {
		name             string
		reasonCode       byte
		reasonNamePublic bool // false => auth-related: reasonName demoted to Internal
	}{
		{"bad-user-pw-0x86-auth", 0x86, false},
		{"not-authorized-0x87-auth", 0x87, false},
		{"bad-auth-method-0x8C-auth", 0x8C, false},
		{"unspecified-0x80-nonauth", 0x80, true},
		{"server-unavailable-0x88-nonauth", 0x88, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cause := &autopaho.ConnackError{ReasonCode: tt.reasonCode}
			e := errcode.New(errcode.KindInternal, ErrAdapterMQTTConnectPermanent,
				"mqtt: connection rejected", buildConnackOpts(cause)...)
			assertConnackOptsRedaction(t, e, tt.reasonNamePublic)
		})
	}
}

// assertConnackOptsRedaction checks the public/internal channel split for a
// buildConnackOpts-produced errcode.Error. Extracted to reduce cognitive
// complexity of TestBuildConnackOpts_ReasonNameRedaction.
func assertConnackOptsRedaction(t *testing.T, e *errcode.Error, reasonNamePublic bool) {
	t.Helper()
	if !hasPublicDetailKey(e.Details, reasonDetailKeyCode) {
		t.Errorf("reasonCode must always be a public detail")
	}
	if got := hasPublicDetailKey(e.Details, reasonDetailKeyName); got != reasonNamePublic {
		t.Errorf("reasonName public = %v, want %v", got, reasonNamePublic)
	}
	if reasonNamePublic {
		return
	}
	if hasPublicDetailKey(e.Details, reasonDetailKeyName) {
		t.Errorf("auth-related reasonName must NOT be in public details")
	}
	if !hasInternalDetailKey(e.InternalDetails, reasonDetailKeyName) {
		t.Errorf("auth-related reasonName must be present in internal details")
	}
}

func hasPublicDetailKey(ds []errcode.PublicDetail, key string) bool {
	for _, d := range ds {
		if d.Key() == key {
			return true
		}
	}
	return false
}

func hasInternalDetailKey(ds []errcode.InternalDetail, key string) bool {
	for _, d := range ds {
		if d.Key() == key {
			return true
		}
	}
	return false
}

// TestTopicErrorCodeWireStringsPreserved asserts that the four topic-namespace
// error codes re-exported from adapters/mqtt/internal/topicns keep their wire
// strings after the type-seal refactor (#1247). Ops/alerting rules depend on
// these exact string values.
func TestTopicErrorCodeWireStringsPreserved(t *testing.T) {
	t.Parallel()
	cases := map[errcode.Code]string{
		ErrAdapterMQTTInvalidTopicNamespace:  "ERR_ADAPTER_MQTT_INVALID_TOPIC_NAMESPACE",
		ErrAdapterMQTTTopicOutsideNamespace:  "ERR_ADAPTER_MQTT_TOPIC_OUTSIDE_NAMESPACE",
		ErrAdapterMQTTInvalidPublishTopic:    "ERR_ADAPTER_MQTT_INVALID_PUBLISH_TOPIC",
		ErrAdapterMQTTInvalidSubscribeFilter: "ERR_ADAPTER_MQTT_INVALID_SUBSCRIBE_FILTER",
	}
	for got, want := range cases {
		if string(got) != want {
			t.Errorf("error code wire string drifted: got %q, want %q", got, want)
		}
	}
}

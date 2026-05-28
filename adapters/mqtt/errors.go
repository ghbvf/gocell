// Package mqtt provides an MQTT v5 adapter for GoCell. It wraps
// autopaho (eclipse/paho.golang) for connection management and surfaces
// the GoCell adapter lifecycle (healthz probe, structured errors, payload
// redaction).
//
// ref: errcode.PublicDetail sealed-struct pattern (pkg/errcode/details.go)
// ref: pkg/redaction single-source sensitivity list
package mqtt

import (
	"crypto/tls"
	"crypto/x509"
	"errors"

	"github.com/eclipse/paho.golang/autopaho"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// MQTT adapter error codes. All codes carry the ERR_ADAPTER_MQTT_ prefix
// so operators can route them independently from other adapter errors.
//
// NOTE: exported package-scope error sentinels must be errcode.Code constants,
// not var Err* = errors.New(...). The archtest EXPORTED-ERROR-NEW-01 enforces
// this. Functions returning errors wrap these codes via errcode.New/Wrap.
const (
	// ErrAdapterMQTTInvalidConfig signals a malformed or missing adapter configuration.
	ErrAdapterMQTTInvalidConfig errcode.Code = "ERR_ADAPTER_MQTT_INVALID_CONFIG"

	// ErrAdapterMQTTInvalidClientID signals a client identifier that fails
	// validation (empty, bad chars, too long, etc.).
	ErrAdapterMQTTInvalidClientID errcode.Code = "ERR_ADAPTER_MQTT_INVALID_CLIENT_ID"

	// ErrAdapterMQTTInvalidTopicNamespace signals a topic namespace prefix that
	// fails validation.
	ErrAdapterMQTTInvalidTopicNamespace errcode.Code = "ERR_ADAPTER_MQTT_INVALID_TOPIC_NAMESPACE"

	// ErrAdapterMQTTTopicOutsideNamespace signals that a publish or subscribe
	// topic/filter does not fall within the declared TopicNamespace.
	ErrAdapterMQTTTopicOutsideNamespace errcode.Code = "ERR_ADAPTER_MQTT_TOPIC_OUTSIDE_NAMESPACE"

	// ErrAdapterMQTTConnect signals a transient connection failure (network
	// timeout, server unavailable, quota exceeded). autopaho will retry.
	ErrAdapterMQTTConnect errcode.Code = "ERR_ADAPTER_MQTT_CONNECT"

	// ErrAdapterMQTTConnectTimeout signals that the initial connection attempt
	// exceeded the configured timeout budget.
	ErrAdapterMQTTConnectTimeout errcode.Code = "ERR_ADAPTER_MQTT_CONNECT_TIMEOUT"

	// ErrAdapterMQTTConnectPermanent signals a non-retryable connection refusal:
	// either a bootstrap-fatal CONNACK reason code (bad protocol version,
	// malformed client ID, etc.) or a credentials/authorization rejection that
	// requires operator intervention. Bootstrap-fatal causes immediate shutdown;
	// authorization rejection triggers a 503 readyz + continued retrying.
	ErrAdapterMQTTConnectPermanent errcode.Code = "ERR_ADAPTER_MQTT_CONNECT_PERMANENT"

	// ErrAdapterMQTTNeverConnected signals that an operation was attempted before
	// the adapter has completed its first successful connection.
	ErrAdapterMQTTNeverConnected errcode.Code = "ERR_ADAPTER_MQTT_NEVER_CONNECTED"

	// ErrAdapterMQTTClosed signals that an operation was attempted after the
	// adapter has been shut down.
	ErrAdapterMQTTClosed errcode.Code = "ERR_ADAPTER_MQTT_CLOSED"

	// ErrAdapterMQTTPayloadTooLarge signals that a publish payload exceeds the
	// maximum allowed size.
	ErrAdapterMQTTPayloadTooLarge errcode.Code = "ERR_ADAPTER_MQTT_PAYLOAD_TOO_LARGE"

	// ErrAdapterMQTTInvalidSubscribeFilter signals that a subscribe filter has
	// invalid wildcard placement (e.g. "#" not at the last level, or a wildcard
	// character embedded inside a non-wildcard level token). This is distinct
	// from ErrAdapterMQTTInvalidTopicNamespace which means the namespace prefix
	// itself is malformed, and ErrAdapterMQTTTopicOutsideNamespace which means
	// the filter's non-wildcard head falls outside the declared namespace.
	ErrAdapterMQTTInvalidSubscribeFilter errcode.Code = "ERR_ADAPTER_MQTT_INVALID_SUBSCRIBE_FILTER"

	// ErrAdapterMQTTPubAckTimeout signals that no PUBACK was received within
	// the configured PublishTimeout budget. The broker may have received the
	// message but the acknowledgement was lost; callers should treat this as a
	// retryable transient failure and apply idempotency controls before retrying.
	ErrAdapterMQTTPubAckTimeout errcode.Code = "ERR_ADAPTER_MQTT_PUBACK_TIMEOUT"

	// ErrAdapterMQTTPublishNoSubscribers signals that the broker returned
	// PUBACK reason code 0x10 (No Matching Subscribers): the message was
	// accepted but no active subscriber matched the topic filter. This is an
	// informational condition for QoS 1; the publisher may log a warning and
	// continue rather than treating it as a hard error.
	ErrAdapterMQTTPublishNoSubscribers errcode.Code = "ERR_ADAPTER_MQTT_PUBLISH_NO_SUBSCRIBERS"

	// ErrAdapterMQTTPublishRejected signals that the broker returned a PUBACK
	// reason code indicating the message was rejected: unspecified error (0x80),
	// implementation-specific error (0x83), not authorized (0x87), or topic name
	// invalid (0x90). These conditions typically require operator intervention or
	// a client-side fix before retrying.
	ErrAdapterMQTTPublishRejected errcode.Code = "ERR_ADAPTER_MQTT_PUBLISH_REJECTED"

	// ErrAdapterMQTTPublishRateLimited signals that the broker returned PUBACK
	// reason code 0x97 (Quota Exceeded), indicating the publisher has been
	// rate-limited or its message quota is exhausted. Callers should back off
	// and retry after a delay.
	ErrAdapterMQTTPublishRateLimited errcode.Code = "ERR_ADAPTER_MQTT_PUBLISH_RATE_LIMITED"

	// ErrAdapterMQTTPublishCanceled signals that a publish call was canceled
	// because the caller's context was canceled before the broker responded.
	ErrAdapterMQTTPublishCanceled errcode.Code = "ERR_ADAPTER_MQTT_PUBLISH_CANCELED"

	// ErrAdapterMQTTPublishFailed signals a transport-level publish failure (the
	// underlying autopaho returned a non-context error and not a PUBACK reason
	// code). Distinct from ErrAdapterMQTTConnect which is connection-side, and
	// from ErrAdapterMQTTPubAckTimeout / PublishRejected which are PUBACK-side.
	ErrAdapterMQTTPublishFailed errcode.Code = "ERR_ADAPTER_MQTT_PUBLISH_FAILED"

	// ErrAdapterMQTTPublisherCloseTimeout signals that Publisher.Close exceeded
	// its drain budget waiting for in-flight publishes to complete. Distinct from
	// ErrAdapterMQTTPubAckTimeout (single-publish PUBACK timeout) — Close timeout
	// indicates one or more goroutines are stuck.
	ErrAdapterMQTTPublisherCloseTimeout errcode.Code = "ERR_ADAPTER_MQTT_PUBLISHER_CLOSE_TIMEOUT"
)

// connackClass classifies an OnConnectError into one of three categories.
// The classification drives autopaho reconnect behavior and the readyz probe.
type connackClass uint8

const (
	// classTransient: network/timeout/0x88/0x97 — autopaho retries normally.
	classTransient connackClass = iota
	// classBootstrapFatal: 0x81 MalformedPacket / 0x82 ProtocolError /
	// 0x84 UnsupportedProtocolVersion / 0x85 ClientIdentifierNotValid /
	// 0x8A Banned / 0x95 PacketTooLarge + TLS x509 — operator
	// must fix the deployment; fail-fast at bootstrap.
	classBootstrapFatal
	// classPermanentRetain: 0x87 NotAuthorized, 0x86 BadUserOrPass — credentials
	// issue; set readyz 503 and keep retrying until operator fixes credentials.
	classPermanentRetain
)

// classifyConnackReason inspects an OnConnectError and maps it to a
// connackClass and the errcode.Code to stamp on the returned error.
//
// It recovers *autopaho.ConnackError via errors.As and maps the CONNACK
// ReasonCode field (byte) to a class. TLS handshake errors are classified as
// classBootstrapFatal regardless of the ConnackError path.
//
// Reason-code → class mapping (ref: MQTT v5.0 spec §3.2.2.2):
//
//	0x87 NotAuthorized                → classPermanentRetain + ErrAdapterMQTTConnectPermanent
//	0x86 BadUserNameOrPassword        → classPermanentRetain + ErrAdapterMQTTConnectPermanent
//	0x81 MalformedPacket              → classBootstrapFatal  + ErrAdapterMQTTConnectPermanent
//	0x82 ProtocolError                → classBootstrapFatal  + ErrAdapterMQTTConnectPermanent
//	0x84 UnsupportedProtocolVersion   → classBootstrapFatal  + ErrAdapterMQTTConnectPermanent
//	0x85 ClientIdentifierNotValid     → classBootstrapFatal  + ErrAdapterMQTTConnectPermanent
//	0x8A Banned                       → classBootstrapFatal  + ErrAdapterMQTTConnectPermanent
//	0x95 PacketTooLarge               → classBootstrapFatal  + ErrAdapterMQTTConnectPermanent
//	0x88 ServerUnavailable            → classTransient       + ErrAdapterMQTTConnect
//	0x97 QuotaExceeded                → classTransient       + ErrAdapterMQTTConnect
//	default                           → classTransient       + ErrAdapterMQTTConnect
//
// ref: MQTT v5.0 spec §3.2.2.2 Connect Reason Code table
func classifyConnackReason(err error) (connackClass, errcode.Code) {
	if isTLSHandshakeError(err) {
		return classBootstrapFatal, ErrAdapterMQTTConnectPermanent
	}

	var connackErr *autopaho.ConnackError
	if !errors.As(err, &connackErr) {
		return classTransient, ErrAdapterMQTTConnect
	}

	switch connackErr.ReasonCode {
	case 0x87, // NotAuthorized
		0x86, // BadUserNameOrPassword
		0x8C: // BadAuthenticationMethod — broker rejects auth method itself; operator must fix client config.
		return classPermanentRetain, ErrAdapterMQTTConnectPermanent

	case 0x81, // MalformedPacket
		0x82, // ProtocolError
		0x84, // UnsupportedProtocolVersion
		0x85, // ClientIdentifierNotValid
		0x8A, // Banned
		0x95: // PacketTooLarge (bootstrap fatal: maximum packet size mismatch)
		return classBootstrapFatal, ErrAdapterMQTTConnectPermanent

	default:
		// 0x88 ServerUnavailable, 0x97 QuotaExceeded, and all unrecognized codes.
		return classTransient, ErrAdapterMQTTConnect
	}
}

// connackReasonNames is the single source of MQTT v5 CONNACK reason code →
// spec-defined name mapping (MQTT v5.0 §3.2.2.2 Connect Reason Code table).
// Encoded as a const-literal map so message PII constraints are satisfied
// (names are programmer-written, not runtime data).
var connackReasonNames = map[byte]string{
	0x00: "Success",
	0x80: "UnspecifiedError",
	0x81: "MalformedPacket",
	0x82: "ProtocolError",
	0x83: "ImplementationSpecificError",
	0x84: "UnsupportedProtocolVersion",
	0x85: "ClientIdentifierNotValid",
	0x86: "BadUserNameOrPassword",
	0x87: "NotAuthorized",
	0x88: "ServerUnavailable",
	0x89: "ServerBusy",
	0x8A: "Banned",
	0x8C: "BadAuthenticationMethod",
	0x90: "TopicNameInvalid",
	0x95: "PacketTooLarge",
	0x97: "QuotaExceeded",
	0x99: "PayloadFormatInvalid",
	0x9A: "RetainNotSupported",
	0x9B: "QoSNotSupported",
	0x9C: "UseAnotherServer",
	0x9D: "ServerMoved",
	0x9F: "ConnectionRateExceeded",
}

// connackReasonName returns the spec name for an MQTT v5 CONNACK reason
// code. Unknown codes return "Unknown" so operators can still match the
// numeric reasonCode field.
func connackReasonName(code byte) string {
	if name, ok := connackReasonNames[code]; ok {
		return name
	}
	return "Unknown"
}

// classifyPubackReason maps an MQTT v5 PUBACK reason code (byte) to the
// corresponding errcode.Code and errcode.Kind.
//
// Reason-code → (code, kind) mapping (ref: MQTT v5.0 spec §3.4.2.1):
//
//	0x00 Success                  → ("", KindInternal)               — Ack path; caller MUST guard ReasonCode != 0x00 before calling
//	0x10 NoMatchingSubscribers    → (ErrAdapterMQTTPublishNoSubscribers, KindUnavailable)
//	0x80 UnspecifiedError         → (ErrAdapterMQTTPublishRejected,     KindInternal)
//	0x83 ImplementationSpecific   → (ErrAdapterMQTTPublishRejected,     KindInternal)
//	0x87 NotAuthorized            → (ErrAdapterMQTTPublishRejected,     KindUnavailable) — retryable; operator fixes ACL
//	0x90 TopicNameInvalid         → (ErrAdapterMQTTPublishRejected,     KindInvalid)
//	0x97 QuotaExceeded            → (ErrAdapterMQTTPublishRateLimited,  KindUnavailable)
//	0x99 PayloadFormatInvalid     → (ErrAdapterMQTTPayloadTooLarge,     KindInvalid)
//	default                       → (ErrAdapterMQTTPublishRejected,     KindInternal)
//
// ref: MQTT v5.0 spec §3.4.2.1 PUBACK Reason Code table
func classifyPubackReason(code byte) (errcode.Code, errcode.Kind) {
	switch code {
	case 0x00: // Success — Ack path; caller MUST guard ReasonCode != 0x00 before calling
		return "", errcode.KindInternal
	case 0x10: // No Matching Subscribers
		return ErrAdapterMQTTPublishNoSubscribers, errcode.KindUnavailable
	case 0x80: // Unspecified Error
		return ErrAdapterMQTTPublishRejected, errcode.KindInternal
	case 0x83: // Implementation Specific Error
		return ErrAdapterMQTTPublishRejected, errcode.KindInternal
	case 0x87: // Not Authorized — retryable after operator fixes ACL (aligned with CONNACK 0x87 classPermanentRetain)
		return ErrAdapterMQTTPublishRejected, errcode.KindUnavailable
	case 0x90: // Topic Name Invalid
		return ErrAdapterMQTTPublishRejected, errcode.KindInvalid
	case 0x97: // Quota Exceeded
		return ErrAdapterMQTTPublishRateLimited, errcode.KindUnavailable
	case 0x99: // Payload Format Invalid
		return ErrAdapterMQTTPayloadTooLarge, errcode.KindInvalid
	default:
		return ErrAdapterMQTTPublishRejected, errcode.KindInternal
	}
}

// pubackReasonNames is the single source of MQTT v5 PUBACK reason code →
// spec-defined name mapping (MQTT v5.0 §3.4.2.1 PUBACK Reason Code table).
// Distinct from connackReasonNames — the two tables are different despite
// some overlapping codes (CONNACK §3.2.2.2 vs PUBACK §3.4.2.1).
var pubackReasonNames = map[byte]string{
	0x00: "Success",
	0x10: "NoMatchingSubscribers",
	0x80: "UnspecifiedError",
	0x83: "ImplementationSpecificError",
	0x87: "NotAuthorized",
	0x90: "TopicNameInvalid",
	0x91: "PacketIdentifierInUse",
	0x97: "QuotaExceeded",
	0x99: "PayloadFormatInvalid",
}

// pubackReasonName returns the spec name for an MQTT v5 PUBACK reason code.
// Unknown codes return "Unknown" so operators can still match the numeric
// reasonCode field in structured logs.
func pubackReasonName(code byte) string {
	if name, ok := pubackReasonNames[code]; ok {
		return name
	}
	return "Unknown"
}

// isTLSHandshakeError reports whether err (or any error in its chain) is a
// TLS/x509 certificate verification failure. These are always classBootstrapFatal
// because the server certificate or CA configuration is wrong and no amount of
// retry will fix it without operator intervention.
//
// Recognized types:
//   - *tls.CertificateVerificationError (Go 1.20+)
//   - x509.UnknownAuthorityError
//   - x509.HostnameError
func isTLSHandshakeError(err error) bool {
	if err == nil {
		return false
	}
	var certErr *tls.CertificateVerificationError
	if errors.As(err, &certErr) {
		return true
	}
	var unknownAuth x509.UnknownAuthorityError
	if errors.As(err, &unknownAuth) {
		return true
	}
	var hostErr x509.HostnameError
	return errors.As(err, &hostErr)
}

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

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

	"github.com/ghbvf/gocell/adapters/mqtt/internal/topicns"
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
	// Re-exported from adapters/mqtt/internal/topicns, where the value is defined
	// alongside the validation funnel (avoids a circular import).
	ErrAdapterMQTTInvalidTopicNamespace errcode.Code = topicns.ErrInvalidNamespace

	// ErrAdapterMQTTTopicOutsideNamespace signals that a publish or subscribe
	// topic/filter does not fall within the declared TopicNamespace.
	ErrAdapterMQTTTopicOutsideNamespace errcode.Code = topicns.ErrTopicOutsideNamespace

	// ErrAdapterMQTTInvalidPublishTopic signals that a publish topic is malformed
	// under MQTT v5 rules: empty topic name, or a topic containing +/# wildcards.
	// Subscribe filters use ErrAdapterMQTTInvalidSubscribeFilter because wildcard
	// placement is legal-but-constrained there; publish topics are a distinct
	// failure domain and must never reuse the subscribe-filter code.
	ErrAdapterMQTTInvalidPublishTopic errcode.Code = topicns.ErrInvalidPublishTopic

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

	// ErrAdapterMQTTConnectCanceled signals that a wait for connection was aborted
	// by context cancellation (caller abort or a propagated lifecycle-ctx cancel)
	// rather than the connect-deadline budget elapsing. Distinct from
	// ErrAdapterMQTTConnectTimeout so ops can tell a deliberate shutdown/abort from
	// a slow or unreachable broker when triaging startup failures.
	ErrAdapterMQTTConnectCanceled errcode.Code = "ERR_ADAPTER_MQTT_CONNECT_CANCELED"

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
	ErrAdapterMQTTInvalidSubscribeFilter errcode.Code = topicns.ErrInvalidSubscribeFilter

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
	// reason code indicating the message was permanently rejected: unspecified
	// error (0x80), implementation-specific error (0x83), or topic name invalid
	// (0x90). These conditions require a client-side fix before retrying.
	// Not-authorized (0x87) is NOT covered here — it is retryable-after-operator-
	// ACL-fix and carries its own ErrAdapterMQTTPublishNotAuthorized code (below);
	// payload-format-invalid (0x99) carries ErrAdapterMQTTPublishPayloadFormatInvalid.
	ErrAdapterMQTTPublishRejected errcode.Code = "ERR_ADAPTER_MQTT_PUBLISH_REJECTED"

	// ErrAdapterMQTTPublishRateLimited signals that the broker returned PUBACK
	// reason code 0x97 (Quota Exceeded), indicating the publisher has been
	// rate-limited or its message quota is exhausted. Callers should back off
	// and retry after a delay.
	ErrAdapterMQTTPublishRateLimited errcode.Code = "ERR_ADAPTER_MQTT_PUBLISH_RATE_LIMITED"

	// ErrAdapterMQTTPublishNotAuthorized signals that the broker returned PUBACK
	// reason code 0x87 (Not Authorized): the publisher's ACL does not permit
	// publishing to the topic. Classified KindUnavailable (retryable) — aligned
	// with CONNACK 0x87 classPermanentRetain semantics: the condition persists
	// until an operator fixes the broker-side ACL, after which retries succeed.
	// The outbox relay should keep retrying (bounded by its retry budget, then
	// DLX) rather than treating it as a permanent client error. Decision recorded
	// in ADR docs/architecture/202605281200-048-adr-mqtt-adapter.md §错误映射表.
	ErrAdapterMQTTPublishNotAuthorized errcode.Code = "ERR_ADAPTER_MQTT_PUBLISH_NOT_AUTHORIZED"

	// ErrAdapterMQTTPublishPayloadFormatInvalid signals that the broker returned
	// PUBACK reason code 0x99 (Payload Format Invalid): the payload does not
	// conform to the declared Payload Format Indicator / Content Type (e.g. a
	// UTF-8 declaration carrying non-UTF-8 bytes). Classified KindInvalid —
	// semantically distinct from ErrAdapterMQTTPayloadTooLarge (size), which it
	// previously and incorrectly shared. A client-side payload-encoding fix is
	// required.
	ErrAdapterMQTTPublishPayloadFormatInvalid errcode.Code = "ERR_ADAPTER_MQTT_PUBLISH_PAYLOAD_FORMAT_INVALID"

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

	// ErrAdapterMQTTSubscriberCloseTimeout signals that Subscriber.StopIntake
	// exceeded its drain budget waiting for in-flight handler goroutines to
	// settle. Distinct from ErrAdapterMQTTClosed (which means the adapter is
	// already shut down) — a drain timeout means the subscriber is still draining
	// but one or more handlers did not finish within StopIntakeDrainTimeout.
	// Mirrors ErrAdapterMQTTPublisherCloseTimeout on the publish side.
	ErrAdapterMQTTSubscriberCloseTimeout errcode.Code = "ERR_ADAPTER_MQTT_SUBSCRIBER_CLOSE_TIMEOUT"

	// ErrAdapterMQTTSubscribe signals a generic SUBSCRIBE failure: the broker
	// returned a SUBACK reason byte >= 0x80 that is not covered by a more
	// specific code (or the underlying transport call failed). Classified
	// KindInternal — a fail-fast condition that requires operator / config
	// intervention rather than blind retry.
	ErrAdapterMQTTSubscribe errcode.Code = "ERR_ADAPTER_MQTT_SUBSCRIBE"

	// ErrAdapterMQTTSubscribeNotAuthorized signals that the broker returned
	// SUBACK reason code 0x87 (Not Authorized): the subscriber's ACL does not
	// permit subscribing to the requested filter. Classified KindInternal
	// (fail-fast) — a broker-side ACL misconfiguration that retrying cannot
	// resolve; an operator must fix the ACL.
	ErrAdapterMQTTSubscribeNotAuthorized errcode.Code = "ERR_ADAPTER_MQTT_SUBSCRIBE_NOT_AUTHORIZED"

	// ErrAdapterMQTTSharedSubsUnsupported signals that the broker returned SUBACK
	// reason code 0x9E (Shared Subscriptions Not Supported). GoCell's subscriber
	// uses MQTT v5 shared subscriptions ("$share/...") for consumer-group
	// semantics; a broker that does not support them is a deployment
	// misconfiguration. Classified KindInternal (fail-fast).
	ErrAdapterMQTTSharedSubsUnsupported errcode.Code = "ERR_ADAPTER_MQTT_SHARED_SUBS_UNSUPPORTED"

	// ErrAdapterMQTTSubscribeRateLimited signals that the broker returned SUBACK
	// reason code 0x97 (Quota Exceeded): the subscription quota is exhausted.
	// Classified KindUnavailable (transient) — the subscriber should back off
	// and retry.
	ErrAdapterMQTTSubscribeRateLimited errcode.Code = "ERR_ADAPTER_MQTT_SUBSCRIBE_RATE_LIMITED"

	// ErrAdapterMQTTUnmarshalEnvelope signals that a received PUBLISH payload
	// could not be decoded into the expected event envelope. Used by the
	// subscriber to classify poison messages: the bytes are permanently
	// undecodable, so the message must be dead-lettered rather than retried.
	ErrAdapterMQTTUnmarshalEnvelope errcode.Code = "ERR_ADAPTER_MQTT_UNMARSHAL_ENVELOPE"

	// ErrAdapterMQTTAck signals a manual-acknowledgement failure on the receive
	// path: either no delivering client is available to ack through, or the
	// broker rejected the manual PUBACK. Distinct from ErrAdapterMQTTSubscribe (a
	// SUBSCRIBE/SUBACK failure) — ack is a separate operational domain
	// (post-delivery settlement) and must be classified independently for
	// operator alerting / metrics. Classified KindUnavailable (transient): the
	// message will be redelivered and the idempotency key guards reprocessing.
	ErrAdapterMQTTAck errcode.Code = "ERR_ADAPTER_MQTT_ACK"

	// ErrAdapterMQTTSubscriptionIDsUnsupported signals that the broker returned
	// SUBACK reason code 0xA1 (Subscription Identifiers Not Supported). GoCell's
	// subscriber routes each delivered PUBLISH to its originating subscription via
	// the MQTT v5 Subscription Identifier; a broker that does not support them
	// cannot disambiguate overlapping consumer-group subscriptions on one
	// connection, so this is a fail-fast deployment misconfiguration
	// (KindInternal).
	ErrAdapterMQTTSubscriptionIDsUnsupported errcode.Code = "ERR_ADAPTER_MQTT_SUBSCRIPTION_IDS_UNSUPPORTED"
)

// reasonDetailKeyCode / reasonDetailKeyName are the public/internal detail keys
// carrying an MQTT reason code and its spec name.
const (
	reasonDetailKeyCode = "reasonCode"
	reasonDetailKeyName = "reasonName"
)

// reasonDetailOptions is the single funnel for constructing errcode details that
// carry an MQTT reason code + its spec name (CONNACK / SUBACK / PUBACK error
// builders all route through it). For auth-related reason codes the
// human-readable name is moved to the Internal channel (server-log only) so it
// does not help an attacker enumerate "credentials wrong vs authz missing"; only
// the numeric reasonCode stays in Public details. For non-auth codes both are
// Public for operator diagnostics.
//
// archtest REASON-NAME-REDACTION-01 bans PublicString("reasonName", …) anywhere
// in adapters/mqtt outside this function, so the auth-redaction decision cannot
// be re-introduced (or forgotten) per-packet.
func reasonDetailOptions(reasonCode int, reasonName string, isAuth bool) []errcode.Option {
	if isAuth {
		return []errcode.Option{
			errcode.WithDetails(errcode.PublicInt(reasonDetailKeyCode, reasonCode)),
			errcode.WithInternal(errcode.InternalAttr(reasonDetailKeyName, reasonName)),
		}
	}
	return []errcode.Option{
		errcode.WithDetails(
			errcode.PublicInt(reasonDetailKeyCode, reasonCode),
			errcode.PublicString(reasonDetailKeyName, reasonName),
		),
	}
}

// isAuthRelatedSubackCode reports whether a SUBACK reason code is auth-related.
// The only auth-related SUBACK code is 0x87 (Not Authorized); its reason name is
// moved to the Internal channel by reasonDetailOptions (parallel to the CONNACK
// auth-code redaction in isAuthRelatedConnackCode).
func isAuthRelatedSubackCode(code byte) bool {
	return code == 0x87
}

// isAuthRelatedPubackCode reports whether a PUBACK reason code is auth-related.
// The only auth-related PUBACK code is 0x87 (Not Authorized); its reason name is
// moved to the Internal channel by reasonDetailOptions.
func isAuthRelatedPubackCode(code byte) bool {
	return code == 0x87
}

// connackClass classifies an OnConnectError into one of three categories.
// The classification drives autopaho reconnect behavior and the readyz probe.
//
// classInvalid is the zero value and is used as a sentinel for init-time
// table validation — any row whose class field is unset will be caught by
// validateConnackReasonTable before the process accepts connections.
type connackClass uint8

const (
	// classInvalid is the zero value of connackClass. It is never a valid
	// classification; a row in connackReasonTable that omits its class field
	// (zero-initialized) will be caught by validateConnackReasonTable at init.
	classInvalid connackClass = iota
	// classTransient: network/timeout/0x88/0x97 — autopaho retries normally.
	classTransient
	// classBootstrapFatal: 0x81 MalformedPacket / 0x82 ProtocolError /
	// 0x84 UnsupportedProtocolVersion / 0x85 ClientIdentifierNotValid /
	// 0x8A Banned / 0x95 PacketTooLarge + TLS x509 — operator
	// must fix the deployment; fail-fast at bootstrap.
	classBootstrapFatal
	// classPermanentRetain: 0x87 NotAuthorized, 0x86 BadUserOrPass — credentials
	// issue; set readyz 503 and keep retrying until operator fixes credentials.
	classPermanentRetain
)

// ─── Single-source reason-code tables ────────────────────────────────────────
//
// Each packet type (CONNACK / PUBACK / SUBACK) has ONE table that is the sole
// source of truth for:
//   - the spec-defined reason code (byte)
//   - the spec-defined name string
//   - the classification / errcode mapping
//
// All accessor functions and classifiers are derived from these tables via
// indexReasonsByCode. Positional struct literals are MANDATORY (no key:value
// syntax) so that adding a field to the row type is a compile error at every
// existing row — making it impossible to add a code without classifying it.
//
// ref: MQTT v5.0 spec §3.2.2.2 (CONNACK), §3.4.2.1 (PUBACK), §3.9.3 (SUBACK)
// archtest: MQTT-CONNACK-REASON-TABLE-COMPLETE-01
//           MQTT-PUBACK-REASON-TABLE-COMPLETE-01
//           MQTT-SUBACK-REASON-TABLE-COMPLETE-01
//           MQTT-REASON-TABLE-POSITIONAL-01

// connackReason is one row of the CONNACK reason-code single-source table.
// Positional literals are mandatory — named-field literals would allow omitting
// the class field (leaving classInvalid) without a compile error.
type connackReason struct {
	code  byte
	name  string
	class connackClass
}

// ackReason is one row of the PUBACK / SUBACK reason-code single-source tables.
// Positional literals are mandatory — same rationale as connackReason.
type ackReason struct {
	code    byte
	name    string
	errCode errcode.Code
	kind    errcode.Kind
}

// connackReasonTable is the CONNACK single-source table (MQTT v5 §3.2.2.2).
// 22 rows, full spec coverage. Classes annotated with inline reasons where they
// differ from the "obvious" default.
//
// archtest MQTT-CONNACK-REASON-TABLE-COMPLETE-01 locks this set to the exact
// 22 codes of §3.2.2.2. MQTT-REASON-TABLE-POSITIONAL-01 asserts no row uses
// named-field syntax.
var connackReasonTable = []connackReason{
	{0x00, "Success", classTransient}, // success; classifier unreachable (error path only) — parity padding
	{0x80, "UnspecifiedError", classTransient},
	{0x81, "MalformedPacket", classBootstrapFatal},
	{0x82, "ProtocolError", classBootstrapFatal},
	{0x83, "ImplementationSpecificError", classTransient},
	{0x84, "UnsupportedProtocolVersion", classBootstrapFatal},
	{0x85, "ClientIdentifierNotValid", classBootstrapFatal},
	{0x86, "BadUserNameOrPassword", classPermanentRetain},
	{0x87, "NotAuthorized", classPermanentRetain},
	{0x88, "ServerUnavailable", classTransient},
	{0x89, "ServerBusy", classTransient},
	{0x8A, "Banned", classBootstrapFatal},
	{0x8C, "BadAuthenticationMethod", classPermanentRetain},
	{0x90, "TopicNameInvalid", classBootstrapFatal}, // reclassified transient→fatal: will-topic client config error
	{0x95, "PacketTooLarge", classBootstrapFatal},
	{0x97, "QuotaExceeded", classTransient},
	{0x99, "PayloadFormatInvalid", classBootstrapFatal}, // reclassified: will-payload format error
	{0x9A, "RetainNotSupported", classBootstrapFatal},   // reclassified: will-retain vs broker capability mismatch
	{0x9B, "QoSNotSupported", classBootstrapFatal},      // reclassified: will-QoS vs broker capability mismatch
	{0x9C, "UseAnotherServer", classTransient},          // temporary redirect; may lift, retry safe
	{0x9D, "ServerMoved", classBootstrapFatal},          // reclassified: permanent redirect; same endpoint never resolves
	{0x9F, "ConnectionRateExceeded", classTransient},
}

// pubackReasonTable is the PUBACK single-source table (MQTT v5 §3.4.2.1).
// 9 rows, full spec coverage. 0x91 is now explicit (was default in old switch).
//
// archtest MQTT-PUBACK-REASON-TABLE-COMPLETE-01 locks this set to the 9 codes
// of §3.4.2.1. MQTT-REASON-TABLE-POSITIONAL-01 asserts no row uses named-field
// syntax.
var pubackReasonTable = []ackReason{
	{0x00, "Success", "", errcode.KindInternal}, // success; caller guards reason != 0x00
	{0x10, "NoMatchingSubscribers", ErrAdapterMQTTPublishNoSubscribers, errcode.KindUnavailable},
	{0x80, "UnspecifiedError", ErrAdapterMQTTPublishRejected, errcode.KindInternal},
	{0x83, "ImplementationSpecificError", ErrAdapterMQTTPublishRejected, errcode.KindInternal},
	{0x87, "NotAuthorized", ErrAdapterMQTTPublishNotAuthorized, errcode.KindUnavailable},
	{0x90, "TopicNameInvalid", ErrAdapterMQTTPublishRejected, errcode.KindInvalid},
	{0x91, "PacketIdentifierInUse", ErrAdapterMQTTPublishRejected, errcode.KindInternal}, // was default; now explicit
	{0x97, "QuotaExceeded", ErrAdapterMQTTPublishRateLimited, errcode.KindUnavailable},
	{0x99, "PayloadFormatInvalid", ErrAdapterMQTTPublishPayloadFormatInvalid, errcode.KindInvalid},
}

// subackReasonTable is the SUBACK single-source table (MQTT v5 §3.9.3).
// 12 rows, full spec coverage. 0x83 and 0x91 are now explicit (were default).
//
// archtest MQTT-SUBACK-REASON-TABLE-COMPLETE-01 locks this set to the 12 codes
// of §3.9.3. MQTT-REASON-TABLE-POSITIONAL-01 asserts no row uses named-field
// syntax.
var subackReasonTable = []ackReason{
	{0x00, "GrantedQoS0", "", errcode.KindInternal}, // granted-QoS success; caller guards reason >= 0x80
	{0x01, "GrantedQoS1", "", errcode.KindInternal}, // success
	{0x02, "GrantedQoS2", "", errcode.KindInternal}, // success
	{0x80, "UnspecifiedError", ErrAdapterMQTTSubscribe, errcode.KindInternal},
	{0x83, "ImplementationSpecificError", ErrAdapterMQTTSubscribe, errcode.KindInternal}, // was default; now explicit
	{0x87, "NotAuthorized", ErrAdapterMQTTSubscribeNotAuthorized, errcode.KindInternal},
	{0x8F, "TopicFilterInvalid", ErrAdapterMQTTSubscribe, errcode.KindInternal},
	{0x91, "PacketIdentifierInUse", ErrAdapterMQTTSubscribe, errcode.KindInternal}, // was default; now explicit
	{0x97, "QuotaExceeded", ErrAdapterMQTTSubscribeRateLimited, errcode.KindUnavailable},
	{0x9E, "SharedSubscriptionsNotSupported", ErrAdapterMQTTSharedSubsUnsupported, errcode.KindInternal},
	{0xA1, "SubscriptionIdentifiersNotSupported", ErrAdapterMQTTSubscriptionIDsUnsupported, errcode.KindInternal},
	{0xA2, "WildcardSubscriptionsNotSupported", ErrAdapterMQTTSubscribe, errcode.KindInternal},
}

// ─── Derived index maps ───────────────────────────────────────────────────────

// indexReasonsByCode builds a map[byte]R from a slice of rows, using code(r) to
// extract the key. It is the single generic helper for all three tables,
// eliminating repeated build logic (go-standards: abstract when logic repeats 3×).
// It does NOT silently overwrite duplicates — callers must validate first.
func indexReasonsByCode[R any](rows []R, code func(R) byte) map[byte]R {
	m := make(map[byte]R, len(rows))
	for _, r := range rows {
		m[code(r)] = r
	}
	return m
}

// Package-level indexes derived from the single-source tables.
var (
	connackIndex = indexReasonsByCode(connackReasonTable, func(r connackReason) byte { return r.code })
	pubackIndex  = indexReasonsByCode(pubackReasonTable, func(r ackReason) byte { return r.code })
	subackIndex  = indexReasonsByCode(subackReasonTable, func(r ackReason) byte { return r.code })
)

// ─── Init validation ──────────────────────────────────────────────────────────

// validateConnackReasonTable panics (via errcode.Assertion) if rows contains
// duplicate codes or any row with class == classInvalid (the zero value). It is
// the exported validation entry point so tests can call it with a bad slice.
//
// ref: error-handling.md §Panic — A-class programmer error uses errcode.Assertion.
func validateConnackReasonTable(rows []connackReason) {
	seen := make(map[byte]bool, len(rows))
	for _, r := range rows {
		if seen[r.code] {
			panic(errcode.Assertion(
				"mqtt-reason-table-duplicate-connack-code: 0x%02x", r.code))
		}
		seen[r.code] = true
		if r.class == classInvalid {
			panic(errcode.Assertion(
				"mqtt-reason-table-class-invalid-on-connack-code: 0x%02x", r.code))
		}
	}
}

// validateAckReasonTable panics (via errcode.Assertion) if rows contains
// duplicate codes. PUBACK/SUBACK rows have no class field to validate against
// a zero sentinel.
func validateAckReasonTable(label string, rows []ackReason) {
	seen := make(map[byte]bool, len(rows))
	for _, r := range rows {
		if seen[r.code] {
			panic(errcode.Assertion(
				"mqtt-reason-table-duplicate-%s-code: 0x%02x", label, r.code))
		}
		seen[r.code] = true
	}
}

func init() {
	validateConnackReasonTable(connackReasonTable)
	validateAckReasonTable("PUBACK", pubackReasonTable)
	validateAckReasonTable("SUBACK", subackReasonTable)
}

// ─── Name accessors (derived from tables) ─────────────────────────────────────

// connackReasonName returns the spec name for an MQTT v5 CONNACK reason
// code. Unknown codes return "Unknown" so operators can still match the
// numeric reasonCode field.
func connackReasonName(code byte) string {
	if r, ok := connackIndex[code]; ok {
		return r.name
	}
	return "Unknown"
}

// pubackReasonName returns the spec name for an MQTT v5 PUBACK reason code.
// Unknown codes return "Unknown" so operators can still match the numeric
// reasonCode field in structured logs.
func pubackReasonName(code byte) string {
	if r, ok := pubackIndex[code]; ok {
		return r.name
	}
	return "Unknown"
}

// subackReasonName returns the spec name for an MQTT v5 SUBACK reason code.
// Unknown codes return "Unknown" so operators can still match the numeric
// reasonCode field in structured logs.
func subackReasonName(code byte) string {
	if r, ok := subackIndex[code]; ok {
		return r.name
	}
	return "Unknown"
}

// ─── Classifiers (derived from tables) ───────────────────────────────────────

// errcodeForClass maps a connackClass to its errcode.Code. classTransient maps
// to ErrAdapterMQTTConnect; all other classes map to ErrAdapterMQTTConnectPermanent.
func errcodeForClass(class connackClass) errcode.Code {
	if class == classTransient {
		return ErrAdapterMQTTConnect
	}
	return ErrAdapterMQTTConnectPermanent
}

// classifyConnackReason inspects an OnConnectError and maps it to a
// connackClass and the errcode.Code to stamp on the returned error.
//
// It recovers *autopaho.ConnackError via errors.As and looks up the CONNACK
// ReasonCode in connackReasonTable (the single source of truth). TLS handshake
// errors are classified as classBootstrapFatal regardless of the ConnackError path.
//
// Full §3.2.2.2 coverage is enforced by archtest
// MQTT-CONNACK-REASON-TABLE-COMPLETE-01. The classifier is derived entirely from
// connackReasonTable — adding a code to the table automatically includes it here.
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

	if r, ok := connackIndex[connackErr.ReasonCode]; ok {
		return r.class, errcodeForClass(r.class)
	}
	// Unrecognized non-spec code: retry safely.
	return classTransient, ErrAdapterMQTTConnect
}

// classifyPubackReason maps an MQTT v5 PUBACK reason code (byte) to the
// corresponding errcode.Code and errcode.Kind.
//
// The mapping is derived from pubackReasonTable (the single source of truth).
// Full §3.4.2.1 coverage is enforced by archtest MQTT-PUBACK-REASON-TABLE-COMPLETE-01.
// On unrecognized code, returns (ErrAdapterMQTTPublishRejected, KindInternal) —
// identical to the prior default branch.
//
// ref: MQTT v5.0 spec §3.4.2.1 PUBACK Reason Code table
func classifyPubackReason(code byte) (errcode.Code, errcode.Kind) {
	if r, ok := pubackIndex[code]; ok {
		return r.errCode, r.kind
	}
	return ErrAdapterMQTTPublishRejected, errcode.KindInternal
}

// classifySubackReason maps an MQTT v5 SUBACK reason code (byte) to the
// corresponding errcode.Code and errcode.Kind.
//
// The mapping is derived from subackReasonTable (the single source of truth).
// Full §3.9.3 coverage is enforced by archtest MQTT-SUBACK-REASON-TABLE-COMPLETE-01.
// On unrecognized code, returns (ErrAdapterMQTTSubscribe, KindInternal) —
// identical to the prior default branch.
//
// ref: MQTT v5.0 spec §3.9.3 Subscribe Reason Code table
func classifySubackReason(code byte) (errcode.Code, errcode.Kind) {
	if r, ok := subackIndex[code]; ok {
		return r.errCode, r.kind
	}
	return ErrAdapterMQTTSubscribe, errcode.KindInternal
}

// disconnectReasonNames is the single source of MQTT v5 DISCONNECT reason code →
// spec-defined name mapping (MQTT v5.0 §3.14.2.1 Disconnect Reason Code table).
// This is a DISTINCT table from connackReasonNames (§3.2.2.2) and
// pubackReasonNames (§3.4.2.1): the three packets share some numeric codes with
// different meanings, so a server-initiated DISCONNECT must be decoded with this
// table, not the CONNACK one. Only the server-initiated subset is enumerated;
// unrecognized codes fall back to "Unknown".
var disconnectReasonNames = map[byte]string{
	0x00: "NormalDisconnection",
	0x04: "DisconnectWithWillMessage",
	0x80: "UnspecifiedError",
	0x81: "MalformedPacket",
	0x82: "ProtocolError",
	0x83: "ImplementationSpecificError",
	0x87: "NotAuthorized",
	0x89: "ServerBusy",
	0x8B: "ServerShuttingDown",
	0x8D: "KeepAliveTimeout",
	0x8E: "SessionTakenOver",
	0x8F: "TopicFilterInvalid",
	0x90: "TopicNameInvalid",
	0x93: "ReceiveMaximumExceeded",
	0x94: "TopicAliasInvalid",
	0x95: "PacketTooLarge",
	0x96: "MessageRateTooHigh",
	0x97: "QuotaExceeded",
	0x98: "AdministrativeAction",
	0x99: "PayloadFormatInvalid",
	0x9C: "UseAnotherServer",
	0x9D: "ServerMoved",
	0x9F: "ConnectionRateExceeded",
	0xA0: "MaximumConnectTime",
}

// disconnectReasonName returns the spec name for an MQTT v5 DISCONNECT reason
// code. Unknown codes return "Unknown" so operators can still match the numeric
// reasonCode field in structured logs.
func disconnectReasonName(code byte) string {
	if name, ok := disconnectReasonNames[code]; ok {
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

// Package topicns owns MQTT topic-namespace validation and the sealed
// publish/subscribe token types. It lives under adapters/mqtt/internal/ so the
// token structs' unexported fields are invisible to adapters/mqtt: constructing
// a PublishableTopic / SubscribableFilter carrying an arbitrary topic is a
// compile error there, and the only way to obtain one is via the validated
// Mint / MintFilter / MintDeadLetter constructors (MQTT-PUBLISH/SUBSCRIBE-CALLSITE-FUNNEL-01,
// upstream Hard). adapters/mqtt re-exports Namespace as TopicNamespace (a type
// alias) and the four ERR_ADAPTER_MQTT_ codes (const re-export), so its public
// API and all consumers are unchanged.
//
// ref: pkg/errcode/details.go sealed-value pattern; adapters/otel/span.go holder seal
package topicns

import (
	"strings"

	"github.com/ghbvf/gocell/framework/pkg/errcode"
)

// Topic-namespace validation error codes. Defined here (co-located with the
// validation funnel) to avoid a circular import back to adapters/mqtt; that
// package re-exports them as ErrAdapterMQTT* so the rest of the adapter and its
// tests reference them through the mqtt package unchanged. The ERR_ADAPTER_MQTT_
// wire prefix is preserved verbatim.
const (
	// ErrInvalidNamespace signals a topic namespace prefix that fails validation.
	ErrInvalidNamespace errcode.Code = "ERR_ADAPTER_MQTT_INVALID_TOPIC_NAMESPACE"
	// ErrTopicOutsideNamespace signals that a publish or subscribe topic/filter
	// does not fall within the declared namespace.
	ErrTopicOutsideNamespace errcode.Code = "ERR_ADAPTER_MQTT_TOPIC_OUTSIDE_NAMESPACE"
	// ErrInvalidPublishTopic signals that a publish topic is malformed (empty or
	// contains a "+"/"#" wildcard).
	ErrInvalidPublishTopic errcode.Code = "ERR_ADAPTER_MQTT_INVALID_PUBLISH_TOPIC"
	// ErrInvalidSubscribeFilter signals that a subscribe filter has invalid
	// wildcard placement or an empty/invalid consumer group.
	ErrInvalidSubscribeFilter errcode.Code = "ERR_ADAPTER_MQTT_INVALID_SUBSCRIBE_FILTER"
)

const (
	maxNamespaceLen = 128

	// The "128" literal below must stay in sync with maxNamespaceLen above. errcode
	// messages are required to be const literals (MESSAGE-CONST-LITERAL-01), so the
	// constant cannot be interpolated into the string.
	msgInvalidNamespaceLength   = "mqtt topic namespace: must be non-empty and at most 128 chars"
	msgInvalidNamespaceSlash    = "mqtt topic namespace: must not have a leading or trailing slash"
	msgInvalidNamespaceWildcard = "mqtt topic namespace: must not contain MQTT wildcards (+ or #)"
	msgInvalidNamespaceLevel    = "mqtt topic namespace: each level must match ^[a-z0-9_-]+$"
	msgTopicOutsideNamespace    = "mqtt topic is outside the declared namespace"
	msgSubscribeOutsideNS       = "mqtt subscribe filter head is outside the declared namespace"
	msgInvalidWildcardPlacement = "mqtt subscribe filter has invalid wildcard placement (# must be at end, each level must be a lone token)"
	msgPublishTopicWildcard     = "mqtt publish topic must not contain + or # wildcards"
	msgEmptyTopic               = "mqtt publish topic must not be empty (MQTT v5 §4.7.3)"
	msgEmptyFilter              = "mqtt subscribe filter must not be empty"
	msgEmptyConsumerGroup       = "mqtt subscribe consumer group must not be empty (shared subscription requires a group)"
	msgInvalidConsumerGroup     = "mqtt subscribe consumer group must match ^[a-z0-9_-]+$ " +
		"(no /, +, #, $, or whitespace — they would inject extra levels or wildcards into the $share wire filter)"
	msgZeroNamespaceReceiver = "mqtt topic namespace receiver is zero-value; construct via ParseTopicNamespace"

	// detailKeyConsumerGroup is the public-detail key for an invalid consumer
	// group surfaced in errcode.PublicDetail.
	detailKeyConsumerGroup = "consumerGroup"

	// detailKeyNamespace / detailKeyTopic / detailKeyFilter are extracted
	// per go-standards.md "同义字符串重复 ≥ 3 次抽常量". These are public-
	// detail key names exposed in errcode.PublicDetail for 4xx response
	// surfacing.
	detailKeyNamespace = "namespace"
	detailKeyTopic     = "topic"
	detailKeyFilter    = "filter"
)

// Namespace is a validated topic prefix that scopes a cell's publish /
// subscribe surface. It is a sealed struct — only Parse
// constructs it, guaranteeing the prefix is always valid.
//
// ref: errcode.PublicDetail sealed-value pattern (pkg/errcode/details.go)
type Namespace struct {
	value string
}

// Parse validates ns and returns a Namespace.
//
// Validation rules:
//   - Non-empty; length ≤ 128
//   - No leading or trailing "/"
//   - No "+" or "#" wildcards (wildcards are for subscriptions, not namespace prefixes)
//   - Each level matches ^[a-z0-9_-]+$
//
// Returns ErrInvalidNamespace (KindInvalid) on failure.
func Parse(ns string) (Namespace, error) {
	if len(ns) == 0 || len(ns) > maxNamespaceLen {
		return Namespace{}, errcode.New(
			errcode.KindInvalid, ErrInvalidNamespace,
			msgInvalidNamespaceLength,
			errcode.WithDetails(errcode.PublicString(detailKeyNamespace, ns)),
		)
	}
	if strings.HasPrefix(ns, "/") || strings.HasSuffix(ns, "/") {
		return Namespace{}, errcode.New(
			errcode.KindInvalid, ErrInvalidNamespace,
			msgInvalidNamespaceSlash,
			errcode.WithDetails(errcode.PublicString(detailKeyNamespace, ns)),
		)
	}
	if strings.ContainsAny(ns, "+#") {
		return Namespace{}, errcode.New(
			errcode.KindInvalid, ErrInvalidNamespace,
			msgInvalidNamespaceWildcard,
			errcode.WithDetails(errcode.PublicString(detailKeyNamespace, ns)),
		)
	}
	levels := strings.Split(ns, "/")
	for _, lvl := range levels {
		if !isValidNSLevel(lvl) {
			return Namespace{}, errcode.New(
				errcode.KindInvalid, ErrInvalidNamespace,
				msgInvalidNamespaceLevel,
				errcode.WithDetails(errcode.PublicString(detailKeyNamespace, ns)),
			)
		}
	}
	return Namespace{value: ns}, nil
}

// String returns the namespace prefix string.
func (n Namespace) String() string { return n.value }

// PublishOK reports whether topic is a valid publish target under this namespace.
// MQTT v5.0 §3.3.2.1 forbids wildcards in publish topics; §4.7.3 forbids the
// empty topic name. This check rejects:
//   - zero-value receiver (`var ns Namespace; ns.PublishOK(...)` is an
//     in-package programmer error — the only construction path that yields a
//     usable namespace is Parse, which never returns the zero
//     value);
//   - empty topic;
//   - any topic containing "+" or "#";
//
// before the namespace-prefix check. A topic without wildcards is under the
// namespace if it equals the prefix exactly or starts with prefix + "/".
//
// Returns ErrInvalidNamespace for the zero-value receiver,
// ErrInvalidPublishTopic for wildcard/empty topic violations, and
// ErrTopicOutsideNamespace for boundary violations.
func (n Namespace) PublishOK(topic string) error {
	if n.value == "" {
		return errcode.New(
			errcode.KindInvalid, ErrInvalidNamespace,
			msgZeroNamespaceReceiver,
			errcode.WithInternal(errcode.InternalAttr(detailKeyTopic, topic)),
		)
	}
	if topic == "" {
		return errcode.New(
			errcode.KindInvalid, ErrInvalidPublishTopic,
			msgEmptyTopic,
			errcode.WithDetails(errcode.PublicString(detailKeyNamespace, n.value)),
		)
	}
	if strings.ContainsAny(topic, "+#") {
		return errcode.New(
			errcode.KindInvalid, ErrInvalidPublishTopic,
			msgPublishTopicWildcard,
			errcode.WithDetails(
				errcode.PublicString(detailKeyNamespace, n.value),
			),
			errcode.WithInternal(errcode.InternalAttr(detailKeyTopic, topic)),
		)
	}
	if topic == n.value {
		return nil
	}
	if strings.HasPrefix(topic, n.value+"/") {
		return nil
	}
	return errcode.New(
		errcode.KindInvalid, ErrTopicOutsideNamespace,
		msgTopicOutsideNamespace,
		errcode.WithDetails(
			errcode.PublicString(detailKeyNamespace, n.value),
		),
		errcode.WithInternal(errcode.InternalAttr(detailKeyTopic, topic)),
	)
}

// SubscribeOK reports whether filter falls under this namespace.
// The non-wildcard head of the filter must be under the namespace. MQTT
// wildcards "+" (single-level) and "#" (multi-level) are permitted only in
// valid positions:
//   - "#" must appear only as the last level token, alone in its level
//   - "+" must appear alone as its entire level token (not embedded in a word)
//
// Returns ErrTopicOutsideNamespace if the filter head is not under
// the namespace, or ErrInvalidSubscribeFilter on malformed wildcard
// placement (e.g. "#" not at last level, or wildcard embedded in a level).
func (n Namespace) SubscribeOK(filter string) error {
	if n.value == "" {
		return errcode.New(
			errcode.KindInvalid, ErrInvalidNamespace,
			msgZeroNamespaceReceiver,
			errcode.WithInternal(errcode.InternalAttr(detailKeyFilter, filter)),
		)
	}
	if filter == "" {
		return errcode.New(
			errcode.KindInvalid, ErrInvalidSubscribeFilter,
			msgEmptyFilter,
			errcode.WithDetails(errcode.PublicString(detailKeyNamespace, n.value)),
		)
	}
	// Split into levels and validate wildcard placement.
	levels := strings.Split(filter, "/")
	for i, lvl := range levels {
		if lvl == "#" {
			// # is only legal at the last level.
			if i != len(levels)-1 {
				return errcode.New(
					errcode.KindInvalid, ErrInvalidSubscribeFilter,
					msgInvalidWildcardPlacement,
					errcode.WithInternal(errcode.InternalAttr(detailKeyFilter, filter)),
				)
			}
			continue
		}
		if lvl == "+" {
			continue
		}
		// A level that contains + or # but is not exactly "+" or "#" is invalid.
		if strings.ContainsAny(lvl, "+#") {
			return errcode.New(
				errcode.KindInvalid, ErrInvalidSubscribeFilter,
				msgInvalidWildcardPlacement,
				errcode.WithInternal(errcode.InternalAttr(detailKeyFilter, filter)),
			)
		}
	}

	// Check namespace prefix boundary: strip wildcard levels and verify head.
	head := filterHead(filter)
	if head == n.value {
		return nil
	}
	if strings.HasPrefix(head, n.value+"/") {
		return nil
	}
	return errcode.New(
		errcode.KindInvalid, ErrTopicOutsideNamespace,
		msgSubscribeOutsideNS,
		errcode.WithDetails(
			errcode.PublicString(detailKeyNamespace, n.value),
		),
		errcode.WithInternal(errcode.InternalAttr(detailKeyFilter, filter)),
	)
}

// filterHead returns the concrete (non-wildcard) head of a subscribe filter.
// For "ns/a/+/b" it returns "ns/a"; for "ns/#" it returns "ns".
// If the filter has no wildcards it returns the full filter.
func filterHead(filter string) string {
	levels := strings.Split(filter, "/")
	concreteEnd := 0
	for i, lvl := range levels {
		if lvl == "+" || lvl == "#" {
			break
		}
		concreteEnd = i + 1
	}
	if concreteEnd == 0 {
		return ""
	}
	return strings.Join(levels[:concreteEnd], "/")
}

// isValidNSLevel reports whether a single namespace level token matches
// ^[a-z0-9_-]+$ (non-empty, only lowercase letters, digits, underscores,
// hyphens — no uppercase, no wildcards).
func isValidNSLevel(lvl string) bool {
	if len(lvl) == 0 {
		return false
	}
	for _, r := range lvl {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' && r != '-' {
			return false
		}
	}
	return true
}

// isValidConsumerGroup reports whether a shared-subscription consumer group
// matches ^[a-z0-9_-]+$. The wire filter is "$share/{group}/{filter}", so a
// group containing "/", "+", "#", "$", or whitespace would inject extra topic
// levels or wildcards into the SUBSCRIBE packet. ConsumerGroup is
// internally-sourced (cell metadata), so this is defense-in-depth; it shares the
// same alphabet as namespace levels (lowercase alnum + "_" + "-"), matching how
// cell / consumer-group IDs look (e.g. "ready-cg-<uuid>").
func isValidConsumerGroup(group string) bool {
	return isValidNSLevel(group)
}

// PublishableTopic carries a topic string that has been validated against a
// Namespace via PublishOK. Package-unexported and constructed exclusively
// by Namespace.Mint — the only way to obtain a PublishableTopic is to
// call Mint, which internally calls PublishOK. This encodes "PublishOK
// precedence" as a type-system invariant: any callsite that accepts a
// PublishableTopic is guaranteed (at compile time) that the topic has passed
// namespace validation.
//
// The Connection.Publish method (mqtt internal funnel) accepts only
// PublishableTopic, so no other package can drive a publish without minting
// a topic through PublishOK. The single sanctioned holder + sealed
// construction Hard funnel pattern. See archtest MQTT-PUBLISH-CALLSITE-FUNNEL-01.
type PublishableTopic struct {
	topic string
}

// Mint validates topic against this namespace via PublishOK and returns a
// PublishableTopic on success. Caller passes the result to Connection.Publish
// (or to any future publish helper) — the type system guarantees PublishOK
// has been called.
func (n Namespace) Mint(topic string) (PublishableTopic, error) {
	if err := n.PublishOK(topic); err != nil {
		return PublishableTopic{}, err
	}
	return PublishableTopic{topic: topic}, nil
}

// deadLetterPrefix is the reserved MQTT topic prefix for app-level dead-letter
// routing. MQTT has no broker-native dead-letter exchange (unlike AMQP's DLX),
// so a permanently-rejected (DispositionReject) or undecodable (poison) message
// is republished to "$dead/<originalTopic>" for ops audit before being PUBACK'd
// to stop redelivery.
//
// The "$"-prefix is deliberate and load-bearing: MQTT v5 §4.7.2 excludes
// "$"-leading topics from wildcard subscriptions ("#" / "+" at the root), so a
// cell's normal "<ns>/#" subscription can never re-consume a $dead message —
// the dead-letter sink cannot loop back into live delivery. The prefix is
// intentionally OUTSIDE the Namespace boundary: it is an ops sink, not a
// cell topic.
const deadLetterPrefix = "$dead/"

// MintDeadLetter validates originalTopic against this namespace via PublishOK and
// returns a PublishableTopic for the dead-letter sink "$dead/<originalTopic>".
//
// It validates the ORIGINAL topic, not the "$dead/"-prefixed result, because:
//   - the prefixed result lives outside the namespace and begins with "$", which
//     PublishOK would (correctly) reject — so the prefixed form is unvalidatable;
//   - on the poison path originalTopic is the untrusted broker-delivered topic,
//     so re-validating it fail-closed rejects wildcards / empty / out-of-namespace
//     before a publish target is constructed.
//
// MintDeadLetter is a SECOND sanctioned constructor of the sealed PublishableTopic
// (alongside Mint). Splitting it into its own typed function (rather than
// overloading Mint with a "$dead/" special case) keeps the two semantics distinct
// — "publish a cell topic" vs "route a poison message to the ops sink" — so
// choosing the wrong one is choosing the wrong API name. The construction-allowlist
// archtest MQTT-PUBLISH-CALLSITE-FUNNEL-01/A2 enumerates both constructors.
func (n Namespace) MintDeadLetter(originalTopic string) (PublishableTopic, error) {
	if err := n.PublishOK(originalTopic); err != nil {
		return PublishableTopic{}, err
	}
	return PublishableTopic{topic: deadLetterPrefix + originalTopic}, nil
}

// String returns the validated topic string. Provided for slog logging and
// for the internal Publish path to read the topic when constructing the
// paho.Publish packet.
func (t PublishableTopic) String() string { return t.topic }

// SubscribableFilter carries a filter validated against a Namespace via
// SubscribeOK plus its $share wire form. Package-unexported and constructed
// exclusively by Namespace.MintFilter — the only way to obtain a
// SubscribableFilter is to call MintFilter, which internally calls SubscribeOK.
// This mirrors PublishableTopic: any callsite that accepts a SubscribableFilter
// is guaranteed (at compile time) that the filter has passed namespace
// validation and carries a well-formed shared-subscription wire form.
//
// The Connection.Subscribe method (mqtt internal funnel) accepts only
// SubscribableFilter, so no other package can drive a subscribe without minting
// a filter through SubscribeOK. The single sanctioned holder + sealed
// construction Hard funnel pattern, same as PublishableTopic.
//
// Only the SUBSCRIBE wire form is carried: the broker strips "$share/{group}/"
// before delivery (MQTT v5 §4.8.2) and tags each PUBLISH with the route's MQTT v5
// Subscription Identifier, so onPublishReceived routes by sub-id rather than by
// re-matching the delivered topic against a bare filter.
type SubscribableFilter struct {
	wireFilter string // SUBSCRIBE-packet shape: "$share/{group}/{filter}"
}

// sharedSubPrefix is the MQTT v5 §4.8.2 shared-subscription wire prefix. It is
// lowercase per spec; mosquitto requires lowercase. Extracted as a const since
// it composes the wireFilter and is referenced from tests.
const sharedSubPrefix = "$share/"

// MintFilter validates filter against this namespace via SubscribeOK and returns
// a SubscribableFilter whose wireFilter is the MQTT v5 shared-subscription form
// "$share/{consumerGroup}/{filter}". consumerGroup must be non-empty.
//
// Returns ErrInvalidSubscribeFilter for an empty consumerGroup, or
// the SubscribeOK error (ErrInvalidSubscribeFilter /
// ErrTopicOutsideNamespace / ErrInvalidNamespace) on
// filter / namespace violations.
//
// ref: Mint / PublishableTopic (same package)
func (n Namespace) MintFilter(consumerGroup, filter string) (SubscribableFilter, error) {
	if consumerGroup == "" {
		// filter is a topic pattern (may embed identifiers) → Internal channel
		// only, consistent with SubscribeOK; the message alone is sufficient for
		// the 4xx client.
		return SubscribableFilter{}, errcode.New(
			errcode.KindInvalid, ErrInvalidSubscribeFilter,
			msgEmptyConsumerGroup,
			errcode.WithInternal(errcode.InternalAttr(detailKeyFilter, filter)),
		)
	}
	if !isValidConsumerGroup(consumerGroup) {
		return SubscribableFilter{}, errcode.New(
			errcode.KindInvalid, ErrInvalidSubscribeFilter,
			msgInvalidConsumerGroup,
			errcode.WithDetails(errcode.PublicString(detailKeyConsumerGroup, consumerGroup)),
		)
	}
	if err := n.SubscribeOK(filter); err != nil {
		return SubscribableFilter{}, err
	}
	return SubscribableFilter{
		wireFilter: sharedSubPrefix + consumerGroup + "/" + filter,
	}, nil
}

// String returns the SUBSCRIBE-packet wire form ("$share/{group}/{filter}").
// Provided for slog logging; the struct fields stay unexported.
func (f SubscribableFilter) String() string { return f.wireFilter }

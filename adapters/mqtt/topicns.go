package mqtt

import (
	"strings"

	"github.com/ghbvf/gocell/pkg/errcode"
)

const (
	maxTopicNamespaceLen = 128

	msgInvalidTopicNamespace = "mqtt topic namespace: must be non-empty, no leading/trailing slash, " +
		"no wildcards, each level ^[a-z0-9_-]+$, max 128 chars"
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

// TopicNamespace is a validated topic prefix that scopes a cell's publish /
// subscribe surface. It is a sealed struct — only ParseTopicNamespace
// constructs it, guaranteeing the prefix is always valid.
//
// ref: errcode.PublicDetail sealed-value pattern (pkg/errcode/details.go)
type TopicNamespace struct {
	value string
}

// ParseTopicNamespace validates ns and returns a TopicNamespace.
//
// Validation rules:
//   - Non-empty; length ≤ 128
//   - No leading or trailing "/"
//   - No "+" or "#" wildcards (wildcards are for subscriptions, not namespace prefixes)
//   - Each level matches ^[a-z0-9_-]+$
//
// Returns ErrAdapterMQTTInvalidTopicNamespace (KindInvalid) on failure.
func ParseTopicNamespace(ns string) (TopicNamespace, error) {
	if len(ns) == 0 || len(ns) > maxTopicNamespaceLen {
		return TopicNamespace{}, errcode.New(
			errcode.KindInvalid, ErrAdapterMQTTInvalidTopicNamespace,
			msgInvalidTopicNamespace,
			errcode.WithDetails(errcode.PublicString(detailKeyNamespace, ns)),
		)
	}
	if strings.HasPrefix(ns, "/") || strings.HasSuffix(ns, "/") {
		return TopicNamespace{}, errcode.New(
			errcode.KindInvalid, ErrAdapterMQTTInvalidTopicNamespace,
			msgInvalidTopicNamespace,
			errcode.WithDetails(errcode.PublicString(detailKeyNamespace, ns)),
		)
	}
	if strings.ContainsAny(ns, "+#") {
		return TopicNamespace{}, errcode.New(
			errcode.KindInvalid, ErrAdapterMQTTInvalidTopicNamespace,
			msgInvalidTopicNamespace,
			errcode.WithDetails(errcode.PublicString(detailKeyNamespace, ns)),
		)
	}
	levels := strings.Split(ns, "/")
	for _, lvl := range levels {
		if !isValidNSLevel(lvl) {
			return TopicNamespace{}, errcode.New(
				errcode.KindInvalid, ErrAdapterMQTTInvalidTopicNamespace,
				msgInvalidTopicNamespace,
				errcode.WithDetails(errcode.PublicString(detailKeyNamespace, ns)),
			)
		}
	}
	return TopicNamespace{value: ns}, nil
}

// String returns the namespace prefix string.
func (n TopicNamespace) String() string { return n.value }

// PublishOK reports whether topic is a valid publish target under this namespace.
// MQTT v5.0 §3.3.2.1 forbids wildcards in publish topics; §4.7.3 forbids the
// empty topic name. This check rejects:
//   - zero-value receiver (`var ns TopicNamespace; ns.PublishOK(...)` is an
//     in-package programmer error — the only construction path that yields a
//     usable namespace is ParseTopicNamespace, which never returns the zero
//     value);
//   - empty topic;
//   - any topic containing "+" or "#";
//
// before the namespace-prefix check. A topic without wildcards is under the
// namespace if it equals the prefix exactly or starts with prefix + "/".
//
// Returns ErrAdapterMQTTInvalidTopicNamespace for the zero-value receiver,
// ErrAdapterMQTTInvalidPublishTopic for wildcard/empty topic violations, and
// ErrAdapterMQTTTopicOutsideNamespace for boundary violations.
func (n TopicNamespace) PublishOK(topic string) error {
	if n.value == "" {
		return errcode.New(
			errcode.KindInvalid, ErrAdapterMQTTInvalidTopicNamespace,
			msgZeroNamespaceReceiver,
			errcode.WithInternal(errcode.InternalAttr(detailKeyTopic, topic)),
		)
	}
	if topic == "" {
		return errcode.New(
			errcode.KindInvalid, ErrAdapterMQTTInvalidPublishTopic,
			msgEmptyTopic,
			errcode.WithDetails(errcode.PublicString(detailKeyNamespace, n.value)),
		)
	}
	if strings.ContainsAny(topic, "+#") {
		return errcode.New(
			errcode.KindInvalid, ErrAdapterMQTTInvalidPublishTopic,
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
		errcode.KindInvalid, ErrAdapterMQTTTopicOutsideNamespace,
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
// Returns ErrAdapterMQTTTopicOutsideNamespace if the filter head is not under
// the namespace, or ErrAdapterMQTTInvalidSubscribeFilter on malformed wildcard
// placement (e.g. "#" not at last level, or wildcard embedded in a level).
func (n TopicNamespace) SubscribeOK(filter string) error {
	if n.value == "" {
		return errcode.New(
			errcode.KindInvalid, ErrAdapterMQTTInvalidTopicNamespace,
			msgZeroNamespaceReceiver,
			errcode.WithInternal(errcode.InternalAttr(detailKeyFilter, filter)),
		)
	}
	if filter == "" {
		return errcode.New(
			errcode.KindInvalid, ErrAdapterMQTTInvalidSubscribeFilter,
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
					errcode.KindInvalid, ErrAdapterMQTTInvalidSubscribeFilter,
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
				errcode.KindInvalid, ErrAdapterMQTTInvalidSubscribeFilter,
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
		errcode.KindInvalid, ErrAdapterMQTTTopicOutsideNamespace,
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

// publishableTopic carries a topic string that has been validated against a
// TopicNamespace via PublishOK. Package-unexported and constructed exclusively
// by TopicNamespace.Mint — the only way to obtain a publishableTopic is to
// call Mint, which internally calls PublishOK. This encodes "PublishOK
// precedence" as a type-system invariant: any callsite that accepts a
// publishableTopic is guaranteed (at compile time) that the topic has passed
// namespace validation.
//
// The Connection.Publish method (mqtt internal funnel) accepts only
// publishableTopic, so no other package can drive a publish without minting
// a topic through PublishOK. The single sanctioned holder + sealed
// construction Hard funnel pattern. See archtest MQTT-PUBLISH-CALLSITE-FUNNEL-01.
type publishableTopic struct {
	topic string
}

// Mint validates topic against this namespace via PublishOK and returns a
// publishableTopic on success. Caller passes the result to Connection.Publish
// (or to any future publish helper) — the type system guarantees PublishOK
// has been called.
func (n TopicNamespace) Mint(topic string) (publishableTopic, error) {
	if err := n.PublishOK(topic); err != nil {
		return publishableTopic{}, err
	}
	return publishableTopic{topic: topic}, nil
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
// intentionally OUTSIDE the TopicNamespace boundary: it is an ops sink, not a
// cell topic.
const deadLetterPrefix = "$dead/"

// MintDeadLetter validates originalTopic against this namespace via PublishOK and
// returns a publishableTopic for the dead-letter sink "$dead/<originalTopic>".
//
// It validates the ORIGINAL topic, not the "$dead/"-prefixed result, because:
//   - the prefixed result lives outside the namespace and begins with "$", which
//     PublishOK would (correctly) reject — so the prefixed form is unvalidatable;
//   - on the poison path originalTopic is the untrusted broker-delivered topic,
//     so re-validating it fail-closed rejects wildcards / empty / out-of-namespace
//     before a publish target is constructed.
//
// MintDeadLetter is a SECOND sanctioned constructor of the sealed publishableTopic
// (alongside Mint). Splitting it into its own typed function (rather than
// overloading Mint with a "$dead/" special case) keeps the two semantics distinct
// — "publish a cell topic" vs "route a poison message to the ops sink" — so
// choosing the wrong one is choosing the wrong API name. The construction-allowlist
// archtest MQTT-PUBLISH-CALLSITE-FUNNEL-01/A2 enumerates both constructors.
func (n TopicNamespace) MintDeadLetter(originalTopic string) (publishableTopic, error) {
	if err := n.PublishOK(originalTopic); err != nil {
		return publishableTopic{}, err
	}
	return publishableTopic{topic: deadLetterPrefix + originalTopic}, nil
}

// String returns the validated topic string. Provided for slog logging and
// for the internal Publish path to read the topic when constructing the
// paho.Publish packet.
func (t publishableTopic) String() string { return t.topic }

// subscribableFilter carries a filter validated against a TopicNamespace via
// SubscribeOK plus its $share wire form. Package-unexported and constructed
// exclusively by TopicNamespace.MintFilter — the only way to obtain a
// subscribableFilter is to call MintFilter, which internally calls SubscribeOK.
// This mirrors publishableTopic: any callsite that accepts a subscribableFilter
// is guaranteed (at compile time) that the filter has passed namespace
// validation and carries a well-formed shared-subscription wire form.
//
// The Connection.Subscribe method (mqtt internal funnel) accepts only
// subscribableFilter, so no other package can drive a subscribe without minting
// a filter through SubscribeOK. The single sanctioned holder + sealed
// construction Hard funnel pattern, same as publishableTopic.
//
// Only the SUBSCRIBE wire form is carried: the broker strips "$share/{group}/"
// before delivery (MQTT v5 §4.8.2) and tags each PUBLISH with the route's MQTT v5
// Subscription Identifier, so onPublishReceived routes by sub-id rather than by
// re-matching the delivered topic against a bare filter.
type subscribableFilter struct {
	wireFilter string // SUBSCRIBE-packet shape: "$share/{group}/{filter}"
}

// sharedSubPrefix is the MQTT v5 §4.8.2 shared-subscription wire prefix. It is
// lowercase per spec; mosquitto requires lowercase. Extracted as a const since
// it composes the wireFilter and is referenced from tests.
const sharedSubPrefix = "$share/"

// MintFilter validates filter against this namespace via SubscribeOK and returns
// a subscribableFilter whose wireFilter is the MQTT v5 shared-subscription form
// "$share/{consumerGroup}/{filter}". consumerGroup must be non-empty.
//
// Returns ErrAdapterMQTTInvalidSubscribeFilter for an empty consumerGroup, or
// the SubscribeOK error (ErrAdapterMQTTInvalidSubscribeFilter /
// ErrAdapterMQTTTopicOutsideNamespace / ErrAdapterMQTTInvalidTopicNamespace) on
// filter / namespace violations.
//
// ref: adapters/mqtt/topicns.go Mint/publishableTopic
func (n TopicNamespace) MintFilter(consumerGroup, filter string) (subscribableFilter, error) {
	if consumerGroup == "" {
		// filter is a topic pattern (may embed identifiers) → Internal channel
		// only, consistent with SubscribeOK; the message alone is sufficient for
		// the 4xx client.
		return subscribableFilter{}, errcode.New(
			errcode.KindInvalid, ErrAdapterMQTTInvalidSubscribeFilter,
			msgEmptyConsumerGroup,
			errcode.WithInternal(errcode.InternalAttr(detailKeyFilter, filter)),
		)
	}
	if !isValidConsumerGroup(consumerGroup) {
		return subscribableFilter{}, errcode.New(
			errcode.KindInvalid, ErrAdapterMQTTInvalidSubscribeFilter,
			msgInvalidConsumerGroup,
			errcode.WithDetails(errcode.PublicString(detailKeyConsumerGroup, consumerGroup)),
		)
	}
	if err := n.SubscribeOK(filter); err != nil {
		return subscribableFilter{}, err
	}
	return subscribableFilter{
		wireFilter: sharedSubPrefix + consumerGroup + "/" + filter,
	}, nil
}

// String returns the SUBSCRIBE-packet wire form ("$share/{group}/{filter}").
// Provided for slog logging; the struct fields stay unexported.
func (f subscribableFilter) String() string { return f.wireFilter }

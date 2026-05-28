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
	msgZeroNamespaceReceiver    = "mqtt topic namespace receiver is zero-value; construct via ParseTopicNamespace"

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
// ErrAdapterMQTTInvalidSubscribeFilter for wildcard/empty topic violations,
// and ErrAdapterMQTTTopicOutsideNamespace for boundary violations.
func (n TopicNamespace) PublishOK(topic string) error {
	if n.value == "" {
		return errcode.New(
			errcode.KindInvalid, ErrAdapterMQTTInvalidTopicNamespace,
			msgZeroNamespaceReceiver,
			errcode.WithDetails(errcode.PublicString(detailKeyTopic, topic)),
		)
	}
	if topic == "" {
		return errcode.New(
			errcode.KindInvalid, ErrAdapterMQTTInvalidSubscribeFilter,
			msgEmptyTopic,
			errcode.WithDetails(errcode.PublicString(detailKeyNamespace, n.value)),
		)
	}
	if strings.ContainsAny(topic, "+#") {
		return errcode.New(
			errcode.KindInvalid, ErrAdapterMQTTInvalidSubscribeFilter,
			msgPublishTopicWildcard,
			errcode.WithDetails(
				errcode.PublicString(detailKeyNamespace, n.value),
				errcode.PublicString(detailKeyTopic, topic),
			),
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
			errcode.PublicString(detailKeyTopic, topic),
		),
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
			errcode.WithDetails(errcode.PublicString(detailKeyFilter, filter)),
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
					errcode.WithDetails(errcode.PublicString(detailKeyFilter, filter)),
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
				errcode.WithDetails(errcode.PublicString(detailKeyFilter, filter)),
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
			errcode.PublicString(detailKeyFilter, filter),
		),
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

// String returns the validated topic string. Provided for slog logging and
// for the internal Publish path to read the topic when constructing the
// paho.Publish packet.
func (t publishableTopic) String() string { return t.topic }

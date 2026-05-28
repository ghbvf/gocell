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
			errcode.WithDetails(errcode.PublicString("namespace", ns)),
		)
	}
	if strings.HasPrefix(ns, "/") || strings.HasSuffix(ns, "/") {
		return TopicNamespace{}, errcode.New(
			errcode.KindInvalid, ErrAdapterMQTTInvalidTopicNamespace,
			msgInvalidTopicNamespace,
			errcode.WithDetails(errcode.PublicString("namespace", ns)),
		)
	}
	if strings.ContainsAny(ns, "+#") {
		return TopicNamespace{}, errcode.New(
			errcode.KindInvalid, ErrAdapterMQTTInvalidTopicNamespace,
			msgInvalidTopicNamespace,
			errcode.WithDetails(errcode.PublicString("namespace", ns)),
		)
	}
	levels := strings.Split(ns, "/")
	for _, lvl := range levels {
		if !isValidNSLevel(lvl) {
			return TopicNamespace{}, errcode.New(
				errcode.KindInvalid, ErrAdapterMQTTInvalidTopicNamespace,
				msgInvalidTopicNamespace,
				errcode.WithDetails(errcode.PublicString("namespace", ns)),
			)
		}
	}
	return TopicNamespace{value: ns}, nil
}

// String returns the namespace prefix string.
func (n TopicNamespace) String() string { return n.value }

// PublishOK reports whether topic falls under this namespace.
// A topic is under the namespace if it equals the prefix exactly or starts
// with prefix + "/". Returns ErrAdapterMQTTTopicOutsideNamespace on failure.
func (n TopicNamespace) PublishOK(topic string) error {
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
			errcode.PublicString("namespace", n.value),
			errcode.PublicString("topic", topic),
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
	// Split into levels and validate wildcard placement.
	levels := strings.Split(filter, "/")
	for i, lvl := range levels {
		if lvl == "#" {
			// # is only legal at the last level.
			if i != len(levels)-1 {
				return errcode.New(
					errcode.KindInvalid, ErrAdapterMQTTInvalidSubscribeFilter,
					msgInvalidWildcardPlacement,
					errcode.WithDetails(errcode.PublicString("filter", filter)),
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
				errcode.WithDetails(errcode.PublicString("filter", filter)),
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
			errcode.PublicString("namespace", n.value),
			errcode.PublicString("filter", filter),
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

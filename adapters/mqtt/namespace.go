package mqtt

import "github.com/ghbvf/gocell/adapters/mqtt/internal/topicns"

// TopicNamespace is a validated MQTT topic prefix that scopes a cell's publish /
// subscribe surface. It is a type alias for the sealed internal type
// topicns.Namespace.
//
// The namespace, the publish/subscribe token types (PublishableTopic /
// SubscribableFilter) and their validated constructors (Mint / MintFilter /
// MintDeadLetter) live in adapters/mqtt/internal/topicns. Because the token
// structs' fields are unexported in that package, code in adapters/mqtt cannot
// construct a token carrying an arbitrary topic — doing so is a compile error,
// not just an archtest finding (sealed construction, upstream Hard; see
// MQTT-PUBLISH-CALLSITE-FUNNEL-01 / MQTT-SUBSCRIBE-CALLSITE-FUNNEL-01). The only
// way to obtain a non-zero token is the validated Mint* path. The alias keeps the
// public API and external consumers (e.g. examples/iotdevice) unchanged.
type TopicNamespace = topicns.Namespace

// ParseTopicNamespace validates ns and returns a TopicNamespace. It delegates to
// the sole namespace constructor topicns.Parse; validation rules and the returned
// ErrAdapterMQTTInvalidTopicNamespace error code are unchanged.
func ParseTopicNamespace(ns string) (TopicNamespace, error) {
	return topicns.Parse(ns)
}

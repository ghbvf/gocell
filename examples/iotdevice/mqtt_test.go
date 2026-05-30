package main

import (
	"context"
	"testing"

	"github.com/ghbvf/gocell/adapters/mqtt"
)

// TestMQTTEventTopic verifies the dotted-event-type → namespaced-slash-topic
// mapping, and that every mapped topic is accepted by the namespace's PublishOK
// (so mqtt.Publisher.Publish → ns.Mint will not reject it at runtime).
func TestMQTTEventTopic(t *testing.T) {
	ns, err := mqtt.ParseTopicNamespace("iotdevice")
	if err != nil {
		t.Fatalf("ParseTopicNamespace: %v", err)
	}
	tests := []struct {
		name      string
		eventType string
		want      string
	}{
		{"device-registered", "event.device-registered.v1", "iotdevice/event/device-registered/v1"},
		{"single segment", "ping", "iotdevice/ping"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mqttEventTopic(ns, tt.eventType)
			if got != tt.want {
				t.Fatalf("mqttEventTopic(%q) = %q, want %q", tt.eventType, got, tt.want)
			}
			if err := ns.PublishOK(got); err != nil {
				t.Fatalf("mapped topic %q rejected by PublishOK: %v", got, err)
			}
		})
	}
}

// captureFakePublisher records the last Publish call so the topic-mapping
// delegation can be asserted without a live broker.
type captureFakePublisher struct {
	gotTopic   string
	gotPayload []byte
	closed     bool
}

func (f *captureFakePublisher) Publish(_ context.Context, topic string, payload []byte) error {
	f.gotTopic = topic
	f.gotPayload = payload
	return nil
}

func (f *captureFakePublisher) Close(_ context.Context) error {
	f.closed = true
	return nil
}

// TestMQTTTopicPublisher_MapsTopicAndDelegates asserts the decorator maps the
// topic, leaves the payload untouched, and forwards Close to the inner publisher.
func TestMQTTTopicPublisher_MapsTopicAndDelegates(t *testing.T) {
	ns, err := mqtt.ParseTopicNamespace("iotdevice")
	if err != nil {
		t.Fatalf("ParseTopicNamespace: %v", err)
	}
	fake := &captureFakePublisher{}
	pub := &mqttTopicPublisher{inner: fake, ns: ns}

	payload := []byte(`{"id":"dev-1","name":"sensor"}`)
	if err := pub.Publish(context.Background(), "event.device-registered.v1", payload); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if want := "iotdevice/event/device-registered/v1"; fake.gotTopic != want {
		t.Fatalf("inner topic = %q, want %q", fake.gotTopic, want)
	}
	if string(fake.gotPayload) != string(payload) {
		t.Fatalf("payload mutated: got %q want %q", fake.gotPayload, payload)
	}
	if err := pub.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !fake.closed {
		t.Fatal("Close did not delegate to inner publisher")
	}
}

package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/ghbvf/gocell/adapters/mqtt"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
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
	if !bytes.Equal(fake.gotPayload, payload) {
		t.Fatalf("payload mutated: got %q want %q", fake.gotPayload, payload)
	}
	if err := pub.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !fake.closed {
		t.Fatal("Close did not delegate to inner publisher")
	}
}

func TestSplitBrokers(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want []string
	}{
		{"empty", "", []string{}},
		{"single", "tcp://127.0.0.1:1883", []string{"tcp://127.0.0.1:1883"}},
		{"multiple", "tcp://a:1883,tcp://b:1883", []string{"tcp://a:1883", "tcp://b:1883"}},
		{"trims whitespace", " tcp://a:1883 , tcp://b:1883 ", []string{"tcp://a:1883", "tcp://b:1883"}},
		{"drops empty entries", "tcp://a:1883,,  ,tcp://b:1883", []string{"tcp://a:1883", "tcp://b:1883"}},
		{"all empty", " , , ", []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := splitBrokers(tt.raw)
			if len(got) != len(tt.want) {
				t.Fatalf("splitBrokers(%q) = %v, want %v", tt.raw, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("splitBrokers(%q)[%d] = %q, want %q", tt.raw, i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestRedactedBrokers(t *testing.T) {
	// Third entry contains a control character → url.Parse fails → placeholder.
	in := []string{"tcp://user:secret@host:1883", "tcp://127.0.0.1:1883", "tcp://\x7fbad"}
	got := redactedBrokers(in)
	if len(got) != 3 {
		t.Fatalf("redactedBrokers len = %d, want 3", len(got))
	}
	if got[0] == in[0] {
		t.Fatalf("password not redacted: %q", got[0])
	}
	for _, g := range got {
		if bytes.Contains([]byte(g), []byte("secret")) {
			t.Fatalf("redacted broker still contains secret: %q", g)
		}
	}
	if got[2] != "<unparseable-broker-url>" {
		t.Fatalf("unparseable broker = %q, want placeholder", got[2])
	}
}

// TestBuildMQTTDirectPublisher_EarlyReturns covers the env-driven paths that
// return before mqtt.Open (so no broker is needed): disabled channel, empty
// broker list, and invalid topic namespace. The connected happy path is
// covered end-to-end by TestMQTTSmoke_DeviceRegisterPublishesToBroker.
func TestBuildMQTTDirectPublisher_EarlyReturns(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	t.Run("disabled when broker env unset", func(t *testing.T) {
		t.Setenv(envMQTTBrokers, "")
		pub, conn, ok, err := buildMQTTDirectPublisher(context.Background(), clock.Real(), logger)
		if err != nil || ok || pub != nil || conn != nil {
			t.Fatalf("disabled channel = (%v, %v, %v, %v), want (nil, nil, false, nil)", pub, conn, ok, err)
		}
	})

	t.Run("error when brokers env has no valid URLs", func(t *testing.T) {
		t.Setenv(envMQTTBrokers, " , , ")
		_, _, ok, err := buildMQTTDirectPublisher(context.Background(), clock.Real(), logger)
		if ok || err == nil {
			t.Fatalf("empty broker list = (ok=%v, err=%v), want (false, non-nil)", ok, err)
		}
	})

	t.Run("error when topic namespace invalid", func(t *testing.T) {
		t.Setenv(envMQTTBrokers, "tcp://127.0.0.1:1883")
		t.Setenv(envMQTTTopicNS, "Bad/NS/") // trailing slash + uppercase → ParseTopicNamespace rejects
		_, _, ok, err := buildMQTTDirectPublisher(context.Background(), clock.Real(), logger)
		if ok || err == nil {
			t.Fatalf("invalid namespace = (ok=%v, err=%v), want (false, non-nil)", ok, err)
		}
	})
}

// TestMQTTConnectDeadline covers the env-driven bootstrap connect-deadline
// resolver: unset → default constant; valid override; and the fail-fast paths
// (malformed duration / non-positive) that must NOT silently revert to default.
func TestMQTTConnectDeadline(t *testing.T) {
	tests := []struct {
		name    string
		set     bool
		val     string
		want    time.Duration
		wantErr bool
	}{
		{name: "unset uses default", set: false, want: defaultMQTTConnectDeadline},
		{name: "valid override", set: true, val: "10s", want: testtime.D10s},
		{name: "malformed rejected", set: true, val: "notaduration", wantErr: true},
		{name: "zero rejected", set: true, val: "0s", wantErr: true},
		{name: "negative rejected", set: true, val: "-5s", wantErr: true},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if tc.set {
				t.Setenv(envMQTTConnectDeadline, tc.val)
			} else {
				t.Setenv(envMQTTConnectDeadline, "")
			}
			got, err := mqttConnectDeadline()
			if tc.wantErr {
				if err == nil {
					t.Fatalf("mqttConnectDeadline(%q) err = nil, want non-nil", tc.val)
				}
				return
			}
			if err != nil {
				t.Fatalf("mqttConnectDeadline(%q) err = %v, want nil", tc.val, err)
			}
			if got != tc.want {
				t.Fatalf("mqttConnectDeadline(%q) = %v, want %v", tc.val, got, tc.want)
			}
		})
	}
}

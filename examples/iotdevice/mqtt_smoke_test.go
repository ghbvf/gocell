package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	mqttserver "github.com/mochi-mqtt/server/v2"
	mqttallow "github.com/mochi-mqtt/server/v2/hooks/auth"
	"github.com/mochi-mqtt/server/v2/listeners"

	"github.com/ghbvf/gocell/adapters/mqtt"
	devicemem "github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/mem"
	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/slices/deviceregister"
	registercontract "github.com/ghbvf/gocell/generated/contracts/http/device/register/v1"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/pkg/testutil/testwait"
)

// smokeNamespace is the MQTT topic namespace the demo publishes under; it must
// match the namespace wired in mqtt.go (buildMQTTDirectPublisher).
const smokeNamespace = "iotdevice"

// noopSettlement is the broker-side commit/release handle the MQTT subscriber
// hands the verification handler; the smoke only asserts payload receipt, so
// both ops are no-ops.
type noopSettlement struct{}

func (noopSettlement) Commit(context.Context) error  { return nil }
func (noopSettlement) Release(context.Context) error { return nil }

// startSmokeBroker boots an in-process mochi MQTT v2 broker on a random
// loopback port (AllowHook permits all clients) and returns its address.
// Mirrors adapters/mqtt/connection_test.go startEmbeddedBroker.
func startSmokeBroker(t *testing.T) string {
	t.Helper()
	srv := mqttserver.New(&mqttserver.Options{InlineClient: false})
	if err := srv.AddHook(new(mqttallow.AllowHook), nil); err != nil {
		t.Fatalf("add allow hook: %v", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen random port: %v", err)
	}
	addr := ln.Addr().String()
	if cerr := ln.Close(); cerr != nil {
		t.Logf("close probe listener: %v", cerr)
	}
	if err := srv.AddListener(listeners.NewTCP(listeners.Config{ID: "smoke", Address: addr})); err != nil {
		t.Fatalf("add listener: %v", err)
	}
	go func() { _ = srv.Serve() }()
	t.Cleanup(func() { _ = srv.Close() })

	testwait.External(t, "smoke-mqtt-broker-accepts", func() bool {
		c, derr := net.Dial("tcp", addr)
		if derr != nil {
			return false
		}
		_ = c.Close()
		return true
	}, testtime.D2s, testtime.D10ms)
	return addr
}

// smokeConfig returns a minimal valid mqtt.Config pointing at the loopback
// broker for the given role (publisher / subscriber → distinct client IDs).
func smokeConfig(t *testing.T, addr, role string) mqtt.Config {
	t.Helper()
	id, err := mqtt.ParseEphemeralClientID(smokeNamespace, role)
	if err != nil {
		t.Fatalf("ParseEphemeralClientID(%q): %v", role, err)
	}
	return mqtt.Config{
		ClientID:       id,
		Brokers:        []string{fmt.Sprintf("tcp://%s", addr)},
		ConnectTimeout: testtime.D5s,
		KeepAlive:      testtime.D30s,
		Backoff: mqtt.BackoffConfig{
			BaseDelay: testtime.D100ms,
			MaxDelay:  testtime.D2s,
		},
	}
}

// openSmokeConn opens an mqtt.Connection whose lifecycle is bound to t.Cleanup
// (autopaho ties the ConnectionManager to the ctx passed to Open).
func openSmokeConn(t *testing.T, clk clock.Clock, addr, role string) *mqtt.Connection {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	conn, err := mqtt.Open(ctx, clk, smokeConfig(t, addr, role))
	if err != nil {
		t.Fatalf("mqtt.Open(%q): %v", role, err)
	}
	t.Cleanup(func() {
		closeCtx, cn := context.WithTimeout(context.Background(), testtime.D5s)
		defer cn()
		_ = conn.Close(closeCtx)
	})
	return conn
}

// TestMQTTSmoke_DeviceRegisterPublishesToBroker is the end-to-end demo smoke:
// it drives the real device-register service through a DirectCellEmitter wired
// to the MQTT topic-mapping publisher, then verifies an independent MQTT
// subscriber receives the device-registered envelope with the matching payload.
//
// Link: register → DirectEmitter → mqttTopicPublisher → broker → mqtt.Subscriber.
func TestMQTTSmoke_DeviceRegisterPublishesToBroker(t *testing.T) {
	addr := startSmokeBroker(t)
	clk := clock.Real()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	ns, err := mqtt.ParseTopicNamespace(smokeNamespace)
	if err != nil {
		t.Fatalf("ParseTopicNamespace: %v", err)
	}

	// Publisher side: the cell's emitter publishes through the topic mapper.
	pubConn := openSmokeConn(t, clk, addr, "publisher")
	mqttPub, err := mqtt.NewPublisher(clk, pubConn, ns)
	if err != nil {
		t.Fatalf("NewPublisher: %v", err)
	}
	cellPub := outbox.WrapPublisherForCell(&mqttTopicPublisher{inner: mqttPub, ns: ns})
	emitter, err := outbox.NewDirectCellEmitter(
		cellPub, outbox.DirectPublishFailOpen, metrics.NopProvider{}, clk, "devicecell",
		outbox.WithLogger(logger),
	)
	if err != nil {
		t.Fatalf("NewDirectCellEmitter: %v", err)
	}
	svc, err := deviceregister.NewService(clk, devicemem.NewDeviceRepository(), logger,
		deviceregister.WithEmitter(emitter))
	if err != nil {
		t.Fatalf("deviceregister.NewService: %v", err)
	}

	// Verifier side: an independent MQTT subscriber captures the envelope.
	subConn := openSmokeConn(t, clk, addr, "subscriber")
	sub, err := mqtt.NewSubscriber(clk, subConn, ns, mqtt.SubscriberConfig{})
	if err != nil {
		t.Fatalf("NewSubscriber: %v", err)
	}
	subscription := outbox.Subscription{
		Topic:             smokeNamespace + "/event/device-registered/+",
		ConsumerGroup:     "smoke-verifier",
		CellID:            smokeNamespace,
		ContractID:        "event.device-registered.v1",
		ContractKind:      "event",
		ContractTransport: "mqtt",
	}
	received := make(chan outbox.Entry, 1)
	runCtx, runCancel := context.WithCancel(context.Background())
	t.Cleanup(runCancel)
	go func() {
		_ = sub.Subscribe(runCtx, subscription,
			func(_ context.Context, e outbox.Entry) (outbox.HandleResult, outbox.Settlement) {
				select {
				case received <- e:
				default:
				}
				return outbox.Ack(), noopSettlement{}
			})
	}()
	select {
	case <-sub.Ready(subscription):
	case <-time.After(testtime.D10s):
		t.Fatal("subscriber not ready before deadline")
	}

	// Drive the real register flow.
	const deviceName = "smoke-sensor"
	if _, err := svc.Register(context.Background(), &registercontract.Request{Name: deviceName}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// Verify the MQTT side received the envelope with the matching payload.
	select {
	case entry := <-received:
		if got := entry.EventType(); got != deviceregister.TopicDeviceRegistered {
			t.Fatalf("entry EventType = %q, want %q", got, deviceregister.TopicDeviceRegistered)
		}
		var ev struct {
			ID     string `json:"id"`
			Name   string `json:"name"`
			Status string `json:"status"`
		}
		if err := json.Unmarshal(entry.Payload(), &ev); err != nil {
			t.Fatalf("unmarshal payload %q: %v", entry.Payload(), err)
		}
		if ev.Name != deviceName {
			t.Fatalf("payload name = %q, want %q", ev.Name, deviceName)
		}
		if ev.Status != "online" {
			t.Fatalf("payload status = %q, want %q", ev.Status, "online")
		}
		if ev.ID == "" {
			t.Fatal("payload id is empty")
		}
	case <-time.After(testtime.D10s):
		t.Fatal("did not receive device-registered event on MQTT broker before deadline")
	}
}

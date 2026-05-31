package main

import (
	"testing"

	"github.com/ghbvf/gocell/adapters/mqtt"
	"github.com/ghbvf/gocell/kernel/clock"
)

// TestMQTTChannelWiring_RegistersPublisherDrainAndConnection locks the PR #1364
// review F1 regression: an enabled MQTT publish channel must wire BOTH the
// connection (managed resource: mqtt_ready probe + disconnect) AND the publisher
// (managed closer: drains in-flight publishes). Connection.Close does NOT drain,
// so dropping the publisher closer silently loses in-flight publishes on
// shutdown. The smoke tests exercise the adapter publish path but bypass this
// run.go composition wiring (review F2); this test covers the seam directly.
func TestMQTTChannelWiring_RegistersPublisherDrainAndConnection(t *testing.T) {
	addr := startSmokeBroker(t)
	clk := clock.Real()

	conn := openSmokeConn(t, clk, addr, "publisher")
	ns, err := mqtt.ParseTopicNamespace(smokeNamespace)
	if err != nil {
		t.Fatalf("ParseTopicNamespace: %v", err)
	}
	mqttPub, err := mqtt.NewPublisher(clk, conn, ns)
	if err != nil {
		t.Fatalf("NewPublisher: %v", err)
	}
	pub := &mqttTopicPublisher{inner: mqttPub, ns: ns}

	w := mqttChannelWiringFor(pub, conn)
	if w.Resource == nil {
		t.Fatal("wiring.Resource is nil — connection mqtt_ready probe + disconnect would not register")
	}
	if w.Closer == nil {
		t.Fatal("wiring.Closer is nil — in-flight publishes would not drain on shutdown (Connection.Close does not drain)")
	}
	// The closer MUST be the publisher (drains in-flight), not the connection.
	if _, ok := w.Closer.(*mqttTopicPublisher); !ok {
		t.Fatalf("wiring.Closer = %T, want *mqttTopicPublisher (the drain-capable publisher)", w.Closer)
	}
	if got := len(w.bootstrapOptions()); got != 2 {
		t.Fatalf("bootstrapOptions() = %d, want 2 (managed resource + managed closer)", got)
	}
}

package mqtt

import "github.com/ghbvf/gocell/framework/kernel/healthz"

// ProbeReady is the readiness probe name for the MQTT connection. It is the
// single declaration site for "mqtt_ready" (PROBENAME-SEALED-FUNNEL-01 typed
// funnel). The Health/Probes wiring lives in connection.go (Batch B3).
const ProbeReady healthz.ProbeName = "mqtt_ready"

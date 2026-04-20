package configcore

import (
	"testing"

	"github.com/ghbvf/gocell/runtime/eventbus"
	prom "github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
)

// TestWithOnStaleCipherMetric_WiresCallback verifies that WithOnStaleCipherMetric
// sets the staleCipherCounter field on the ConfigCore struct so that Init() can
// forward the increment callback to the ConfigRepository.
func TestWithOnStaleCipherMetric_WiresCallback(t *testing.T) {
	counter := prom.NewCounter(prom.CounterOpts{
		Name: "config_stale_cipher_total_test",
	})
	c := NewConfigCore(
		WithOnStaleCipherMetric(counter),
		WithInMemoryDefaults(),
		WithPublisher(eventbus.New()),
	)
	// The PG Init() wiring path (pgPool != nil → NewConfigRepository receives
	// WithOnStaleCipher option) is covered by config_repo_encrypt_test.go:TestWithOnStaleCipher_Option.
	assert.Equal(t, counter, c.staleCipherCounter, "WithOnStaleCipherMetric must set staleCipherCounter field")
}

// TestWithOnStaleCipherMetric_CounterIsLive verifies that the Prometheus counter
// stored by WithOnStaleCipherMetric is a live, registered counter that can be
// incremented and observed. This is a field-level wiring test; the full callback
// chain (repo.observeStaleCipher → onStaleCipher → counter.Inc) is covered by
// config_repo_encrypt_test.go:TestWithOnStaleCipher_Option.
func TestWithOnStaleCipherMetric_CounterIsLive(t *testing.T) {
	counter := prom.NewCounter(prom.CounterOpts{
		Name: "config_stale_cipher_total_live_test",
	})

	c := NewConfigCore(
		WithOnStaleCipherMetric(counter),
		WithInMemoryDefaults(),
		WithPublisher(eventbus.New()),
	)

	assert.Equal(t, float64(0), testutil.ToFloat64(counter))
	c.staleCipherCounter.Inc()
	assert.Equal(t, float64(1), testutil.ToFloat64(counter),
		"counter must be live and incrementable")
}

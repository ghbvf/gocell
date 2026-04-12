package prometheus

import (
	"sync"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/outbox"
	prom "github.com/prometheus/client_golang/prometheus"
	prom_dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRelayCollector_ImplementsInterface(t *testing.T) {
	var _ outbox.RelayCollector = (*RelayCollector)(nil)
}

func TestNewRelayCollector_MissingCellID(t *testing.T) {
	_, err := NewRelayCollector(RelayCollectorConfig{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "CellID")
}

func TestNewRelayCollector_DefaultConfig(t *testing.T) {
	c, err := NewRelayCollector(RelayCollectorConfig{CellID: "test-cell"})
	require.NoError(t, err)
	require.NotNil(t, c)
	assert.Equal(t, "test-cell", c.cellID)
	assert.NotNil(t, c.registry)
}

func TestRelayCollector_RecordPollCycle(t *testing.T) {
	registry := prom.NewRegistry()
	c, err := NewRelayCollector(RelayCollectorConfig{
		CellID:   "test-cell",
		Registry: registry,
	})
	require.NoError(t, err)

	c.RecordPollCycle(3, 1, 0, 1,
		10*time.Millisecond, 50*time.Millisecond, 5*time.Millisecond)

	families, err := registry.Gather()
	require.NoError(t, err)

	var foundRelayed, foundDuration bool
	for _, f := range families {
		switch f.GetName() {
		case "gocell_outbox_relayed_total":
			foundRelayed = true
			// Should have metrics for published(3), retried(1), skipped(1).
			// dead=0, so no metric emitted for dead.
			metricCount := len(f.GetMetric())
			assert.Equal(t, 3, metricCount,
				"should have 3 label combinations (published, retried, skipped; dead=0 is skipped)")
			// Verify published counter value.
			for _, m := range f.GetMetric() {
				labels := metricLabels(m)
				if labels["outcome"] == "published" {
					assert.Equal(t, 3.0, m.GetCounter().GetValue())
				}
				if labels["outcome"] == "retried" {
					assert.Equal(t, 1.0, m.GetCounter().GetValue())
				}
				if labels["outcome"] == "skipped" {
					assert.Equal(t, 1.0, m.GetCounter().GetValue())
				}
			}
		case "gocell_outbox_poll_duration_seconds":
			foundDuration = true
			// 4 phases: claim, publish, write_back, total.
			assert.Equal(t, 4, len(f.GetMetric()),
				"should have 4 phase label combinations")
		}
	}
	assert.True(t, foundRelayed, "should have relayed_total counter")
	assert.True(t, foundDuration, "should have poll_duration_seconds histogram")
}

func TestRelayCollector_RecordBatchSize(t *testing.T) {
	registry := prom.NewRegistry()
	c, err := NewRelayCollector(RelayCollectorConfig{
		CellID:   "test-cell",
		Registry: registry,
	})
	require.NoError(t, err)

	c.RecordBatchSize(0)
	c.RecordBatchSize(50)
	c.RecordBatchSize(100)

	families, err := registry.Gather()
	require.NoError(t, err)

	var found bool
	for _, f := range families {
		if f.GetName() == "gocell_outbox_batch_size" {
			found = true
			require.Len(t, f.GetMetric(), 1)
			h := f.GetMetric()[0].GetHistogram()
			assert.Equal(t, uint64(3), h.GetSampleCount())
		}
	}
	assert.True(t, found, "should have batch_size histogram")
}

func TestRelayCollector_RecordReclaim(t *testing.T) {
	registry := prom.NewRegistry()
	c, err := NewRelayCollector(RelayCollectorConfig{
		CellID:   "test-cell",
		Registry: registry,
	})
	require.NoError(t, err)

	c.RecordReclaim(0) // no-op
	c.RecordReclaim(5)

	families, err := registry.Gather()
	require.NoError(t, err)

	var found bool
	for _, f := range families {
		if f.GetName() == "gocell_outbox_reclaimed_total" {
			found = true
			require.Len(t, f.GetMetric(), 1)
			assert.Equal(t, 5.0, f.GetMetric()[0].GetCounter().GetValue())
		}
	}
	assert.True(t, found, "should have reclaimed_total counter")
}

func TestRelayCollector_RecordCleanup(t *testing.T) {
	registry := prom.NewRegistry()
	c, err := NewRelayCollector(RelayCollectorConfig{
		CellID:   "test-cell",
		Registry: registry,
	})
	require.NoError(t, err)

	c.RecordCleanup(100, 3)

	families, err := registry.Gather()
	require.NoError(t, err)

	var found bool
	for _, f := range families {
		if f.GetName() == "gocell_outbox_cleaned_total" {
			found = true
			assert.Equal(t, 2, len(f.GetMetric()),
				"should have 2 status label combinations (published, dead)")
		}
	}
	assert.True(t, found, "should have cleaned_total counter")
}

func TestRelayCollector_RecordCleanup_ZeroSkipped(t *testing.T) {
	registry := prom.NewRegistry()
	c, err := NewRelayCollector(RelayCollectorConfig{
		CellID:   "test-cell",
		Registry: registry,
	})
	require.NoError(t, err)

	c.RecordCleanup(0, 0) // both zero, no metrics emitted

	families, err := registry.Gather()
	require.NoError(t, err)

	for _, f := range families {
		assert.NotEqual(t, "gocell_outbox_cleaned_total", f.GetName(),
			"zero cleanup should not emit any cleaned_total metrics")
	}
}

func TestRelayCollector_ConcurrentSafety(t *testing.T) {
	c, err := NewRelayCollector(RelayCollectorConfig{CellID: "test-cell"})
	require.NoError(t, err)

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.RecordPollCycle(1, 0, 0, 0, time.Millisecond, time.Millisecond, time.Millisecond)
			c.RecordBatchSize(10)
			c.RecordReclaim(1)
			c.RecordCleanup(1, 0)
		}()
	}
	wg.Wait()

	families, err := c.registry.Gather()
	require.NoError(t, err)

	for _, f := range families {
		if f.GetName() == "gocell_outbox_reclaimed_total" {
			assert.Equal(t, 100.0, f.GetMetric()[0].GetCounter().GetValue())
		}
	}
}

func TestRelayCollector_CustomBuckets(t *testing.T) {
	registry := prom.NewRegistry()
	pollBuckets := []float64{0.001, 0.01, 0.1}
	batchBuckets := []float64{1, 10, 100}

	c, err := NewRelayCollector(RelayCollectorConfig{
		CellID:       "test-cell",
		Registry:     registry,
		PollBuckets:  pollBuckets,
		BatchBuckets: batchBuckets,
	})
	require.NoError(t, err)
	require.NotNil(t, c)
}

func TestRelayCollector_DuplicateRegister_Error(t *testing.T) {
	registry := prom.NewRegistry()
	_, err := NewRelayCollector(RelayCollectorConfig{
		CellID:   "test-cell",
		Registry: registry,
	})
	require.NoError(t, err)

	// Second registration on the same registry should fail.
	_, err = NewRelayCollector(RelayCollectorConfig{
		CellID:   "test-cell",
		Registry: registry,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "registration")
}

// metricLabels extracts label name→value from a gathered Prometheus metric.
func metricLabels(m *prom_dto.Metric) map[string]string {
	labels := make(map[string]string)
	for _, lp := range m.GetLabel() {
		labels[lp.GetName()] = lp.GetValue()
	}
	return labels
}

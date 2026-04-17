package metrics

// NopProvider is a no-op Provider used when no metrics backend is injected.
// It validates label sets (so label-drift bugs surface in test) but records
// nothing.
//
// ref: opentelemetry-go metric/noop/noop.go@main — null-object pattern.
// GoCell's Nop still enforces label validation so a misbehaving caller is
// caught in unit tests whose wire has not yet been extended with a real
// Provider, preventing silent drift into production.
type NopProvider struct{}

// CounterVec returns a no-op CounterVec that still enforces label
// correctness at With() time.
func (NopProvider) CounterVec(opts CounterOpts) (CounterVec, error) {
	return nopCounterVec{labels: append([]string(nil), opts.LabelNames...)}, nil
}

// HistogramVec returns a no-op HistogramVec that still enforces label
// correctness at With() time.
func (NopProvider) HistogramVec(opts HistogramOpts) (HistogramVec, error) {
	return nopHistogramVec{labels: append([]string(nil), opts.LabelNames...)}, nil
}

type nopCounterVec struct{ labels []string }

func (v nopCounterVec) With(l Labels) Counter {
	MustValidateLabels(v.labels, l)
	return nopCounter{}
}

type nopHistogramVec struct{ labels []string }

func (v nopHistogramVec) With(l Labels) Histogram {
	MustValidateLabels(v.labels, l)
	return nopHistogram{}
}

type nopCounter struct{}

func (nopCounter) Inc()              {}
func (nopCounter) Add(delta float64) {}

type nopHistogram struct{}

func (nopHistogram) Observe(value float64) {}

package ctxkeys

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCellIDRoundTrip(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{name: "normal id", value: "access-core"},
		{name: "empty string", value: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := WithCellID(context.Background(), tt.value)
			got, ok := CellIDFrom(ctx)
			assert.True(t, ok)
			assert.Equal(t, tt.value, got)
		})
	}
}

func TestSliceIDRoundTrip(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{name: "normal id", value: "auth-login"},
		{name: "empty string", value: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := WithSliceID(context.Background(), tt.value)
			got, ok := SliceIDFrom(ctx)
			assert.True(t, ok)
			assert.Equal(t, tt.value, got)
		})
	}
}

func TestCorrelationIDRoundTrip(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{name: "uuid", value: "550e8400-e29b-41d4-a716-446655440000"},
		{name: "empty string", value: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := WithCorrelationID(context.Background(), tt.value)
			got, ok := CorrelationIDFrom(ctx)
			assert.True(t, ok)
			assert.Equal(t, tt.value, got)
		})
	}
}

func TestJourneyIDRoundTrip(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{name: "journey id", value: "J-SSO-001"},
		{name: "empty string", value: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := WithJourneyID(context.Background(), tt.value)
			got, ok := JourneyIDFrom(ctx)
			assert.True(t, ok)
			assert.Equal(t, tt.value, got)
		})
	}
}

func TestTraceIDRoundTrip(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{name: "w3c trace", value: "4bf92f3577b34da6a3ce929d0e0e4736"},
		{name: "empty string", value: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := WithTraceID(context.Background(), tt.value)
			got, ok := TraceIDFrom(ctx)
			assert.True(t, ok)
			assert.Equal(t, tt.value, got)
		})
	}
}

func TestSpanIDRoundTrip(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{name: "span id", value: "00f067aa0ba902b7"},
		{name: "empty string", value: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := WithSpanID(context.Background(), tt.value)
			got, ok := SpanIDFrom(ctx)
			assert.True(t, ok)
			assert.Equal(t, tt.value, got)
		})
	}
}

func TestFromMissingKey(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name string
		fn   func(context.Context) (string, bool)
	}{
		{name: "CellID missing", fn: CellIDFrom},
		{name: "SliceID missing", fn: SliceIDFrom},
		{name: "CorrelationID missing", fn: CorrelationIDFrom},
		{name: "JourneyID missing", fn: JourneyIDFrom},
		{name: "TraceID missing", fn: TraceIDFrom},
		{name: "SpanID missing", fn: SpanIDFrom},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := tt.fn(ctx)
			assert.False(t, ok)
			assert.Equal(t, "", got)
		})
	}
}

func TestMultipleKeysInSameContext(t *testing.T) {
	ctx := context.Background()
	ctx = WithCellID(ctx, "access-core")
	ctx = WithSliceID(ctx, "auth-login")
	ctx = WithCorrelationID(ctx, "corr-123")
	ctx = WithJourneyID(ctx, "J-SSO-001")
	ctx = WithTraceID(ctx, "trace-abc")
	ctx = WithSpanID(ctx, "span-xyz")

	cellID, ok := CellIDFrom(ctx)
	assert.True(t, ok)
	assert.Equal(t, "access-core", cellID)

	sliceID, ok := SliceIDFrom(ctx)
	assert.True(t, ok)
	assert.Equal(t, "auth-login", sliceID)

	corrID, ok := CorrelationIDFrom(ctx)
	assert.True(t, ok)
	assert.Equal(t, "corr-123", corrID)

	journeyID, ok := JourneyIDFrom(ctx)
	assert.True(t, ok)
	assert.Equal(t, "J-SSO-001", journeyID)

	traceID, ok := TraceIDFrom(ctx)
	assert.True(t, ok)
	assert.Equal(t, "trace-abc", traceID)

	spanID, ok := SpanIDFrom(ctx)
	assert.True(t, ok)
	assert.Equal(t, "span-xyz", spanID)
}

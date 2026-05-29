package ctxkeys

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

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

func TestTraceParentRoundTrip(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{name: "w3c traceparent", value: "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"},
		{name: "empty string", value: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := WithTraceParent(context.Background(), tt.value)
			got, ok := TraceParentFrom(ctx)
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

func TestRequestIDRoundTrip(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{name: "uuid", value: "a1b2c3d4-e5f6-7890-abcd-ef1234567890"},
		{name: "empty string", value: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := WithRequestID(context.Background(), tt.value)
			got, ok := RequestIDFrom(ctx)
			assert.True(t, ok)
			assert.Equal(t, tt.value, got)
		})
	}
}

func TestRealIPRoundTrip(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{name: "ipv4", value: "192.168.1.100"},
		{name: "ipv6", value: "::1"},
		{name: "empty string", value: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := WithRealIP(context.Background(), tt.value)
			got, ok := RealIPFrom(ctx)
			assert.True(t, ok)
			assert.Equal(t, tt.value, got)
		})
	}
}

func TestPrincipalRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		with func(context.Context, string) context.Context
		from func(context.Context) (string, bool)
	}{
		{name: "ActorID", with: WithActorID, from: ActorIDFrom},
		{name: "SubjectID", with: WithSubjectID, from: SubjectIDFrom},
		{name: "TenantID", with: WithTenantID, from: TenantIDFrom},
		{name: "SessionID", with: WithSessionID, from: SessionIDFrom},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := tt.with(context.Background(), "id-"+tt.name)
			got, ok := tt.from(ctx)
			assert.True(t, ok)
			assert.Equal(t, "id-"+tt.name, got)
		})
	}
}

func TestPrincipalKeysIndependent(t *testing.T) {
	// All four principal keys coexist without collision.
	ctx := context.Background()
	ctx = WithActorID(ctx, "actor-1")
	ctx = WithSubjectID(ctx, "subject-1")
	ctx = WithTenantID(ctx, "tenant-1")
	ctx = WithSessionID(ctx, "session-1")

	a, _ := ActorIDFrom(ctx)
	s, _ := SubjectIDFrom(ctx)
	tn, _ := TenantIDFrom(ctx)
	se, _ := SessionIDFrom(ctx)
	assert.Equal(t, "actor-1", a)
	assert.Equal(t, "subject-1", s)
	assert.Equal(t, "tenant-1", tn)
	assert.Equal(t, "session-1", se)
}

func TestFromMissingKey(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name string
		fn   func(context.Context) (string, bool)
	}{
		{name: "CorrelationID missing", fn: CorrelationIDFrom},
		{name: "TraceID missing", fn: TraceIDFrom},
		{name: "TraceParent missing", fn: TraceParentFrom},
		{name: "SpanID missing", fn: SpanIDFrom},
		{name: "RequestID missing", fn: RequestIDFrom},
		{name: "RealIP missing", fn: RealIPFrom},
		{name: "ActorID missing", fn: ActorIDFrom},
		{name: "SubjectID missing", fn: SubjectIDFrom},
		{name: "TenantID missing", fn: TenantIDFrom},
		{name: "SessionID missing", fn: SessionIDFrom},
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
	ctx = WithCorrelationID(ctx, "corr-123")
	ctx = WithTraceID(ctx, "trace-abc")
	ctx = WithTraceParent(ctx, "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	ctx = WithSpanID(ctx, "span-xyz")
	ctx = WithRequestID(ctx, "req-001")
	ctx = WithRealIP(ctx, "10.0.0.1")

	corrID, ok := CorrelationIDFrom(ctx)
	assert.True(t, ok)
	assert.Equal(t, "corr-123", corrID)

	traceID, ok := TraceIDFrom(ctx)
	assert.True(t, ok)
	assert.Equal(t, "trace-abc", traceID)

	traceParent, ok := TraceParentFrom(ctx)
	assert.True(t, ok)
	assert.Equal(t, "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01", traceParent)

	spanID, ok := SpanIDFrom(ctx)
	assert.True(t, ok)
	assert.Equal(t, "span-xyz", spanID)

	reqID, ok := RequestIDFrom(ctx)
	assert.True(t, ok)
	assert.Equal(t, "req-001", reqID)

	realIP, ok := RealIPFrom(ctx)
	assert.True(t, ok)
	assert.Equal(t, "10.0.0.1", realIP)
}

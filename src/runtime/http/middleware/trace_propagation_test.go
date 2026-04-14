package middleware

import (
	"net/http"
	"testing"

	"github.com/ghbvf/gocell/pkg/ctxkeys"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExtractTraceContext(t *testing.T) {
	tests := []struct {
		name         string
		headers      map[string]string
		wantTraceID  string
		wantTraceSet bool
	}{
		{
			name: "w3c traceparent",
			headers: map[string]string{
				"traceparent": "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
			},
			wantTraceID:  "4bf92f3577b34da6a3ce929d0e0e4736",
			wantTraceSet: true,
		},
		{
			name: "b3 single header",
			headers: map[string]string{
				"b3": "4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-1",
			},
			wantTraceID:  "4bf92f3577b34da6a3ce929d0e0e4736",
			wantTraceSet: true,
		},
		{
			name: "b3 multi header",
			headers: map[string]string{
				"X-B3-TraceId": "4bf92f3577b34da6a3ce929d0e0e4736",
				"X-B3-SpanId":  "00f067aa0ba902b7",
				"X-B3-Sampled": "1",
			},
			wantTraceID:  "4bf92f3577b34da6a3ce929d0e0e4736",
			wantTraceSet: true,
		},
		{
			name: "valid traceparent wins over conflicting b3",
			headers: map[string]string{
				"traceparent":  "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
				"X-B3-TraceId": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				"X-B3-SpanId":  "bbbbbbbbbbbbbbbb",
				"X-B3-Sampled": "1",
			},
			wantTraceID:  "4bf92f3577b34da6a3ce929d0e0e4736",
			wantTraceSet: true,
		},
		{
			name: "invalid traceparent falls back to b3",
			headers: map[string]string{
				"traceparent":  "00-not-a-valid-trace-id-00f067aa0ba902b7-01",
				"X-B3-TraceId": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				"X-B3-SpanId":  "bbbbbbbbbbbbbbbb",
				"X-B3-Sampled": "1",
			},
			wantTraceID:  "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			wantTraceSet: true,
		},
		{
			name: "invalid traceparent ignored",
			headers: map[string]string{
				"traceparent": "00-not-a-valid-trace-id-00f067aa0ba902b7-01",
			},
			wantTraceSet: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			header := make(http.Header)
			for key, value := range tt.headers {
				header.Set(key, value)
			}

			ctx := extractTraceContext(t.Context(), header)

			traceID, traceOK := ctxkeys.TraceIDFrom(ctx)

			assert.Equal(t, tt.wantTraceSet, traceOK)

			// Extraction only seeds trace_id into ctxkeys; span_id is left
			// to tracer.Start so it always gets a fresh server span.
			_, spanOK := ctxkeys.SpanIDFrom(ctx)
			assert.False(t, spanOK, "extraction must not pre-seed span_id")

			if tt.wantTraceSet {
				require.True(t, traceOK)
				assert.Equal(t, tt.wantTraceID, traceID)
			}
		})
	}
}

package tracing

import (
	"net/http"
	"testing"

	"github.com/ghbvf/gocell/pkg/ctxkeys"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExtractHTTPContext(t *testing.T) {
	tests := []struct {
		name         string
		headers      map[string]string
		wantTraceID  string
		wantSpanID   string
		wantTraceSet bool
		wantSpanSet  bool
	}{
		{
			name: "w3c traceparent",
			headers: map[string]string{
				"traceparent": "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
			},
			wantTraceID:  "4bf92f3577b34da6a3ce929d0e0e4736",
			wantSpanID:   "00f067aa0ba902b7",
			wantTraceSet: true,
			wantSpanSet:  true,
		},
		{
			name: "b3 single header",
			headers: map[string]string{
				"b3": "4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-1",
			},
			wantTraceID:  "4bf92f3577b34da6a3ce929d0e0e4736",
			wantSpanID:   "00f067aa0ba902b7",
			wantTraceSet: true,
			wantSpanSet:  true,
		},
		{
			name: "b3 multi header",
			headers: map[string]string{
				"X-B3-TraceId": "4bf92f3577b34da6a3ce929d0e0e4736",
				"X-B3-SpanId":  "00f067aa0ba902b7",
				"X-B3-Sampled": "1",
			},
			wantTraceID:  "4bf92f3577b34da6a3ce929d0e0e4736",
			wantSpanID:   "00f067aa0ba902b7",
			wantTraceSet: true,
			wantSpanSet:  true,
		},
		{
			name: "invalid traceparent ignored",
			headers: map[string]string{
				"traceparent": "00-not-a-valid-trace-id-00f067aa0ba902b7-01",
			},
			wantTraceSet: false,
			wantSpanSet:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			header := make(http.Header)
			for key, value := range tt.headers {
				header.Set(key, value)
			}

			ctx := ExtractHTTPContext(t.Context(), header)

			traceID, traceOK := ctxkeys.TraceIDFrom(ctx)
			spanID, spanOK := ctxkeys.SpanIDFrom(ctx)

			assert.Equal(t, tt.wantTraceSet, traceOK)
			assert.Equal(t, tt.wantSpanSet, spanOK)

			if tt.wantTraceSet {
				require.True(t, traceOK)
				assert.Equal(t, tt.wantTraceID, traceID)
			}
			if tt.wantSpanSet {
				require.True(t, spanOK)
				assert.Equal(t, tt.wantSpanID, spanID)
			}
		})
	}
}
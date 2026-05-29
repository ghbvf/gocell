//go:build !integration

package mqtt

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ghbvf/gocell/pkg/redaction"
)

// TestSafeTopicForLog verifies that safeTopicForLog strips control characters
// (CR/LF/tab/ANSI escape) so a broker-delivered topic cannot forge log lines
// (CWE-117), masks sensitive key=value substrings, and caps overly long topics.
func TestSafeTopicForLog(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		topic string
		// assert checks the sanitized output.
		assert func(t *testing.T, out string)
	}{
		{
			name:  "plain topic passes through unchanged",
			topic: "iot/device/temp",
			assert: func(t *testing.T, out string) {
				assert.Equal(t, "iot/device/temp", out)
			},
		},
		{
			name:  "CRLF stripped (log-injection guard)",
			topic: "iot/dev\r\nFAKE level=error injected",
			assert: func(t *testing.T, out string) {
				assert.NotContains(t, out, "\r")
				assert.NotContains(t, out, "\n")
			},
		},
		{
			name:  "tab stripped",
			topic: "iot/dev\tcol",
			assert: func(t *testing.T, out string) {
				assert.NotContains(t, out, "\t")
			},
		},
		{
			name:  "ANSI escape stripped",
			topic: "iot/dev\x1b[31mred\x1b[0m",
			assert: func(t *testing.T, out string) {
				assert.NotContains(t, out, "\x1b")
			},
		},
		{
			name:  "sensitive key=value masked",
			topic: "iot/dev?token=supersecret",
			assert: func(t *testing.T, out string) {
				assert.Contains(t, out, redaction.Mask)
				assert.NotContains(t, out, "supersecret")
			},
		},
		{
			name:  "long topic truncated to cap",
			topic: strings.Repeat("a", maxTopicLogLen*2),
			assert: func(t *testing.T, out string) {
				assert.LessOrEqual(t, len([]rune(out)), maxTopicLogLen)
			},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tc.assert(t, safeTopicForLog(tc.topic))
		})
	}
}

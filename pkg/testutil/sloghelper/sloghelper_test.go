package sloghelper_test

import (
	"testing"

	"github.com/ghbvf/gocell/pkg/testutil/sloghelper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFindLogEntry(t *testing.T) {
	logOutput := `{"level":"INFO","msg":"service started","component":"auth"}
{"level":"WARN","msg":"session not found","sid":"s1","subject":"u1"}
{"level":"ERROR","msg":"session repo unavailable","sid":"s2","error":"db down"}
`

	t.Run("finds warn line by msg substring", func(t *testing.T) {
		entry := sloghelper.FindLogEntry(logOutput, "session not found")
		require.NotNil(t, entry)
		assert.Equal(t, "WARN", entry["level"])
		assert.Equal(t, "s1", entry["sid"])
	})

	t.Run("finds error line by msg substring", func(t *testing.T) {
		entry := sloghelper.FindLogEntry(logOutput, "repo unavailable")
		require.NotNil(t, entry)
		assert.Equal(t, "ERROR", entry["level"])
	})

	t.Run("returns nil when no match", func(t *testing.T) {
		entry := sloghelper.FindLogEntry(logOutput, "does not exist")
		assert.Nil(t, entry)
	})

	t.Run("skips malformed JSON lines", func(t *testing.T) {
		mixed := `not-json
{"level":"ERROR","msg":"real error"}
`
		entry := sloghelper.FindLogEntry(mixed, "real error")
		require.NotNil(t, entry)
		assert.Equal(t, "ERROR", entry["level"])
	})

	t.Run("empty log output returns nil", func(t *testing.T) {
		entry := sloghelper.FindLogEntry("", "anything")
		assert.Nil(t, entry)
	})
}

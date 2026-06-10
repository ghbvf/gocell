package testoutbox

import (
	"testing"

	"github.com/ghbvf/gocell/kernel/outbox"
)

func MustEmitter(t testing.TB, w outbox.Writer) outbox.Emitter {
	t.Helper()

	emitter, err := outbox.NewWriterEmitter(w)
	if err != nil {
		t.Fatalf("create writer emitter: %v", err)
	}
	return emitter
}

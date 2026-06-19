//go:build archtest_fixture

// Package commandasyncemitfixture is the RED fixture for
// COMMAND-ASYNC-EMIT-FUNNEL-01.
//
// It constructs command-namespace (`command.*`) outbox entries via a bare
// kout.Emit and a bare kout.NewEntry — bypassing runtime/command.EmitAsync, which
// is the sanctioned producer that stamps the idempotency identity (subject +
// commandID) the relay's Claimer wrap needs to dedup the dispatch (#1698). Both
// are violations the scanner must flag.
//
// DO NOT use this package in production code.
package commandasyncemitfixture

import (
	"context"

	"github.com/ghbvf/gocell/framework/kernel/clock"
	kout "github.com/ghbvf/gocell/framework/kernel/outbox"
)

// commandTopic is a const string in the reserved command namespace.
const commandTopic = "command.remotecommand.v1"

// BadEmit constructs a command-namespace entry via the generic kout.Emit helper
// without going through EmitAsync — the producer-side bypass.
func BadEmit(ctx context.Context, emitter kout.Emitter) error {
	return kout.Emit(ctx, clock.Real(), emitter, commandTopic, struct {
		Foo string `json:"foo"`
	}{Foo: "bar"})
}

// BadNewEntry constructs a command-namespace entry via the raw kout.NewEntry
// sealed constructor without going through EmitAsync — the second bypass form.
func BadNewEntry(ctx context.Context) (kout.Entry, error) {
	return kout.NewEntry(clock.Real(), ctx, commandTopic, []byte(`{"foo":"bar"}`))
}

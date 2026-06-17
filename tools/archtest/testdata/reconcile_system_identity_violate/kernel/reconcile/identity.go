package reconcile

import (
	"context"

	"github.com/ghbvf/gocell/framework/pkg/ctxkeys"
)

const systemProducerActor = "system"

func installSystemProducerIdentity(ctx context.Context) context.Context {
	ctx = ctxkeys.WithActorID(ctx, systemProducerActor)
	ctx = ctxkeys.WithSubjectID(ctx, systemProducerActor)
	ctx = ctxkeys.WithTenantID(ctx, "")
	ctx = ctxkeys.WithSessionID(ctx, "")
	return ctx
}

func bypassSystemIdentitySetter(ctx context.Context) context.Context {
	return ctxkeys.WithActorID(ctx, "bypass")
}

package reconcile

import "context"

type Loop struct{}

func (l *Loop) process(ctx context.Context) context.Context {
	return installSystemProducerIdentity(ctx)
}

func bypassSystemIdentityInstaller(ctx context.Context) context.Context {
	return installSystemProducerIdentity(ctx)
}

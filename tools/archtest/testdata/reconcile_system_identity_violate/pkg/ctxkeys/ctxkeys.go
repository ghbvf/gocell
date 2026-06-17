package ctxkeys

import "context"

func WithActorID(ctx context.Context, _ string) context.Context { return ctx }

func WithSubjectID(ctx context.Context, _ string) context.Context { return ctx }

func WithTenantID(ctx context.Context, _ string) context.Context { return ctx }

func WithSessionID(ctx context.Context, _ string) context.Context { return ctx }

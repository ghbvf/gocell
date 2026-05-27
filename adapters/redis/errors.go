// Package redis — error classification helpers.
//
// classifyRedisError routes a Redis command error to transient (retriable) or
// permanent classification. Transient conditions route through errcode.WrapInfra
// (KindUnavailable + CategoryInfra + private transient marker), which errcode.IsTransient
// recognizes. Permanent conditions route through errcode.Wrap(KindInternal, …).
//
// ref: redis/go-redis error.go reply codes (CLUSTERDOWN / LOADING / TRYAGAIN / MASTERDOWN)
// ref: errcode.WrapInfra funnel + archtest ADAPTER-ERROR-CLASSIFICATION-TRANSIENT-01
package redis

import (
	"context"
	"errors"
	"strings"

	goredis "github.com/redis/go-redis/v9"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// classifyRedisError routes a Redis command failure to the correct errcode shape.
//
// Transient when any of:
//   - errors.Is(err, context.DeadlineExceeded) — deadline may succeed on retry
//   - errcode.IsTransientNet(err) — any net.Error in chain (timeout, *net.OpError
//     dial refused / connection reset, *net.DNSError) per ADAPTER-NET-TRANSIENT-
//     FUNNEL-01
//   - err.Error() contains "i/o timeout" — raw network timeout string fallback
//   - CLUSTERDOWN / LOADING / TRYAGAIN / MASTERDOWN Redis reply codes
//     (server-recovering states that should requeue, not DLX)
//
// context.Canceled is NOT transient — the caller gave up.
// Permanent (WRONGTYPE, marshal errors, etc.) → errcode.Wrap KindInternal.
//
// opCode is reused as-is for both branches; no new ErrAdapter*Transient constant is introduced.
// message args are const literals to satisfy MESSAGE-CONST-LITERAL-01.
func classifyRedisError(err error, opCode errcode.Code, opMsg string) error {
	if isTransientRedisError(err) {
		return errcode.WrapInfra(opCode,
			"redis: transient error",
			err,
			errcode.WithInternal(errcode.InternalAttr("_", opMsg)))
	}
	return errcode.Wrap(errcode.KindInternal, opCode,
		"redis: operation failed",
		err,
		errcode.WithInternal(errcode.InternalAttr("_", opMsg)))
}

// isTransientRedisError reports whether err represents a transient Redis failure
// that is safe to requeue.
//
// Classification:
//  1. context.DeadlineExceeded → transient (deadline exceeded may succeed on retry).
//     context.Canceled is excluded (caller gave up; retrying is pointless).
//  2. goredis.ErrPoolTimeout → transient (connection-pool exhaustion; the
//     pool frees up — go-redis itself classifies this retryable).
//  3. errcode.IsTransientNet(err) → transient. Covers timeout class (socket
//     I/O timeout) AND non-timeout class (*net.OpError dial refused /
//     connection reset, *net.DNSError). Symmetric with adapters/s3 and
//     adapters/vault per ADR 202605161800 §"Adapter transient inventory"
//     (ADAPTER-NET-TRANSIENT-FUNNEL-01).
//  4. Error string contains "i/o timeout" → transient. SOFT best-effort
//     fallback for plain errors.New strings that do NOT implement net.Error
//     (the typed net.Error path at step 3 catches all go-redis socket
//     errors). Over/under-match here degrades to fail-closed-permanent
//     (Requeue-then-budget-DLX), never to event loss.
//  5. Redis reply-code prefixes CLUSTERDOWN / LOADING / TRYAGAIN / MASTERDOWN →
//     transient (server-recovering states; go-redis typed helpers via HasErrorPrefix
//     are preferred; plain errors.New strings match the HasPrefix fallback path).
func isTransientRedisError(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}

	// go-redis pool exhaustion: the client could not obtain a connection
	// within PoolTimeout. go-redis itself treats this as retryable (the pool
	// frees up); requeue rather than DLX.
	if errors.Is(err, goredis.ErrPoolTimeout) {
		return true
	}

	// Any net.Error in chain (timeout / *net.OpError dial refused / reset /
	// DNS) → transient. Single-source funnel: ADAPTER-NET-TRANSIENT-FUNNEL-01.
	if errcode.IsTransientNet(err) {
		return true
	}

	if strings.Contains(err.Error(), "i/o timeout") {
		return true
	}

	return isTransientRedisReplyCode(err)
}

// isTransientRedisReplyCode reports whether err is one of the Redis server-recovering
// reply codes (CLUSTERDOWN / LOADING / TRYAGAIN / MASTERDOWN).
//
// go-redis v9 exposes typed public helpers (IsClusterDownError, IsLoadingError,
// IsTryAgainError, IsMasterDownError) that work with wrapped errors. For plain
// errors.New strings (common in unit tests), the helpers delegate internally to
// HasErrorPrefix which also covers that path.
func isTransientRedisReplyCode(err error) bool {
	return goredis.IsClusterDownError(err) ||
		goredis.IsLoadingError(err) ||
		goredis.IsTryAgainError(err) ||
		goredis.IsMasterDownError(err)
}

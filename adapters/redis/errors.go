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
	"net"
	"strings"

	goredis "github.com/redis/go-redis/v9"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// classifyRedisError routes a Redis command failure to the correct errcode shape.
//
// Transient when any of:
//   - errors.Is(err, context.DeadlineExceeded) — deadline may succeed on retry
//   - errors.As net.Error && Timeout() == true — socket read/write timeout
//   - err.Error() contains "i/o timeout" — raw network timeout string
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
			errcode.WithInternal(opMsg))
	}
	return errcode.Wrap(errcode.KindInternal, opCode,
		"redis: operation failed",
		err,
		errcode.WithInternal(opMsg))
}

// isTransientRedisError reports whether err represents a transient Redis failure
// that is safe to requeue.
//
// Classification:
//  1. context.DeadlineExceeded → transient (deadline exceeded may succeed on retry).
//     context.Canceled is excluded (caller gave up; retrying is pointless).
//  2. net.Error.Timeout() == true → transient (socket I/O timeout).
//  3. Error string contains "i/o timeout" → transient. SOFT best-effort
//     fallback, intentionally AFTER the typed net.Error.Timeout() check (2)
//     which is the primary path: go-redis dial/socket timeouts implement
//     net.Error and are caught by (2). (3) only catches plain errors that
//     carry the message text without implementing net.Error. It is not the
//     authoritative classifier; over/under-match here degrades to the
//     fail-closed-permanent default (Requeue-then-budget-DLX), never to
//     event loss. Not an AI-rebust enforcement mechanism (business
//     classification, not archtest/governance) — no Soft-upgrade backlog
//     entry required; the typed check (2) is the durable signal.
//     3b. goredis.ErrPoolTimeout → transient (connection-pool exhaustion; the
//     pool frees up — go-redis itself classifies this retryable).
//  4. Redis reply-code prefixes CLUSTERDOWN / LOADING / TRYAGAIN / MASTERDOWN →
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

	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
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

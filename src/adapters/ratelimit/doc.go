// Package ratelimit provides a token-bucket rate limiter adapter that
// implements the runtime/http/middleware.RateLimiter and WindowedRateLimiter
// interfaces using golang.org/x/time/rate.
//
// Each unique key (typically client IP) gets its own token bucket, providing
// per-IP rate limiting. Stale buckets are periodically cleaned up to prevent
// memory leaks from ephemeral IPs.
//
// ref: golang.org/x/time/rate — token bucket algorithm
// Adopted: rate.NewLimiter for per-key token bucket.
// Deviated: wrapped behind middleware.RateLimiter/WindowedRateLimiter so
// runtime/ remains decoupled from x/time imports.
package ratelimit

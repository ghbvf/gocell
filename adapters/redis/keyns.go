package redis

import (
	"regexp"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// KeyNamespace scopes every Redis key produced by the four primitives in this
// adapter (Cache, IdempotencyClaimer, NonceStore, RedisDriver) under a
// constructor-injected prefix.
//
// Convention:
//
//   - Per-cell resources (e.g. a cache owned by accesscore) use the cell ID
//     as the namespace, e.g. KeyNamespace("accesscore").
//
//   - Shared infrastructure constructed at composition root (cmd/corebundle)
//     uses an explicit role label or the "_runtime" sentinel:
//
//     newRedisNonceStore -> KeyNamespace("servicetoken-nonce")
//     newRedisIdempotencyClaimer -> KeyNamespace("_runtime")
//
// Validation forbids characters that would break Redis Cluster hashtag
// boundaries ('{', '}'), the prefix/key separator (':'), and other
// ambiguous punctuation. The first character may be a lowercase letter or
// underscore (the underscore lets sentinel namespaces like "_runtime" be
// visually distinct from cell IDs, which are constrained to lowercase
// letters by the no-dash policy on cell.yaml IDs).
//
// ref: launchdarkly/go-server-sdk-redis-go-redis Builder.Prefix (constructor-injected prefix pattern)
// ref: dapr/components-contrib state appID prefix (constructor-injected tenancy)
// ref: cockroachdb/cockroach pkg/keys MakeTenantPrefix (lint-guarded tenant boundary)
type KeyNamespace string

// keyNamespaceMaxLen caps the namespace at 48 characters, leaving headroom
// inside the Redis 512 MB key limit for the per-call key body. The choice
// mirrors the K8s DNS label limit (63) minus a 15-char allowance.
const keyNamespaceMaxLen = 48

// keyNamespacePattern enforces:
//   - first char: lowercase letter or '_' (sentinel)
//   - subsequent: lowercase letters, digits, '-', '_'
//   - length 1..keyNamespaceMaxLen
var keyNamespacePattern = regexp.MustCompile(`^[a-z_][a-z0-9_-]*$`)

// Validate reports whether n is a legal namespace. Errors are descriptive
// const-literal messages so MESSAGE-CONST-LITERAL-01 stays satisfied.
func (n KeyNamespace) Validate() error {
	s := string(n)
	if s == "" {
		return errcode.New(errcode.KindInternal, errcode.ErrValidationFailed,
			"redis key namespace must not be empty")
	}
	if len(s) > keyNamespaceMaxLen {
		return errcode.New(errcode.KindInternal, errcode.ErrValidationFailed,
			"redis key namespace exceeds maximum length of 48 characters")
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == ':':
			return errcode.New(errcode.KindInternal, errcode.ErrValidationFailed,
				"redis key namespace must not contain colon (key separator)")
		case c == '{' || c == '}':
			return errcode.New(errcode.KindInternal, errcode.ErrValidationFailed,
				"redis key namespace must not contain curly brace (cluster hashtag)")
		case c >= 'A' && c <= 'Z':
			return errcode.New(errcode.KindInternal, errcode.ErrValidationFailed,
				"redis key namespace must be lowercase")
		}
	}
	if !keyNamespacePattern.MatchString(s) {
		return errcode.New(errcode.KindInternal, errcode.ErrValidationFailed,
			"redis key namespace contains forbidden characters or shape")
	}
	return nil
}

// apply returns "<ns>:<key>" — the canonical single-key derivation used by
// Cache, NonceStore, and RedisDriver. Callers must have validated n at
// construction time; apply does not re-validate.
func (n KeyNamespace) apply(key string) string {
	return string(n) + ":" + key
}

// applyHashtag returns "<ns>:{<key>}:<role>" — the multi-key Lua hashtag
// derivation used by IdempotencyClaimer. The hashtag wraps the business key
// only, so Redis Cluster CRC16 colocates lease/done on a single slot
// regardless of the namespace prefix.
func (n KeyNamespace) applyHashtag(key, role string) string {
	return string(n) + ":{" + key + "}:" + role
}

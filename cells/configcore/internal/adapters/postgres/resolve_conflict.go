package postgres

import (
	"errors"
	"fmt"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// classifyProbeFailure decides whether a probe error during CAS conflict
// resolution should be surfaced as a confirmed NotFound (caller wraps as 404)
// or as an infrastructure failure (KindInternal — must NOT masquerade as 404).
//
// Three-way semantics:
//
//   - probeErr is an *errcode.Error whose Code equals notFoundCode → returns
//     (true, nil). Caller is expected to wrap probeErr as KindNotFound with
//     the entity-specific message.
//   - probeErr is any other error (timeout, tx aborted, scan failure, plain
//     io.EOF, etc.) → returns (false, KindInternal-wrapped errcode.Error)
//     so infra failures surface to clients as 500 instead of misleading 404.
//
// Extracted as a free function (PR464 P2.1) so the three branches are unit
// testable without a PostgreSQL container.
//
// ref: docs/reviews/PR-464 round-3 P2.1 (Kratos/Watermill/etcd: infra error
// must not collapse into business not-found; needs regression coverage).
func classifyProbeFailure(probeErr error, notFoundCode errcode.Code, op, key, entityName string) (notFound bool, infraErr error) {
	var ce *errcode.Error
	if errors.As(probeErr, &ce) && ce.Code == notFoundCode {
		return true, nil
	}
	return false, errcode.Wrap(errcode.KindInternal, errcode.ErrInternal,
		"repository probe failed during CAS conflict resolution", probeErr,
		errcode.WithInternal(fmt.Sprintf("%s repo: %s probe failed key=%s", entityName, op, key)),
		errcode.WithCategory(errcode.CategoryInfra),
	)
}

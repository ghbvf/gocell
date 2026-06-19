//go:build archtest_fixture

// Package grpcerrcodemappingfixture is the GRPC-ERRCODE-MAPPING-01 reverse
// self-check corpus. It imports the real errcode.Kind and contains a deliberately
// NON-exhaustive switch over errcode.Kind (missing KindUnprocessable and KindGone)
// so the detector, pointed at this package, must report those missing constants.
// Bypassing the reverse check requires editing this real, type-checked source.
//
// Loaded via packages.Load with the archtest_fixture build tag;
// the tag keeps it out of every production scan and `go build ./...`.
//
// DO NOT use this package in production code.
package grpcerrcodemappingfixture

import (
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"google.golang.org/grpc/codes"
)

// nonExhaustiveToGRPCCode is the RED case: a toGRPCCode-style switch that
// omits KindUnprocessable and KindGone so GRPC-ERRCODE-MAPPING-01 must flag it.
func nonExhaustiveToGRPCCode(k errcode.Kind) codes.Code {
	switch k {
	case errcode.KindInternal:
		return codes.Internal
	case errcode.KindInvalid:
		return codes.InvalidArgument
	case errcode.KindUnauthenticated:
		return codes.Unauthenticated
	case errcode.KindPermissionDenied:
		return codes.PermissionDenied
	case errcode.KindNotFound:
		return codes.NotFound
	case errcode.KindConflict:
		return codes.Aborted
	// KindUnprocessable deliberately omitted — RED case.
	// KindGone deliberately omitted — RED case.
	case errcode.KindPayloadTooLarge:
		return codes.ResourceExhausted
	case errcode.KindRateLimited:
		return codes.ResourceExhausted
	case errcode.KindClientClosed:
		return codes.Canceled
	case errcode.KindDeadlineExceeded:
		return codes.DeadlineExceeded
	case errcode.KindUnavailable:
		return codes.Unavailable
	case errcode.KindNotImplemented:
		return codes.Unimplemented
	default:
		return codes.Internal
	}
}

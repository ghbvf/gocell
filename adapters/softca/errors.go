package softca

import "github.com/ghbvf/gocell/framework/pkg/errcode"

// Error construction for the soft CA. The runtime cert codes live in the
// provider-agnostic ERR_CERT_ namespace (framework/pkg/errcode, registered +
// locked by ERRCODE-PREFIX-OWNERSHIP-01) so any signer backend emits the same
// code for the same failure. Messages are const literals (MESSAGE-CONST-LITERAL-01);
// runtime detail flows through WithInternal (server-only, never on the wire) so a
// CSR / key / file-path value never leaks into an error message.
const (
	msgCAInit            = "softca: certificate authority could not be initialized"
	msgSignFailed        = "softca: certificate signing failed"
	msgCRLFailed         = "softca: certificate revocation list generation failed"
	msgRevokeNotFound    = "softca: serial not issued within scope"
	msgRevokeUnsupported = "softca: revocation reason not supported by the terminal-revocation model"
)

// errCAInit reports a CA bootstrap / load failure (key generation, PEM load).
// KindInternal → HTTP 500.
func errCAInit(reason string, cause error) error {
	return errcode.Wrap(errcode.KindInternal, errcode.ErrCertCAInit, msgCAInit, cause,
		errcode.WithInternal(errcode.InternalAttr("reason", reason)))
}

// errSignFailed reports a certificate signing-execution failure. KindInternal → 500.
func errSignFailed(reason string, cause error) error {
	return errcode.Wrap(errcode.KindInternal, errcode.ErrCertSignFailed, msgSignFailed, cause,
		errcode.WithInternal(errcode.InternalAttr("reason", reason)))
}

// errCRLFailed reports a CRL generation failure. KindInternal → 500.
func errCRLFailed(reason string, cause error) error {
	return errcode.Wrap(errcode.KindInternal, errcode.ErrCertCRLFailed, msgCRLFailed, cause,
		errcode.WithInternal(errcode.InternalAttr("reason", reason)))
}

// errRevokeNotFound reports that a serial was not issued within the given scope
// (cross-scope / unknown serial fails closed). KindNotFound → 404.
func errRevokeNotFound(reason string) error {
	return errcode.New(errcode.KindNotFound, errcode.ErrCertRevokeNotFound, msgRevokeNotFound,
		errcode.WithInternal(errcode.InternalAttr("reason", reason)))
}

// errRevokeUnsupported reports a revocation reason softca's terminal model cannot
// honor (removeFromCRL un-hold). KindInvalid → 400 (the caller passed a reason the
// provider rejects, fail-closed rather than silently inverting its meaning).
func errRevokeUnsupported(reason string) error {
	return errcode.New(errcode.KindInvalid, errcode.ErrCertRevokeUnsupported, msgRevokeUnsupported,
		errcode.WithInternal(errcode.InternalAttr("reason", reason)))
}

package initialadmin

import "errors"

// Sentinel errors for credential file operations.
//
// These sentinels intentionally use errors.New rather than the project-wide
// errcode package: they are package-internal signaling values used only within
// this package (writeCredentialFile, removeCredentialFile) and tested via
// errors.Is in callers that live in the same package. They are never returned
// through the HTTP boundary or exposed as API error codes, so the errcode
// convention ("no bare errors.New for external-facing errors") does not apply
// here.
var (
	// errCredFileExists is returned by writeCredentialFile when the target path
	// already exists. This prevents a second bootstrap run from silently
	// overwriting an existing credential.
	errCredFileExists = errors.New("initialadmin: credential file already exists")

	// errCredFileTampered is returned by removeCredentialFile when the file
	// security state deviates from the expected policy: on Unix this means the
	// mode is no longer 0600; on Windows it means the DACL protection, ACE count,
	// ACE type, or access mask has been altered.
	errCredFileTampered = errors.New("initialadmin: credential file security state unexpectedly changed; policy violated")
)

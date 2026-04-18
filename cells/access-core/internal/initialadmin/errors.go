package initialadmin

import "errors"

// Sentinel errors for credential file operations.
//
// These sentinels intentionally use errors.New rather than the project-wide
// errcode package: they are package-internal signalling values used only within
// this package (WriteCredentialFile, RemoveCredentialFile) and tested via
// errors.Is in callers that live in the same package. They are never returned
// through the HTTP boundary or exposed as API error codes, so the errcode
// convention ("no bare errors.New for external-facing errors") does not apply
// here.
var (
	// ErrCredFileExists is returned by WriteCredentialFile when the target path
	// already exists. This prevents a second bootstrap run from silently
	// overwriting an existing credential.
	ErrCredFileExists = errors.New("initialadmin: credential file already exists")

	// ErrCredFileTampered is returned by RemoveCredentialFile when the file
	// permission is not 0600, indicating the file may have been modified by an
	// operator or malicious process.
	ErrCredFileTampered = errors.New("initialadmin: credential file mode unexpectedly changed")
)

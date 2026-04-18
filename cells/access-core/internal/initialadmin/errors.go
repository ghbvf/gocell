package initialadmin

import "errors"

// Sentinel errors for credential file operations.
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

// Package scaffoldfs centralizes filesystem permissions for gocell scaffold/generate
// CLI output. The output is developer source code (cell.yaml, slice.yaml, handler.go,
// indexes, assemblies) that must be readable+writable by the developer's umask + git +
// multi-user CI runners. Restrictive 0o600/0o700 breaks these workflows.
package scaffoldfs

import "os"

const (
	// FileMode is the permission for scaffold-generated source files.
	FileMode os.FileMode = 0o644
	// DirMode is the permission for scaffold-generated source directories.
	DirMode os.FileMode = 0o755
)

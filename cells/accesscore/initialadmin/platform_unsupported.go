//go:build !unix && !windows

package initialadmin

// PlatformSupported returns an error on platforms that are neither unix nor windows.
func PlatformSupported() error { return errUnsupportedPlatform }

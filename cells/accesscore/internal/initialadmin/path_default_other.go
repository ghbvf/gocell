//go:build !linux && !darwin && !windows

package initialadmin

func defaultCredentialDir() (string, error) {
	return "/run/gocell", nil
}

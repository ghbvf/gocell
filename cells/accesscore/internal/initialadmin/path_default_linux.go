//go:build linux

package initialadmin

func defaultCredentialDir() (string, error) {
	return "/run/gocell", nil
}

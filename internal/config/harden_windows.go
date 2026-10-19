//go:build windows

package config

import (
	"os/exec"

	"golang.org/x/sys/windows"
)

// harden restricts the config file, which holds camera passwords and the API
// token, to SYSTEM, the local administrators and the account the agent runs
// as. That last one matters: without it an agent started under a plain user
// account would lock itself out of its own configuration on the next start.
//
// It is best effort — without icacls the agent still works, the file just
// keeps the permissions it inherits from the directory.
func harden(path string) {
	args := []string{path, "/inheritance:r",
		"/grant:r", "*S-1-5-18:(F)", // SYSTEM
		"/grant:r", "*S-1-5-32-544:(F)", // Administrators
	}
	if sid, err := currentUserSID(); err == nil && sid != "" {
		args = append(args, "/grant:r", "*"+sid+":(F)")
	}
	_ = exec.Command("icacls", args...).Run()
}

// currentUserSID returns the SID of the account this process runs under.
func currentUserSID() (string, error) {
	token := windows.GetCurrentProcessToken()
	user, err := token.GetTokenUser()
	if err != nil {
		return "", err
	}
	return user.User.Sid.String(), nil
}

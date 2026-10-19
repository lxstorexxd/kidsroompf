//go:build linux

package config

import "os"

// harden restricts the config file to its owner, which is root: the file holds
// the camera passwords, the TLS private key and the control API token.
//
// The temporary file Save writes is created with the process umask applied, so
// the mode is set here rather than at creation — otherwise a default umask of
// 022 would leave the secrets world-readable for the instant between rename
// and this call, and for good on a system where this call fails.
//
// It is best effort, like its Windows counterpart: the agent works either way,
// the file merely keeps the permissions it was created with.
func harden(path string) {
	_ = os.Chmod(path, 0o600)
}

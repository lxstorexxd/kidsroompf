//go:build windows

// Package winutil holds the small Windows shell helpers shared by the tray and
// the settings window.
package winutil

import (
	"os"
	"os/exec"
	"syscall"
)

const createNoWindow = 0x08000000

// OpenURL opens a link in the user's default browser.
func OpenURL(url string) error {
	// rundll32 avoids the console window that "cmd /c start" would flash.
	cmd := exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", url)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow}
	return cmd.Start()
}

// OpenFolder shows a directory in Explorer.
func OpenFolder(path string) error {
	// explorer.exe returns a non-zero exit code even on success, so the
	// process is started and not waited on.
	return exec.Command("explorer.exe", path).Start()
}

// RunSelf starts another copy of the current executable with the given
// arguments, detached from this process.
func RunSelf(args ...string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow}
	return cmd.Start()
}

//go:build windows

package winutil

import (
	"os"

	"golang.org/x/sys/windows"
)

// attachParentProcess is ATTACH_PARENT_PROCESS: attach to the console of the
// process that started this one.
const attachParentProcess = ^uintptr(0) // (DWORD)-1

// AttachConsole makes the command line modes able to print.
//
// The agent is linked as a GUI application so that starting the tray at logon
// never flashes a console window. A GUI application starts without a console,
// so it borrows the caller's one.
//
// Streams that the caller already redirected are left alone: a pipe or a file
// from "kidsroom-agent.exe status > out.txt" is a perfectly good stdout, and
// replacing it with the console would send the output to the wrong place.
//
// It reports whether anything can be printed at all. Started by double click
// or by the service manager there is no console and no redirection, and the
// output is dropped — which is correct for those cases.
func AttachConsole() bool {
	// Validity is sampled first: attaching a console can fill in the standard
	// handles, which would hide the fact that the caller redirected nothing.
	outRedirected := stdHandleValid(windows.STD_OUTPUT_HANDLE)
	errRedirected := stdHandleValid(windows.STD_ERROR_HANDLE)

	kernel32 := windows.NewLazySystemDLL("kernel32.dll")
	r, _, _ := kernel32.NewProc("AttachConsole").Call(attachParentProcess)
	attached := r != 0

	if attached {
		// Go bound os.Stdout to the handles this process started with, which
		// for a GUI application are empty, so the streams that were not
		// redirected are reopened on the console just attached.
		if !outRedirected {
			if f, err := os.OpenFile("CONOUT$", os.O_WRONLY, 0); err == nil {
				os.Stdout = f
			}
		}
		if !errRedirected {
			if f, err := os.OpenFile("CONOUT$", os.O_WRONLY, 0); err == nil {
				os.Stderr = f
			}
		}
	}

	return attached || outRedirected
}

func stdHandleValid(which uint32) bool {
	h, err := windows.GetStdHandle(which)
	return err == nil && h != 0 && h != windows.InvalidHandle
}

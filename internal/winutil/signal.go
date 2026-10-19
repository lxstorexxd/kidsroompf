//go:build windows

package winutil

import (
	"golang.org/x/sys/windows"
)

// Signal is a named Windows event used to poke the copy of the agent that is
// already running. Starting the agent a second time does not open a second
// tray icon; it raises the settings window of the first one instead.
type Signal struct {
	handle windows.Handle
}

// NewSignal creates the named event that this process will listen on.
func NewSignal(name string) (*Signal, error) {
	ptr, err := windows.UTF16PtrFromString(`Local\` + name)
	if err != nil {
		return nil, err
	}
	// Auto-reset, initially unset: each Raise releases exactly one Wait.
	handle, err := windows.CreateEvent(nil, 0, 0, ptr)
	if err != nil && err != windows.ERROR_ALREADY_EXISTS {
		return nil, err
	}
	return &Signal{handle: handle}, nil
}

// Wait blocks until someone raises the signal.
func (s *Signal) Wait() error {
	_, err := windows.WaitForSingleObject(s.handle, windows.INFINITE)
	return err
}

func (s *Signal) Close() {
	if s == nil || s.handle == 0 {
		return
	}
	_ = windows.CloseHandle(s.handle)
	s.handle = 0
}

// Raise wakes the process waiting on the named signal. It reports whether
// anyone was listening.
func Raise(name string) bool {
	ptr, err := windows.UTF16PtrFromString(`Local\` + name)
	if err != nil {
		return false
	}
	handle, err := windows.OpenEvent(windows.EVENT_MODIFY_STATE, false, ptr)
	if err != nil {
		return false
	}
	defer windows.CloseHandle(handle)
	return windows.SetEvent(handle) == nil
}

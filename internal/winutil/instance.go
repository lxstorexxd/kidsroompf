//go:build windows

package winutil

import (
	"golang.org/x/sys/windows"
)

// Instance is a named mutex that marks a running copy of the agent.
type Instance struct {
	handle windows.Handle
}

// Release drops the claim. Windows frees the mutex when the process exits
// anyway, so this only matters for long-lived processes that give it up early.
func (i *Instance) Release() {
	if i == nil || i.handle == 0 {
		return
	}
	_ = windows.CloseHandle(i.handle)
	i.handle = 0
}

// Acquire claims a named mutex and reports whether another process already
// holds it.
//
// Scope decides who shares the name: a session-local mutex keeps one tray icon
// and one settings window per logged-in user, while a machine-wide one keeps a
// single agent across the service session and any console run.
//
// A machine-wide claim needs SeCreateGlobalPrivilege, which an ordinary user
// does not have; in that case the call reports no conflict rather than failing,
// and the caller falls back to whatever guard it has (for the agent, the port
// it binds).
func Acquire(name string, machineWide bool) (inst *Instance, already bool, err error) {
	prefix := `Local\`
	if machineWide {
		prefix = `Global\`
	}
	ptr, err := windows.UTF16PtrFromString(prefix + name)
	if err != nil {
		return nil, false, err
	}

	handle, err := windows.CreateMutex(nil, false, ptr)
	switch err {
	case nil:
		return &Instance{handle: handle}, false, nil
	case windows.ERROR_ALREADY_EXISTS:
		// The handle is valid even then, and closing it must not release the
		// owner's claim — this process simply holds a second reference.
		return &Instance{handle: handle}, true, nil
	case windows.ERROR_ACCESS_DENIED:
		return nil, false, nil
	default:
		return nil, false, err
	}
}

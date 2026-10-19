//go:build windows

package winutil

import (
	"fmt"
	"os"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	seeMaskNoCloseProcess = 0x00000040
	seeMaskNoAsync        = 0x00000100
	swShowNormal          = 1
)

// shellExecuteInfo mirrors SHELLEXECUTEINFOW. ShellExecuteEx is used instead of
// the simpler ShellExecute because only it hands back a process handle, and the
// caller has to wait for the elevated step to finish before carrying on.
type shellExecuteInfo struct {
	cbSize       uint32
	fMask        uint32
	hwnd         uintptr
	lpVerb       *uint16
	lpFile       *uint16
	lpParameters *uint16
	lpDirectory  *uint16
	nShow        int32
	hInstApp     windows.Handle
	lpIDList     uintptr
	lpClass      *uint16
	hkeyClass    windows.Handle
	dwHotKey     uint32
	hIcon        windows.Handle
	hProcess     windows.Handle
}

// ErrElevationDeclined is returned when the user dismisses the UAC prompt.
var ErrElevationDeclined = fmt.Errorf("запуск с правами администратора отменён")

// RunElevatedSelf starts this executable again with administrator rights and
// waits for it to finish. Installing a service, touching the firewall and
// starting or stopping the service all need those rights, and the tray runs as
// an ordinary user.
func RunElevatedSelf(args ...string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}

	verb, err := windows.UTF16PtrFromString("runas")
	if err != nil {
		return err
	}
	file, err := windows.UTF16PtrFromString(exe)
	if err != nil {
		return err
	}
	params, err := windows.UTF16PtrFromString(strings.Join(args, " "))
	if err != nil {
		return err
	}

	info := shellExecuteInfo{
		fMask:        seeMaskNoCloseProcess | seeMaskNoAsync,
		lpVerb:       verb,
		lpFile:       file,
		lpParameters: params,
		nShow:        swShowNormal,
	}
	info.cbSize = uint32(unsafe.Sizeof(info))

	shell32 := windows.NewLazySystemDLL("shell32.dll")
	r, _, callErr := shell32.NewProc("ShellExecuteExW").Call(uintptr(unsafe.Pointer(&info)))
	if r == 0 {
		if callErr == windows.ERROR_CANCELLED {
			return ErrElevationDeclined
		}
		return fmt.Errorf("не удалось запросить права администратора: %w", callErr)
	}
	if info.hProcess == 0 {
		// Elevation was granted but no handle came back; nothing to wait on.
		return nil
	}
	defer windows.CloseHandle(info.hProcess)

	if _, err := windows.WaitForSingleObject(info.hProcess, windows.INFINITE); err != nil {
		return err
	}
	var code uint32
	if err := windows.GetExitCodeProcess(info.hProcess, &code); err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("%s завершилась с кодом %d", strings.Join(args, " "), code)
	}
	return nil
}

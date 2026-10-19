//go:build windows

package winutil

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	mbOK              = 0x00000000
	mbYesNo           = 0x00000004
	mbIconError       = 0x00000010
	mbIconQuestion    = 0x00000020
	mbIconInformation = 0x00000040
	mbSetForeground   = 0x00010000
	mbTopmost         = 0x00040000

	idYes = 6
)

// Ask shows a yes/no dialog and reports whether the user agreed. It is used
// before anything that needs administrator rights, so the UAC prompt never
// comes out of nowhere.
func Ask(title, text string) bool {
	return messageBox(title, text, mbYesNo|mbIconQuestion) == idYes
}

// Inform shows a plain message.
func Inform(title, text string) {
	messageBox(title, text, mbOK|mbIconInformation)
}

// Fail shows an error message.
func Fail(title, text string) {
	messageBox(title, text, mbOK|mbIconError)
}

func messageBox(title, text string, flags uint32) int32 {
	textPtr, err := windows.UTF16PtrFromString(text)
	if err != nil {
		return 0
	}
	titlePtr, err := windows.UTF16PtrFromString(title)
	if err != nil {
		return 0
	}
	// The tray has no window to own the dialog, so it is brought forward
	// explicitly; otherwise it can open behind whatever the user is doing.
	ret, _, _ := windows.NewLazySystemDLL("user32.dll").NewProc("MessageBoxW").Call(
		0,
		uintptr(unsafe.Pointer(textPtr)),
		uintptr(unsafe.Pointer(titlePtr)),
		uintptr(flags|mbSetForeground|mbTopmost),
	)
	return int32(ret)
}

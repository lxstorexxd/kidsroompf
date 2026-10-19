package svc

import (
	"os"

	"golang.org/x/sys/windows/registry"
)

const (
	runKeyPath = `Software\Microsoft\Windows\CurrentVersion\Run`
	runValue   = "kidsroom-agent-tray"
)

// EnableTrayAutostart starts the tray icon when the current user logs in.
//
// The service runs in session 0 and cannot show UI, so the icon is a separate
// process of the same executable. It starts with "tray", which shows the icon
// without opening the settings window.
func EnableTrayAutostart() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	key, _, err := registry.CreateKey(registry.CURRENT_USER, runKeyPath, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer key.Close()
	return key.SetStringValue(runValue, `"`+exe+`" tray`)
}

// DisableTrayAutostart removes the autostart entry.
func DisableTrayAutostart() error {
	key, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.SET_VALUE)
	if err != nil {
		if err == registry.ErrNotExist {
			return nil
		}
		return err
	}
	defer key.Close()
	if err := key.DeleteValue(runValue); err != nil && err != registry.ErrNotExist {
		return err
	}
	return nil
}

// TrayAutostartEnabled reports whether the autostart entry is present.
func TrayAutostartEnabled() bool {
	key, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer key.Close()
	_, _, err = key.GetStringValue(runValue)
	return err == nil
}

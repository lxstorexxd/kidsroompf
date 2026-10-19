//go:build windows

package main

import (
	"kidsroom-agent/internal/tray"
	"kidsroom-agent/internal/winutil"
)

// instanceName guards the tray: one icon per logged-in user.
const instanceName = "kidsroom-agent-tray"

// The parts of the usage text that only exist on Windows.
const (
	usageIntro    = "Без аргументов: значок в трее и окно настроек.\n\n"
	usageCommands = "  tray             только значок в трее (так агент стартует при входе в систему)\n"
	installHelp   = "установить и запустить службу, открыть порты, включить автозапуск"
	uninstallHelp = "остановить и удалить службу, снять правила брандмауэра и автозапуск"
	firewallHelp  = "пересоздать правила брандмауэра по текущим настройкам"
	usageTail     = ""
)

// platformCommand handles the two modes that need a desktop to draw on.
func platformCommand(cmd string) (bool, error) {
	switch cmd {
	case "":
		// Started by hand: show the icon and open the settings window.
		runTray(true)
		return true, nil
	case "tray":
		// Started at logon: only the icon.
		runTray(false)
		return true, nil
	}
	return false, nil
}

func attachConsole() { winutil.AttachConsole() }

// runTray starts the tray icon, or hands the request to the copy that is
// already running: a second launch raises the settings window of the first one
// instead of adding another icon.
func runTray(showWindow bool) {
	inst, already, err := winutil.Acquire(instanceName, false)
	if err == nil && already {
		winutil.Raise(tray.ShowSignal)
		return
	}
	defer inst.Release()

	tray.Run(showWindow)
}

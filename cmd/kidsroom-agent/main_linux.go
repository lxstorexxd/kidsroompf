//go:build linux

package main

import "kidsroom-agent/internal/svc"

// The parts of the usage text that differ on Linux. There is no tray and no
// settings window: a site is configured from the hub, or by editing config.json
// and restarting the service.
const (
	usageIntro    = ""
	usageCommands = ""
	installHelp   = "разложить файлы, установить и запустить службу systemd"
	uninstallHelp = "остановить и удалить службу (настройки и логи останутся)"
	firewallHelp  = "показать, какие порты нужно открыть"
	usageTail     = "\nСлужба:    " + svc.UnitFile + "\nЖурнал:    journalctl -u " + svc.Name + "\n"
)

// platformCommand: a bare launch prints the usage. A daemon started with no
// arguments on a server is a mistake, not a request to run in the foreground —
// that is what "run" is for.
func platformCommand(cmd string) (bool, error) {
	if cmd == "" {
		usage()
		return true, nil
	}
	return false, nil
}

// attachConsole has nothing to do: this binary is an ordinary console program.
// Only the Windows build is linked as a GUI application and has to borrow the
// console of whoever started it.
func attachConsole() {}

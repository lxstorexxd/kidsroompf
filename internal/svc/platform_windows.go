//go:build windows

package svc

import "github.com/kardianos/service"

// executablePath and workingDirectory stay empty: the service manager runs the
// binary where the installation kit put it, and the agent builds every path it
// needs from ProgramData rather than from the current directory.
func executablePath() string { return "" }

func workingDirectory() string { return "" }

func serviceOptions() service.KeyValue {
	return service.KeyValue{
		"StartType":              "automatic",
		"OnFailure":              "restart",
		"OnFailureDelayDuration": "5s",
		"OnFailureResetPeriod":   60,
	}
}

// Prepare has nothing to do on Windows. The kit is unpacked wherever the
// administrator chose, and that is where the service is registered from.
func Prepare(func(string, ...any)) error { return nil }

func afterInstall(logf func(string, ...any)) {
	if err := EnsureFirewall(logf); err != nil {
		logf("ВНИМАНИЕ: правила брандмауэра не настроены: %v", err)
	}
	if err := EnableTrayAutostart(); err != nil {
		logf("ВНИМАНИЕ: автозапуск значка в трее не настроен: %v", err)
	} else {
		logf("значок в трее будет запускаться при входе в систему")
	}
}

func beforeUninstall(logf func(string, ...any)) {
	if err := RemoveFirewall(logf); err != nil {
		logf("правила брандмауэра не удалены: %v", err)
	}
	if err := DisableTrayAutostart(); err != nil {
		logf("автозапуск трея не снят: %v", err)
	}
}

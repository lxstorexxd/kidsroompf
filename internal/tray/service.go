//go:build windows

package tray

import (
	"errors"
	"time"

	"kidsroom-agent/internal/client"
	"kidsroom-agent/internal/svc"
	"kidsroom-agent/internal/winutil"
)

// waitForAgent is how long the agent is given to answer after the service has
// been started: it binds its ports and launches mediamtx first.
const waitForAgent = 15 * time.Second

// ensureService brings the background service up before the window opens.
//
// Without it a first launch would greet the user with a connection error:
// the settings window talks to the service, and on a fresh machine there is
// no service yet.
//
// It is only called on a manual launch. At logon the tray starts silently and
// shows the problem in its menu instead, because dialogs and a UAC prompt in
// the middle of signing in would be worse than a wrong-looking icon.
//
// It reports whether the agent answers afterwards. When it does not, the user
// has already been told why, and the caller must not pile a second error
// dialog on top by opening a window with nothing in it.
func ensureService() bool {
	switch svc.CurrentState() {
	case svc.StateRunning:
		return waitUntilAnswering()

	case svc.StateNotInstalled:
		if !winutil.Ask("Трансляция детской",
			"Служба kidsroom-agent ещё не установлена.\n\n"+
				"Установить её сейчас? Понадобятся права администратора.") {
			return false
		}
		if err := elevate("install"); err != nil {
			return false
		}
		// The elevated step may have run under a different account, and the
		// autostart entry belongs to the user who is actually signed in.
		if err := svc.EnableTrayAutostart(); err != nil {
			winutil.Fail("Трансляция детской",
				"Служба установлена, но автозапуск значка не настроен:\n\n"+err.Error())
		}
		winutil.Inform("Трансляция детской", "Служба установлена и запущена.")
		return waitUntilAnswering()

	case svc.StateStopped:
		// Starting a service needs rights the tray does not have, so the
		// attempt goes straight through the elevated path.
		if !winutil.Ask("Трансляция детской",
			"Служба kidsroom-agent установлена, но остановлена.\n\n"+
				"Запустить её? Понадобятся права администратора.") {
			return false
		}
		if err := elevate("start"); err != nil {
			return false
		}
		return waitUntilAnswering()
	}

	return waitUntilAnswering()
}

// elevate runs one of the agent's own commands as administrator and reports
// the outcome to the user.
func elevate(command string) error {
	err := winutil.RunElevatedSelf(command)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, winutil.ErrElevationDeclined):
		winutil.Fail("Трансляция детской",
			"Без прав администратора служба не может быть "+actionName(command)+".\n\n"+
				"Запустите из командной строки администратора:\n"+
				"    kidsroom-agent.exe "+command)
	default:
		winutil.Fail("Трансляция детской",
			"Не удалось выполнить "+command+":\n\n"+err.Error())
	}
	return err
}

func actionName(command string) string {
	switch command {
	case "install":
		return "установлена"
	case "stop":
		return "остановлена"
	default:
		return "запущена"
	}
}

// waitUntilAnswering blocks until the agent responds on its loopback API, so
// the settings window opens with real data instead of an error. It reports
// whether the agent came up within the deadline.
func waitUntilAnswering() bool {
	c := client.New()
	deadline := time.Now().Add(waitForAgent)
	for {
		if _, err := c.Status(); err == nil {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// confirmQuit asks before the stream goes dark. "Выход" stops the service, so
// everyone watching loses the picture — that is worth one question.
func confirmQuit() bool {
	if svc.CurrentState() != svc.StateRunning {
		return true
	}
	return winutil.Ask("Трансляция детской",
		"Выход остановит службу: трансляция станет недоступна для всех, "+
			"пока агент не запустят снова.\n\nПродолжить?")
}

// stopService stops the Windows service on the way out. Stopping a service
// needs administrator rights, so the attempt falls back to the elevated path.
func stopService() {
	switch svc.CurrentState() {
	case svc.StateNotInstalled, svc.StateStopped:
		return
	}
	if err := svc.Control("stop"); err == nil {
		return
	}
	// The user may refuse the prompt; the icon closes either way, and elevate
	// has already explained what to run by hand.
	_ = elevate("stop")
}

// Command kidsroom-agent is the nursery streaming agent: the service, the
// command line, and on Windows the tray icon with the settings window, all in
// one executable.
//
// The Windows binary is linked as a GUI application so that starting the tray
// at logon never flashes a console window; its command line modes borrow the
// caller's console (see winutil.AttachConsole). The Linux binary is an ordinary
// console program with no tray and no window: a site there is configured from
// the hub, or by editing config.json.
package main

import (
	"fmt"
	"os"
	"strings"

	"kidsroom-agent/internal/app"
	"kidsroom-agent/internal/client"
	"kidsroom-agent/internal/config"
	"kidsroom-agent/internal/mediamtx"
	"kidsroom-agent/internal/paths"
	"kidsroom-agent/internal/svc"
	"kidsroom-agent/internal/webdefaults"
)

func main() {
	cmd := ""
	if len(os.Args) > 1 {
		cmd = strings.ToLower(strings.TrimLeft(os.Args[1], "-/"))
	}

	// What exists on one platform only goes first: on Windows a bare launch
	// opens the tray icon and the settings window.
	if handled, err := platformCommand(cmd); handled {
		if err != nil {
			fmt.Fprintln(os.Stderr, "ошибка:", err)
			os.Exit(1)
		}
		return
	}

	if cmd == "run" {
		// Started by the service manager; there may be no console to write to.
		if err := svc.Run(); err != nil {
			fmt.Fprintln(os.Stderr, "ошибка:", err)
			os.Exit(1)
		}
		return
	}

	attachConsole()

	var err error
	switch cmd {
	case "install":
		err = install()
	case "uninstall":
		err = svc.Uninstall(say)
	case "start", "stop", "restart":
		if err = svc.Control(cmd); err == nil {
			say("готово: %s", cmd)
		}
	case "status":
		err = printStatus()
	case "fetch-mediamtx":
		err = mediamtx.Fetch(say)
	case "reset-web":
		err = resetWeb()
	case "firewall":
		err = svc.EnsureFirewall(say)
	case "token":
		err = printToken()
	case "version":
		say("kidsroom-agent %s (mediamtx %s)", app.Version, mediamtx.Version)
	case "help", "h", "?":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "неизвестная команда: %s\n\n", cmd)
		usage()
		os.Exit(2)
	}

	if err != nil {
		fmt.Fprintln(os.Stderr, "ошибка:", err)
		os.Exit(1)
	}
}

// install makes one step produce a working setup: without mediamtx the service
// would come up and serve a page with no video.
func install() error {
	// On Linux this creates the directories and copies the agent and mediamtx
	// to the paths the unit will use, so the check below looks where the
	// service will actually read from. On Windows there is nothing to prepare.
	if err := svc.Prepare(say); err != nil {
		return err
	}
	if _, statErr := os.Stat(paths.MediamtxBinary()); statErr != nil {
		say("mediamtx не найден, скачиваю…")
		if err := mediamtx.Fetch(say); err != nil {
			return fmt.Errorf("не удалось получить mediamtx: %w", err)
		}
	}
	return svc.Install(say)
}

func say(format string, args ...any) {
	fmt.Printf(format+"\n", args...)
}

func usage() {
	fmt.Print("kidsroom-agent — трансляция детской комнаты.\n\n" + usageIntro +
		"Команды:\n" + usageCommands +
		`  run              запустить агента в текущем процессе (так его запускает служба)
  install          ` + installHelp + `
  uninstall        ` + uninstallHelp + `
  start|stop|restart   управление установленной службой
  status           состояние агента и камер
  fetch-mediamtx   скачать и установить mediamtx ` + mediamtx.Version + `
  reset-web        вернуть заводскую страницу (текущая сохранится рядом)
  firewall         ` + firewallHelp + `
  token            показать токен control API
  version          версия
` + usageTail + `
Настройки: ` + paths.ConfigFile() + `
Страница:  ` + paths.DefaultWebDir() + `
Логи:      ` + paths.LogsDir() + `
`)
}

func printStatus() error {
	st, err := client.New().Status()
	if err != nil {
		return err
	}

	say("Агент:       %s, аптайм %s", st.Version, st.Uptime)
	say("Страница:    %s", st.PublicURL)
	say("Control API: %s", st.ControlAddr)
	say("Каталог web: %s", st.WebDir)

	if st.Certificate.Available {
		kind := "выданный"
		if st.Certificate.SelfSign {
			kind = "self-signed"
		}
		say("Сертификат:  %s (%s), до %s, имена: %s",
			st.Certificate.Subject, kind,
			st.Certificate.NotAfter.Format("2006-01-02"),
			strings.Join(st.Certificate.Hosts, ", "))
	}

	if st.Stream.Running {
		say("mediamtx:    работает, pid %d, аптайм %s, перезапусков %d",
			st.Stream.PID, st.Stream.Uptime, st.Stream.Restarts)
	} else {
		say("mediamtx:    НЕ РАБОТАЕТ %s", st.Stream.LastError)
	}

	say("Камеры:")
	if len(st.Cameras) == 0 {
		say("  (не настроены)")
	}
	for _, c := range st.Cameras {
		state := "выключена"
		switch {
		case c.Enabled && c.Ready:
			state = fmt.Sprintf("в эфире, зрителей: %d", c.Readers)
		case c.Enabled:
			state = "нет потока"
		}
		say("  %-12s %-20s %s", c.ID, c.Name, state)
	}

	for _, w := range st.Warnings {
		say("! %s", w)
	}
	return nil
}

func printToken() error {
	token, err := client.New().Token()
	if err == nil {
		say("%s", token)
		return nil
	}
	// The service may be stopped; fall back to the config file, which an
	// administrator can read.
	cfg, _, ferr := config.Load(paths.ConfigFile())
	if ferr != nil {
		return err
	}
	say("%s", cfg.Control.Token)
	return nil
}

func resetWeb() error {
	cfg, _, err := config.Load(paths.ConfigFile())
	if err != nil {
		return err
	}
	backup, err := webdefaults.Reset(cfg.WebDir, say)
	if err != nil {
		return err
	}
	if backup != "" {
		say("прежняя страница сохранена: %s", backup)
	}
	say("заводская страница восстановлена: %s", cfg.WebDir)
	return nil
}

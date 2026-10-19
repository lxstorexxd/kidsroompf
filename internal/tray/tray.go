//go:build windows

// Package tray is the per-user face of the agent: the status icon in the
// notification area and the settings window it owns.
package tray

import (
	_ "embed"
	"fmt"
	"strings"
	"time"

	"fyne.io/systray"

	"kidsroom-agent/internal/app"
	"kidsroom-agent/internal/client"
	"kidsroom-agent/internal/gui"
	"kidsroom-agent/internal/paths"
	"kidsroom-agent/internal/svc"
	"kidsroom-agent/internal/winutil"
)

//go:embed icons/icon.ico
var iconOK []byte

//go:embed icons/icon-warn.ico
var iconWarn []byte

//go:embed icons/icon-down.ico
var iconDown []byte

const (
	pollInterval = 5 * time.Second
	// ShowSignal is raised by a second launch of the agent to bring this
	// instance's settings window up instead of starting a duplicate.
	ShowSignal = "kidsroom-agent-show"
)

// Run shows the tray icon and blocks until the user quits from its menu.
// showWindow opens the settings window right away, which is what a manual
// launch should do; the logon autostart leaves it closed.
func Run(showWindow bool) {
	host := gui.NewHost()
	host.Start()

	// A second launch of the agent raises this signal instead of starting its
	// own icon.
	if sig, err := winutil.NewSignal(ShowSignal); err == nil {
		go func() {
			for {
				if err := sig.Wait(); err != nil {
					return
				}
				host.Show()
			}
		}()
	}

	if showWindow {
		// A manual launch is the moment to get the service going. If it still
		// does not answer, the user has just been told why; opening a window
		// that can only show another error would add nothing.
		if ensureService() {
			host.Show()
		}
	}

	systray.Run(func() { onReady(host) }, func() { host.Quit() })
}

func onReady(host *gui.Host) {
	systray.SetIcon(iconDown)
	systray.SetTitle("Трансляция детской")
	systray.SetTooltip("kidsroom-agent: подключаюсь…")

	mSettings := systray.AddMenuItem("Настройки…", "Открыть окно настроек")
	mOpen := systray.AddMenuItem("Открыть трансляцию", "Открыть страницу в браузере")
	mStatus := systray.AddMenuItem("Состояние: подключаюсь…", "")
	mStatus.Disable()

	systray.AddSeparator()
	mService := systray.AddMenuItem("Запустить службу…", "Служба не работает")
	mService.Hide()
	mRestart := systray.AddMenuItem("Перезапустить поток", "Перезапустить mediamtx")
	mWebDir := systray.AddMenuItem("Папка страницы", "Открыть каталог с index.html")
	mLogs := systray.AddMenuItem("Папка логов", "Открыть каталог логов")

	systray.AddSeparator()
	mQuit := systray.AddMenuItem("Выход", "Остановить службу и закрыть значок")

	c := client.New()
	state := &uiState{client: c, status: mStatus, service: mService}
	go state.poll()

	// Left click opens the window, right click the menu — the behaviour
	// Windows users expect of a tray application.
	systray.SetOnTapped(func() { host.Show() })

	go func() {
		for {
			select {
			case <-mSettings.ClickedCh:
				host.Show()
			case <-mService.ClickedCh:
				if ensureService() {
					host.Show()
				}
			case <-mOpen.ClickedCh:
				_ = winutil.OpenURL(state.publicURL())
			case <-mRestart.ClickedCh:
				if err := c.Restart(); err != nil {
					state.setStatus("Перезапуск не удался: " + err.Error())
				} else {
					state.setStatus("Поток перезапускается…")
				}
			case <-mWebDir.ClickedCh:
				_ = winutil.OpenFolder(state.webDir())
			case <-mLogs.ClickedCh:
				_ = winutil.OpenFolder(paths.LogsDir())
			case <-mQuit.ClickedCh:
				if !confirmQuit() {
					continue
				}
				stopService()
				systray.Quit()
				return
			}
		}
	}()
}

type uiState struct {
	client  *client.Client
	status  *systray.MenuItem
	service *systray.MenuItem

	last *app.Status
}

// offerServiceFix puts the matching repair action in the menu while the agent
// is unreachable.
func (u *uiState) offerServiceFix() {
	switch svc.CurrentState() {
	case svc.StateNotInstalled:
		u.service.SetTitle("Установить службу…")
		u.service.Show()
	case svc.StateStopped:
		u.service.SetTitle("Запустить службу…")
		u.service.Show()
	default:
		u.service.Hide()
	}
}

func (u *uiState) publicURL() string {
	if u.last != nil && u.last.PublicURL != "" {
		return u.last.PublicURL
	}
	return "https://localhost:8443/"
}

func (u *uiState) webDir() string {
	if u.last != nil && u.last.WebDir != "" {
		return u.last.WebDir
	}
	return paths.DefaultWebDir()
}

func (u *uiState) setStatus(text string) {
	u.status.SetTitle(text)
	systray.SetTooltip("kidsroom-agent — " + text)
}

func (u *uiState) poll() {
	for {
		st, err := u.client.Status()
		if err != nil {
			u.last = nil
			systray.SetIcon(iconDown)
			u.setStatus(serviceHint())
			u.offerServiceFix()
		} else {
			u.service.Hide()
			u.last = st
			icon, text := summarise(st)
			systray.SetIcon(icon)
			u.setStatus(text)
		}
		time.Sleep(pollInterval)
	}
}

// serviceHint tells apart "not installed" from "installed but not answering",
// because the fix differs.
func serviceHint() string {
	switch svc.CurrentState() {
	case svc.StateNotInstalled:
		return "Служба не установлена"
	case svc.StateStopped:
		return "Служба остановлена"
	default:
		return "Служба не отвечает"
	}
}

// summarise turns the status report into one line and the matching icon.
func summarise(st *app.Status) ([]byte, string) {
	var live, enabled int
	var offline []string
	for _, c := range st.Cameras {
		if !c.Enabled {
			continue
		}
		enabled++
		if c.Ready {
			live++
		} else {
			offline = append(offline, c.Name)
		}
	}

	switch {
	case enabled == 0:
		return iconWarn, "Камеры не настроены"
	case !st.Stream.Running:
		return iconDown, "mediamtx не работает"
	case live == enabled:
		return iconOK, fmt.Sprintf("В эфире: %d из %d", live, enabled)
	case live == 0:
		return iconDown, "Нет потока: " + strings.Join(offline, ", ")
	default:
		return iconWarn, fmt.Sprintf("В эфире %d из %d, нет потока: %s",
			live, enabled, strings.Join(offline, ", "))
	}
}

//go:build windows

// Package gui is the native settings window. It writes through the same
// control API that remote provisioning uses, so there is one code path that
// changes settings.
package gui

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/lxn/walk"
	//lint:ignore ST1001 the declarative package is designed to be dot-imported
	. "github.com/lxn/walk/declarative"
	"github.com/lxn/win"

	"kidsroom-agent/internal/certs"
	"kidsroom-agent/internal/client"
	"kidsroom-agent/internal/config"
	"kidsroom-agent/internal/svc"
	"kidsroom-agent/internal/winutil"
)

type window struct {
	mw  *walk.MainWindow
	api *client.Client

	cfg   *config.Config
	cams  *cameraModel
	certI certs.Info

	domainEdit *walk.LineEdit
	portEdit   *walk.LineEdit
	linkLabel  *walk.LinkLabel
	autostart  *walk.CheckBox

	table *walk.TableView

	certLabel *walk.TextLabel

	controlAddrEdit *walk.LineEdit
	controlTLS      *walk.CheckBox
	cidrEdit        *walk.TextEdit
	tokenEdit       *walk.LineEdit
	showToken       *walk.CheckBox

	statusBar *walk.Label
}

// newWindow builds the settings window, hidden. The caller shows it.
func newWindow() (*window, error) {
	w := &window{api: client.New(), cams: &cameraModel{}}

	if err := w.load(); err != nil {
		return nil, err
	}
	if err := w.build(); err != nil {
		return nil, err
	}
	// Checkbox states are set after the widgets exist: the declarative Checked
	// field leaves a false value at the control's default, which would show the
	// autostart box ticked while the registry entry is absent.
	w.autostart.SetChecked(svc.TrayAutostartEnabled())
	w.controlTLS.SetChecked(w.cfg.Control.TLS)
	return w, nil
}

// reportStartupError explains why the window could not open. Without the
// service there is nothing to edit, so an empty window would only confuse.
func reportStartupError(err error) {
	walk.MsgBox(nil, "Агент не отвечает",
		"Не удалось получить настройки у службы kidsroom-agent.\n\n"+
			err.Error()+"\n\nПроверьте, что служба запущена: services.msc → "+svc.Name,
		walk.MsgBoxIconError)
}

// refresh reloads the settings into the widgets. It runs on the UI thread
// every time the window is shown again, so a change made meanwhile through the
// API is picked up.
func (w *window) refresh() {
	if err := w.load(); err != nil {
		w.setStatus("Не удалось обновить настройки: " + err.Error())
		return
	}
	w.cams.PublishRowsReset()
	w.certLabel.SetText(w.certSummary())
	w.linkLabel.SetText(w.publicLink())
	_ = w.domainEdit.SetText(w.cfg.Domain)
	_ = w.portEdit.SetText(strconv.Itoa(w.cfg.ListenPort))
	_ = w.controlAddrEdit.SetText(w.cfg.Control.Addr)
	w.controlTLS.SetChecked(w.cfg.Control.TLS)
	_ = w.cidrEdit.SetText(strings.Join(w.cfg.Control.AllowCIDRs, "\r\n"))
	_ = w.tokenEdit.SetText(w.cfg.Control.Token)
	w.autostart.SetChecked(svc.TrayAutostartEnabled())
	w.setStatus("")
}

// win32BringToFront raises the window above the others. Windows only grants
// the foreground to a process that recently handled input, which is the case
// right after a click on the tray menu.
func win32BringToFront(mw *walk.MainWindow) {
	win.SetForegroundWindow(mw.Handle())
}

func (w *window) load() error {
	cfg, err := w.api.Settings()
	if err != nil {
		return err
	}
	w.cfg = cfg
	w.cams.items = append([]config.Camera(nil), cfg.Cameras...)

	if st, err := w.api.Status(); err == nil {
		w.certI = st.Certificate
	}
	if token, err := w.api.Token(); err == nil {
		w.cfg.Control.Token = token
	}
	return nil
}

func (w *window) build() error {
	var applyPB *walk.PushButton

	return MainWindow{
		AssignTo: &w.mw,
		Title:    WindowTitle,
		MinSize:  Size{Width: 760, Height: 580},
		Size:     Size{Width: 820, Height: 620},
		Layout:   VBox{MarginsZero: false},
		Children: []Widget{
			TabWidget{
				Pages: []TabPage{
					w.pageNetwork(),
					w.pageCameras(),
					w.pageCertificate(),
					w.pageRemote(),
				},
			},
			Composite{
				Layout: HBox{MarginsZero: true},
				Children: []Widget{
					Label{AssignTo: &w.statusBar, Text: ""},
					HSpacer{},
					PushButton{
						AssignTo:  &applyPB,
						Text:      "Применить",
						MinSize:   Size{Width: 120},
						OnClicked: w.apply,
					},
					PushButton{
						Text:      "Закрыть",
						MinSize:   Size{Width: 100},
						OnClicked: func() { w.mw.Close() },
					},
				},
			},
		},
	}.Create()
}

// ---------- вкладка «Сеть» ----------

func (w *window) pageNetwork() TabPage {
	return TabPage{
		Title:  "Сеть",
		Layout: VBox{},
		Children: []Widget{
			GroupBox{
				Title:  "Адрес трансляции",
				Layout: Grid{Columns: 2},
				Children: []Widget{
					Label{Text: "Доменное имя:"},
					LineEdit{AssignTo: &w.domainEdit, Text: w.cfg.Domain, CueBanner: "например nursery.example.ru"},

					Label{Text: "Порт HTTPS:"},
					LineEdit{AssignTo: &w.portEdit, Text: strconv.Itoa(w.cfg.ListenPort), MaxLength: 5},

					Label{Text: "Ссылка:"},
					LinkLabel{
						AssignTo:        &w.linkLabel,
						Text:            w.publicLink(),
						OnLinkActivated: func(link *walk.LinkLabelLink) { _ = winutil.OpenURL(link.URL()) },
					},
				},
			},
			GroupBox{
				Title:  "Страница трансляции",
				Layout: VBox{},
				Children: []Widget{
					Label{Text: w.cfg.WebDir},
					Composite{
						Layout: HBox{MarginsZero: true},
						Children: []Widget{
							PushButton{
								Text:      "Открыть папку страницы",
								OnClicked: func() { _ = winutil.OpenFolder(w.cfg.WebDir) },
							},
							HSpacer{},
						},
					},
				},
			},
			GroupBox{
				Title:  "Значок в трее",
				Layout: HBox{},
				Children: []Widget{
					CheckBox{
						AssignTo: &w.autostart,
						Text:     "Запускать значок в трее при входе в систему",
					},
					HSpacer{},
				},
			},
			VSpacer{},
		},
	}
}

func (w *window) publicLink() string {
	host := w.cfg.Domain
	if strings.TrimSpace(host) == "" {
		host = "localhost"
	}
	url := fmt.Sprintf("https://%s:%d/", host, w.cfg.ListenPort)
	return fmt.Sprintf(`<a id="link" href="%s">%s</a>`, url, url)
}

// ---------- вкладка «Камеры» ----------

func (w *window) pageCameras() TabPage {
	return TabPage{
		Title:  "Камеры",
		Layout: VBox{},
		Children: []Widget{
			TableView{
				AssignTo: &w.table,
				Model:    w.cams,
				Columns: []TableViewColumn{
					{Title: "ID", Width: 90},
					{Title: "Название", Width: 170},
					{Title: "RTSP-адрес", Width: 360},
					{Title: "Включена", Width: 80},
					{Title: "Поток", Width: 90},
				},
				OnItemActivated: w.editCamera,
			},
			Composite{
				Layout: HBox{MarginsZero: true},
				Children: []Widget{
					PushButton{Text: "Добавить", OnClicked: w.addCamera},
					PushButton{Text: "Изменить", OnClicked: w.editCamera},
					PushButton{Text: "Удалить", OnClicked: w.deleteCamera},
					PushButton{Text: "Проверить поток", OnClicked: w.probeCameras},
					HSpacer{},
				},
			},
		},
	}
}

func (w *window) addCamera() {
	cam := config.Camera{ID: w.cams.nextID(), Name: "", URL: "rtsp://", Enabled: true}
	if ok, _ := runCameraDialog(w.mw, &cam); ok {
		w.cams.items = append(w.cams.items, cam)
		w.cams.PublishRowsReset()
	}
}

func (w *window) editCamera() {
	i := w.table.CurrentIndex()
	if i < 0 || i >= len(w.cams.items) {
		return
	}
	cam := w.cams.items[i]
	if ok, _ := runCameraDialog(w.mw, &cam); ok {
		w.cams.items[i] = cam
		w.cams.PublishRowsReset()
	}
}

func (w *window) deleteCamera() {
	i := w.table.CurrentIndex()
	if i < 0 || i >= len(w.cams.items) {
		return
	}
	cam := w.cams.items[i]
	if walk.MsgBox(w.mw, "Удаление камеры",
		fmt.Sprintf("Удалить камеру %q?", cam.Name),
		walk.MsgBoxYesNo|walk.MsgBoxIconQuestion) != walk.DlgCmdYes {
		return
	}
	w.cams.items = append(w.cams.items[:i], w.cams.items[i+1:]...)
	w.cams.PublishRowsReset()
}

// probeCameras refreshes the live column from the agent status.
func (w *window) probeCameras() {
	st, err := w.api.Status()
	if err != nil {
		w.setStatus("Не удалось получить состояние: " + err.Error())
		return
	}
	ready := map[string]bool{}
	for _, c := range st.Cameras {
		ready[c.ID] = c.Ready
	}
	w.cams.ready = ready
	w.cams.PublishRowsReset()
	w.setStatus("Состояние потоков обновлено")
}

// ---------- вкладка «Сертификат» ----------

func (w *window) pageCertificate() TabPage {
	return TabPage{
		Title:  "Сертификат",
		Layout: VBox{},
		Children: []Widget{
			GroupBox{
				Title:  "Текущий сертификат",
				Layout: VBox{},
				Children: []Widget{
					TextLabel{
						AssignTo: &w.certLabel,
						Text:     w.certSummary(),
						MinSize:  Size{Width: 500, Height: 90},
						MaxSize:  Size{Height: 110},
					},
				},
			},
			GroupBox{
				Title:  "Заменить",
				Layout: VBox{},
				Children: []Widget{
					Composite{
						Layout: HBox{MarginsZero: true},
						Children: []Widget{
							PushButton{Text: "Загрузить .crt и .key…", OnClicked: w.loadCertFiles},
							PushButton{Text: "Сгенерировать self-signed", OnClicked: w.makeSelfSigned},
							HSpacer{},
						},
					},
				},
			},
			VSpacer{},
		},
	}
}

func (w *window) certSummary() string {
	if !w.certI.Available {
		return "Сертификат ещё не загружен."
	}
	kind := "выданный центром сертификации"
	if w.certI.SelfSign {
		kind = "самоподписанный (браузер будет предупреждать)"
	}
	return strings.Join([]string{
		"Субъект:   " + w.certI.Subject,
		"Издатель:  " + w.certI.Issuer,
		"Тип:       " + kind,
		"Действует до: " + w.certI.NotAfter.Format("2006-01-02") +
			fmt.Sprintf(" (осталось %d дн.)", w.certI.DaysLeft),
		"Имена:     " + strings.Join(w.certI.Hosts, ", "),
	}, "\r\n")
}

func (w *window) loadCertFiles() {
	certPath, ok := pickFile(w.mw, "Сертификат (PEM)",
		"Сертификаты (*.crt;*.pem;*.cer)|*.crt;*.pem;*.cer|Все файлы (*.*)|*.*")
	if !ok {
		return
	}
	keyPath, ok := pickFile(w.mw, "Приватный ключ (PEM)",
		"Ключи (*.key;*.pem)|*.key;*.pem|Все файлы (*.*)|*.*")
	if !ok {
		return
	}

	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		w.errorBox("Не удалось прочитать сертификат", err)
		return
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		w.errorBox("Не удалось прочитать ключ", err)
		return
	}
	// Check the pair here so the mistake is caught before the service is asked
	// to swap a certificate that cannot work.
	pair, err := certs.LoadPair(string(certPEM), string(keyPEM))
	if err != nil {
		w.errorBox("Сертификат и ключ не подходят друг к другу", err)
		return
	}

	w.cfg.TLS = config.TLSConfig{CertPEM: string(certPEM), KeyPEM: string(keyPEM)}
	w.certI = certs.Describe(pair)
	w.certLabel.SetText(w.certSummary())
	w.setStatus("Сертификат готов к применению — нажмите «Применить»")
}

func (w *window) makeSelfSigned() {
	domain := strings.TrimSpace(w.domainEdit.Text())
	certPEM, keyPEM, err := certs.GenerateSelfSigned(domain)
	if err != nil {
		w.errorBox("Не удалось выписать сертификат", err)
		return
	}
	pair, err := certs.LoadPair(certPEM, keyPEM)
	if err != nil {
		w.errorBox("Не удалось выписать сертификат", err)
		return
	}
	w.cfg.TLS = config.TLSConfig{CertPEM: certPEM, KeyPEM: keyPEM, SelfSigned: true}
	w.certI = certs.Describe(pair)
	w.certLabel.SetText(w.certSummary())
	w.setStatus("Самоподписанный сертификат готов — нажмите «Применить»")
}

// ---------- вкладка «Удалённый доступ» ----------

func (w *window) pageRemote() TabPage {
	return TabPage{
		Title:  "Удалённый доступ",
		Layout: VBox{},
		Children: []Widget{
			GroupBox{
				Title:  "API настроек",
				Layout: Grid{Columns: 2},
				Children: []Widget{
					Label{Text: "Слушать адрес:"},
					LineEdit{AssignTo: &w.controlAddrEdit, Text: w.cfg.Control.Addr},

					Label{Text: ""},
					CheckBox{AssignTo: &w.controlTLS, Text: "Шифровать (HTTPS)"},

					Label{Text: "Разрешённые подсети:"},
					TextEdit{
						AssignTo: &w.cidrEdit,
						Text:     strings.Join(w.cfg.Control.AllowCIDRs, "\r\n"),
						VScroll:  true,
						MinSize:  Size{Height: 90},
						MaxSize:  Size{Height: 120},
					},
				},
			},
			GroupBox{
				Title:  "Токен",
				Layout: VBox{},
				Children: []Widget{
					Composite{
						Layout: HBox{MarginsZero: true},
						Children: []Widget{
							LineEdit{
								AssignTo:     &w.tokenEdit,
								Text:         w.cfg.Control.Token,
								ReadOnly:     true,
								PasswordMode: true,
							},
							CheckBox{
								AssignTo: &w.showToken,
								Text:     "Показать",
								OnCheckedChanged: func() {
									w.tokenEdit.SetPasswordMode(!w.showToken.Checked())
								},
							},
							PushButton{Text: "Копировать", OnClicked: w.copyToken},
							PushButton{Text: "Перегенерировать", OnClicked: w.regenerateToken},
						},
					},
					TextLabel{
						Text: "curl -k -X POST " + w.remoteExampleURL() + "/api/settings ^\r\n" +
							"  -H \"Authorization: Bearer <токен>\" ^\r\n" +
							"  -H \"Content-Type: application/json\" --data-binary @settings.json",
						MinSize: Size{Width: 500, Height: 54},
						MaxSize: Size{Height: 60},
					},
				},
			},
			VSpacer{},
		},
	}
}

func (w *window) remoteExampleURL() string {
	scheme := "http"
	if w.cfg.Control.TLS {
		scheme = "https"
	}
	_, port, err := splitPort(w.cfg.Control.Addr)
	if err != nil {
		return scheme + "://<адрес-агента>"
	}
	return scheme + "://<адрес-агента>:" + port
}

func (w *window) copyToken() {
	if err := walk.Clipboard().SetText(w.tokenEdit.Text()); err != nil {
		w.errorBox("Не удалось скопировать", err)
		return
	}
	w.setStatus("Токен скопирован в буфер обмена")
}

func (w *window) regenerateToken() {
	if walk.MsgBox(w.mw, "Новый токен",
		"Старый токен перестанет работать. Продолжить?",
		walk.MsgBoxYesNo|walk.MsgBoxIconWarning) != walk.DlgCmdYes {
		return
	}
	token, err := w.api.RegenerateToken()
	if err != nil {
		w.errorBox("Не удалось перегенерировать токен", err)
		return
	}
	w.cfg.Control.Token = token
	w.tokenEdit.SetText(token)
	w.setStatus("Токен обновлён")
}

// ---------- применение ----------

func (w *window) apply() {
	port, err := strconv.Atoi(strings.TrimSpace(w.portEdit.Text()))
	if err != nil {
		w.errorBox("Порт", fmt.Errorf("%q не число", w.portEdit.Text()))
		return
	}

	next := w.cfg.Clone()
	next.Domain = strings.TrimSpace(w.domainEdit.Text())
	next.ListenPort = port
	next.Cameras = append([]config.Camera(nil), w.cams.items...)
	next.Control.Addr = strings.TrimSpace(w.controlAddrEdit.Text())
	next.Control.TLS = w.controlTLS.Checked()
	next.Control.AllowCIDRs = splitLines(w.cidrEdit.Text())
	// The token travels only through its own endpoint.
	next.Control.Token = ""

	if err := w.api.Save(next); err != nil {
		w.errorBox("Настройки не применены", err)
		return
	}

	if w.autostart.Checked() != svc.TrayAutostartEnabled() {
		var err error
		if w.autostart.Checked() {
			err = svc.EnableTrayAutostart()
		} else {
			err = svc.DisableTrayAutostart()
		}
		if err != nil {
			w.setStatus("Настройки применены, но автозапуск трея не изменён: " + err.Error())
			return
		}
	}

	if err := w.load(); err != nil {
		w.setStatus("Применено, но состояние не перечитано: " + err.Error())
		return
	}
	w.cams.PublishRowsReset()
	w.certLabel.SetText(w.certSummary())
	w.linkLabel.SetText(w.publicLink())
	w.setStatus("Настройки применены")
}

func (w *window) setStatus(text string) {
	if w.statusBar != nil {
		_ = w.statusBar.SetText(text)
	}
}

func (w *window) errorBox(title string, err error) {
	walk.MsgBox(w.mw, title, err.Error(), walk.MsgBoxIconError)
	w.setStatus(title + ": " + err.Error())
}

// ---------- вспомогательное ----------

func splitLines(s string) []string {
	var out []string
	for _, line := range strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n") {
		if t := strings.TrimSpace(line); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func splitPort(addr string) (host, port string, err error) {
	i := strings.LastIndex(addr, ":")
	if i < 0 {
		return "", "", fmt.Errorf("в адресе %q нет порта", addr)
	}
	return addr[:i], addr[i+1:], nil
}

func pickFile(owner walk.Form, title, filter string) (string, bool) {
	dlg := new(walk.FileDialog)
	dlg.Title = title
	dlg.Filter = filter
	ok, err := dlg.ShowOpen(owner)
	if err != nil || !ok {
		return "", false
	}
	return dlg.FilePath, true
}

// WindowTitle is the caption of the settings window.
const WindowTitle = "Трансляция детской — настройки"

// restoreIfMinimized un-minimizes the window, so reopening it from the tray
// always puts it back on screen.
func (w *window) restoreIfMinimized() {
	if win.IsIconic(w.mw.Handle()) {
		win.ShowWindow(w.mw.Handle(), win.SW_RESTORE)
	}
}

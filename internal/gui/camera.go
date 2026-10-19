//go:build windows

package gui

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/lxn/walk"
	//lint:ignore ST1001 the declarative package is designed to be dot-imported
	. "github.com/lxn/walk/declarative"

	"kidsroom-agent/internal/config"
)

// cameraModel backs the camera table.
type cameraModel struct {
	walk.TableModelBase
	items []config.Camera
	// ready mirrors the last status poll, so the table can show which cameras
	// are actually on air.
	ready map[string]bool
}

func (m *cameraModel) RowCount() int { return len(m.items) }

func (m *cameraModel) Value(row, col int) any {
	cam := m.items[row]
	switch col {
	case 0:
		return cam.ID
	case 1:
		return cam.Name
	case 2:
		return cam.URL
	case 3:
		if cam.Enabled {
			return "да"
		}
		return "нет"
	case 4:
		if m.ready == nil {
			return ""
		}
		if m.ready[cam.ID] {
			return "в эфире"
		}
		if cam.Enabled {
			return "нет потока"
		}
		return ""
	}
	return ""
}

// nextID proposes cam1, cam2, … skipping ids already in the table.
func (m *cameraModel) nextID() string {
	used := map[string]bool{}
	for _, c := range m.items {
		used[c.ID] = true
	}
	for i := 1; i < 100; i++ {
		id := fmt.Sprintf("cam%d", i)
		if !used[id] {
			return id
		}
	}
	return ""
}

var idRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// runCameraDialog edits one camera. It validates before accepting, so a typo
// is caught here instead of coming back as an error from the service.
func runCameraDialog(owner walk.Form, cam *config.Camera) (bool, error) {
	var dlg *walk.Dialog
	var acceptPB, cancelPB *walk.PushButton
	var idEdit, nameEdit, urlEdit *walk.LineEdit
	var enabledCB *walk.CheckBox

	result, err := Dialog{
		AssignTo:      &dlg,
		Title:         "Камера",
		DefaultButton: &acceptPB,
		CancelButton:  &cancelPB,
		MinSize:       Size{Width: 520, Height: 230},
		Layout:        VBox{},
		Children: []Widget{
			Composite{
				Layout: Grid{Columns: 2},
				Children: []Widget{
					Label{Text: "Идентификатор:"},
					LineEdit{AssignTo: &idEdit, Text: cam.ID, CueBanner: "cam1"},

					Label{Text: "Название:"},
					LineEdit{AssignTo: &nameEdit, Text: cam.Name, CueBanner: "Камера 1"},

					Label{Text: "RTSP-адрес:"},
					LineEdit{AssignTo: &urlEdit, Text: cam.URL,
						CueBanner: "rtsp://user:pass@192.168.1.10:554/stream1"},

					Label{Text: ""},
					CheckBox{AssignTo: &enabledCB, Text: "Включена", Checked: cam.Enabled},
				},
			},
			VSpacer{},
			Composite{
				Layout: HBox{MarginsZero: true},
				Children: []Widget{
					HSpacer{},
					PushButton{
						AssignTo: &acceptPB,
						Text:     "ОК",
						MinSize:  Size{Width: 100},
						OnClicked: func() {
							id := strings.TrimSpace(idEdit.Text())
							name := strings.TrimSpace(nameEdit.Text())
							url := strings.TrimSpace(urlEdit.Text())

							switch {
							case !idRe.MatchString(id):
								walk.MsgBox(dlg, "Идентификатор",
									"Допустимы строчные латинские буквы, цифры, дефис и подчёркивание, до 32 символов.",
									walk.MsgBoxIconWarning)
								return
							case name == "":
								walk.MsgBox(dlg, "Название", "Укажите название камеры.", walk.MsgBoxIconWarning)
								return
							case !strings.HasPrefix(url, "rtsp://") && !strings.HasPrefix(url, "rtsps://"):
								walk.MsgBox(dlg, "RTSP-адрес",
									"Адрес должен начинаться с rtsp:// или rtsps://",
									walk.MsgBoxIconWarning)
								return
							}

							cam.ID, cam.Name, cam.URL = id, name, url
							cam.Enabled = enabledCB.Checked()
							dlg.Accept()
						},
					},
					PushButton{
						AssignTo:  &cancelPB,
						Text:      "Отмена",
						MinSize:   Size{Width: 100},
						OnClicked: func() { dlg.Cancel() },
					},
				},
			},
		},
	}.Run(owner)

	return result == walk.DlgCmdOK, err
}

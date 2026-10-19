package hub

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func recordedSettings(t *testing.T, o *Options, name string) Doc {
	t.Helper()
	inv, err := Load(o.InventoryPath)
	if err != nil {
		t.Fatal(err)
	}
	site, ok := inv.Find(name)
	if !ok {
		t.Fatalf("площадка %q пропала из инвентаря", name)
	}
	return site.Settings
}

func cameraByID(t *testing.T, doc Doc, id string) Doc {
	t.Helper()
	cams, err := doc.Cameras()
	if err != nil {
		t.Fatal(err)
	}
	i, ok := findCamera(cams, id)
	if !ok {
		return nil
	}
	return cams[i]
}

func TestPullRecordsSettings(t *testing.T) {
	a := newFakeAgent(t, false)

	var out strings.Builder
	o := testOptions(t, &out, a.site("sad5"))

	failures, err := Pull(o, Selection{All: true})
	if err != nil || failures != 0 {
		t.Fatalf("ошибок %d: %v", failures, err)
	}

	doc := recordedSettings(t, o, "sad5")
	if doc.String("domain") != "sad5.kidsroompf.ru" {
		t.Errorf("домен не записан: %v", doc["domain"])
	}
	if cam := cameraByID(t, doc, "cam1"); cam == nil || cam.String("name") != "Камера 1" {
		t.Errorf("камера не записана: %v", doc["cameras"])
	}
	// The site's own credential belongs to its inventory field, not to a second
	// copy inside settings.
	if _, present := doc["control"]; present {
		t.Error("блок control попал в записанные настройки")
	}
}

// The agent masks secrets, so a pull can never learn a camera password. What
// the inventory already holds must therefore survive a pull — otherwise one
// pull would erase the passwords that let the file rebuild a site from nothing.
func TestPullKeepsKnownSecrets(t *testing.T) {
	a := newFakeAgent(t, false)

	site := a.site("sad5")
	site.Settings = Doc{
		"domain": "sad5.kidsroompf.ru",
		"cameras": []any{
			map[string]any{"id": "cam1", "name": "Камера 1", "url": "rtsp://admin:realpass@10.0.0.5:554/s1", "enabled": true},
		},
	}

	var out strings.Builder
	o := testOptions(t, &out, site)

	failures, err := Pull(o, Selection{All: true})
	if err != nil {
		t.Fatal(err)
	}
	// Without this the test would pass by never reaching the write: a refused
	// update leaves the old file in place, which looks like success.
	if failures != 0 {
		t.Fatalf("pull не отработал:\n%s", out.String())
	}

	cam := cameraByID(t, recordedSettings(t, o, "sad5"), "cam1")
	if cam == nil {
		t.Fatal("камера пропала")
	}
	if got := cam.String("url"); got != "rtsp://admin:realpass@10.0.0.5:554/s1" {
		t.Errorf("пароль затёрт маской: %s", got)
	}
}

func TestPushSendsRecordedSettings(t *testing.T) {
	a := newFakeAgent(t, false)

	site := a.site("sad5")
	site.Settings = Doc{
		"domain": "restored.kidsroompf.ru",
		"cameras": []any{
			map[string]any{"id": "cam9", "name": "Коридор", "url": "rtsp://admin:pass@10.0.0.9:554/s", "enabled": true},
		},
	}

	var out strings.Builder
	o := testOptions(t, &out, site)

	failures, err := Push(o, Selection{All: true})
	if err != nil || failures != 0 {
		t.Fatalf("ошибок %d: %v", failures, err)
	}

	body := a.lastApplied(t)
	if body["domain"] != "restored.kidsroompf.ru" {
		t.Errorf("домен не отправлен: %v", body["domain"])
	}
	if ids := strings.Join(a.cameraIDs(), ","); ids != "cam9" {
		t.Errorf("камеры на площадке: %s, ожидалась cam9", ids)
	}
}

// A site the file knows nothing about must be left alone: the file not holding
// settings is not a statement that the site should have no cameras.
func TestPushSkipsSitesWithoutRecordedSettings(t *testing.T) {
	a := newFakeAgent(t, false)
	b := newFakeAgent(t, false)

	withSettings := a.site("sad5")
	withSettings.Settings = Doc{"domain": "sad5.kidsroompf.ru"}

	var out strings.Builder
	o := testOptions(t, &out, withSettings, b.site("sad7"))

	if _, err := Push(o, Selection{All: true}); err != nil {
		t.Fatal(err)
	}
	if a.appliedCount() != 1 {
		t.Errorf("площадка с настройками не получила push (%d)", a.appliedCount())
	}
	if b.appliedCount() != 0 {
		t.Error("площадка без записанных настроек получила запрос")
	}
	if !strings.Contains(out.String(), "пропуска") {
		t.Errorf("о пропуске не сказано:\n%s", out.String())
	}
}

func TestPushWithNothingRecorded(t *testing.T) {
	a := newFakeAgent(t, false)
	var out strings.Builder
	o := testOptions(t, &out, a.site("sad5"))

	if _, err := Push(o, Selection{All: true}); err == nil {
		t.Error("push без записанных настроек должен объяснять, что делать")
	}
	if a.appliedCount() != 0 {
		t.Error("на площадку ушёл пустой push")
	}
}

func TestDiffReportsDivergence(t *testing.T) {
	a := newFakeAgent(t, false) // домен sad5.kidsroompf.ru, одна камера cam1
	b := newFakeAgent(t, false)

	same := a.site("sad5")
	same.Settings = Doc{
		"domain": "sad5.kidsroompf.ru",
		"cameras": []any{
			map[string]any{"id": "cam1", "name": "Камера 1", "url": "rtsp://admin:***@10.0.0.5:554/s1", "enabled": true},
		},
		"hls":      map[string]any{"always_remux": true, "segment_duration": "1s"},
		"mediamtx": map[string]any{"hls_addr": "127.0.0.1:8888", "api_addr": "127.0.0.1:9997"},
	}

	differs := b.site("sad7")
	differs.Settings = Doc{"domain": "other.kidsroompf.ru"}

	var out strings.Builder
	o := testOptions(t, &out, same, differs)

	if _, err := Diff(o, Selection{All: true}); err != nil {
		t.Fatal(err)
	}

	text := out.String()
	if !strings.Contains(text, "sad5  совпадает") {
		t.Errorf("совпадающая площадка названа расходящейся:\n%s", text)
	}
	if !strings.Contains(text, "sad7  расходится: domain") {
		t.Errorf("расхождение по домену не найдено:\n%s", text)
	}
}

// A mask carries no information. If diff compared it to a secret it would
// report a difference on every run and the command would be worthless.
func TestDiffIgnoresMaskedValues(t *testing.T) {
	a := newFakeAgent(t, false)

	site := a.site("sad5")
	site.Settings = Doc{
		"domain": "sad5.kidsroompf.ru",
		"cameras": []any{
			// Real password here, mask on the agent's side: the same camera.
			map[string]any{"id": "cam1", "name": "Камера 1", "url": "rtsp://admin:realpass@10.0.0.5:554/s1", "enabled": true},
		},
	}

	var out strings.Builder
	o := testOptions(t, &out, site)

	if _, err := Diff(o, Selection{All: true}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "расходится") {
		t.Errorf("маска принята за расхождение:\n%s", out.String())
	}
}

func TestDiffSaysWhenNothingRecorded(t *testing.T) {
	a := newFakeAgent(t, false)
	var out strings.Builder
	o := testOptions(t, &out, a.site("sad5"))

	if _, err := Diff(o, Selection{All: true}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "pull") {
		t.Errorf("не сказано, как заполнить инвентарь:\n%s", out.String())
	}
}

// Every mutating command has to keep the file in step, or the first set-domain
// would make the inventory a lie.
func TestSetDomainRecordsIntoInventory(t *testing.T) {
	a := newFakeAgent(t, false)

	site := a.site("sad5")
	site.Settings = Doc{"domain": "old.kidsroompf.ru", "hls": map[string]any{"always_remux": true}}

	var out strings.Builder
	o := testOptions(t, &out, site)

	if _, err := SetDomain(o, Selection{All: true}, "fresh.kidsroompf.ru"); err != nil {
		t.Fatal(err)
	}

	doc := recordedSettings(t, o, "sad5")
	if doc.String("domain") != "fresh.kidsroompf.ru" {
		t.Errorf("в инвентаре домен %q", doc["domain"])
	}
	// Only the domain was asked for; the rest of the entry must survive.
	if hls, ok := doc["hls"].(map[string]any); !ok || hls["always_remux"] != true {
		t.Errorf("остальные настройки потерялись: %v", doc)
	}
}

func TestCameraCommandsRecordIntoInventory(t *testing.T) {
	a := newFakeAgent(t, false)

	site := a.site("sad5")
	site.Settings = Doc{
		"domain": "sad5.kidsroompf.ru",
		"cameras": []any{
			map[string]any{"id": "cam1", "name": "Камера 1", "url": "rtsp://admin:realpass@10.0.0.5:554/s1", "enabled": true},
		},
	}

	var out strings.Builder
	o := testOptions(t, &out, site)
	sel := Selection{All: true}

	failures, err := CameraAdd(o, sel, CameraSpec{ID: "cam2", Name: "Коридор", URL: "rtsp://admin:pass@10.0.0.9:554/s"})
	if err != nil {
		t.Fatal(err)
	}
	if failures != 0 {
		t.Fatalf("команда не отработала:\n%s", out.String())
	}

	doc := recordedSettings(t, o, "sad5")
	cams, err := doc.Cameras()
	if err != nil {
		t.Fatal(err)
	}
	if len(cams) != 2 {
		t.Fatalf("в инвентаре камер %d, ожидалось 2", len(cams))
	}
	// The password of the camera that was already there must not have been
	// replaced by the mask the agent would have answered with.
	if got := cameraByID(t, doc, "cam1").String("url"); got != "rtsp://admin:realpass@10.0.0.5:554/s1" {
		t.Errorf("пароль первой камеры испорчен: %s", got)
	}

	off := false
	if _, err := CameraSet(o, sel, "cam2", CameraSpec{Enabled: &off}); err != nil {
		t.Fatal(err)
	}
	if on, _ := cameraByID(t, recordedSettings(t, o, "sad5"), "cam2")["enabled"].(bool); on {
		t.Error("выключение камеры не записано в инвентарь")
	}

	if _, err := CameraRemove(o, sel, "cam1"); err != nil {
		t.Fatal(err)
	}
	if cameraByID(t, recordedSettings(t, o, "sad5"), "cam1") != nil {
		t.Error("удалённая камера осталась в инвентаре")
	}
}

// A patch has to be folded into the recorded settings the way the agent folds
// it into its own configuration, or the file would claim a block the site does
// not have and the next push would undo the rest of it.
func TestApplyMergesPatchIntoInventory(t *testing.T) {
	a := newFakeAgent(t, false)

	site := a.site("sad5")
	site.Settings = Doc{
		"domain": "sad5.kidsroompf.ru",
		"hls":    map[string]any{"always_remux": true, "segment_duration": "1s", "low_latency": false},
	}

	file := filepath.Join(t.TempDir(), "patch.json")
	if err := os.WriteFile(file, []byte(`{"hls":{"low_latency":true}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	o := testOptions(t, &out, site)

	if _, err := Apply(o, Selection{All: true}, file); err != nil {
		t.Fatal(err)
	}

	doc := recordedSettings(t, o, "sad5")
	hls, ok := doc["hls"].(map[string]any)
	if !ok {
		t.Fatalf("блок hls потерялся: %v", doc)
	}
	if hls["low_latency"] != true {
		t.Error("изменение не записано")
	}
	if hls["always_remux"] != true || hls["segment_duration"] != "1s" {
		t.Errorf("остальные поля блока затёрты: %v", hls)
	}
	if doc.String("domain") != "sad5.kidsroompf.ru" {
		t.Error("домен потерялся")
	}
}

// A site that refused the change must keep its old entry: otherwise a failed
// half of a fleet-wide change would silently become the recorded truth.
func TestFailedChangeIsNotRecorded(t *testing.T) {
	a := newFakeAgent(t, false)
	a.breakMethod(http.MethodPost, "/api/settings", http.StatusInternalServerError)

	site := a.site("sad5")
	site.Settings = Doc{"domain": "old.kidsroompf.ru"}

	var out strings.Builder
	o := testOptions(t, &out, site)

	failures, err := SetDomain(o, Selection{All: true}, "fresh.kidsroompf.ru")
	if err != nil {
		t.Fatal(err)
	}
	if failures != 1 {
		t.Fatalf("отказ площадки не отражён:\n%s", out.String())
	}
	if got := recordedSettings(t, o, "sad5").String("domain"); got != "old.kidsroompf.ru" {
		t.Errorf("в инвентаре записан непринятый домен: %s", got)
	}
}

func TestAddRecordsSettings(t *testing.T) {
	a := newFakeAgent(t, false)
	var out strings.Builder
	o := testOptions(t, &out)

	if err := Add(o, AddParams{Name: "sad5", Addr: a.addr(), Token: a.token}); err != nil {
		t.Fatal(err)
	}

	doc := recordedSettings(t, o, "sad5")
	if doc.String("domain") != "sad5.kidsroompf.ru" {
		t.Errorf("add не записал настройки: %v", doc)
	}
	if _, present := doc["control"]; present {
		t.Error("блок control попал в инвентарь")
	}
}

// Version 1 files held no settings at all; they must keep loading, and a pull
// fills them in.
func TestLoadUpgradesVersionOne(t *testing.T) {
	path := filepath.Join(t.TempDir(), "inventory.json")
	body := `{"version":1,"sites":[{"name":"sad5","addr":"https://10.0.0.1:8081","token":"t"}]}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	inv, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if inv.Version != InventoryVersion {
		t.Errorf("версия %d, ожидалась %d", inv.Version, InventoryVersion)
	}
	site, _ := inv.Find("sad5")
	if len(site.Settings) != 0 {
		t.Errorf("настройки взялись из ниоткуда: %v", site.Settings)
	}

	if err := inv.Save(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// An entry without settings must not grow an empty block.
	var check struct {
		Version int               `json:"version"`
		Sites   []json.RawMessage `json:"sites"`
	}
	if err := json.Unmarshal(raw, &check); err != nil {
		t.Fatal(err)
	}
	if check.Version != 2 {
		t.Errorf("записана версия %d", check.Version)
	}
	if strings.Contains(string(check.Sites[0]), "settings") {
		t.Errorf("пустой блок settings записан: %s", check.Sites[0])
	}
}

func TestSettingsValidationCatchesTypos(t *testing.T) {
	cases := map[string]string{
		"id с пробелом": `{"cameras":[{"id":"cam 1","name":"Х","url":"rtsp://10.0.0.5:554/s"}]}`,
		"дубликат id":   `{"cameras":[{"id":"cam1","name":"А","url":"rtsp://10.0.0.5:554/s"},{"id":"cam1","name":"Б","url":"rtsp://10.0.0.6:554/s"}]}`,
		"пустое имя":    `{"cameras":[{"id":"cam1","name":" ","url":"rtsp://10.0.0.5:554/s"}]}`,
		"не rtsp":       `{"cameras":[{"id":"cam1","name":"Х","url":"http://10.0.0.5/stream"}]}`,
		"не список":     `{"cameras":"одна"}`,
	}

	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			var doc Doc
			if err := json.Unmarshal([]byte(body), &doc); err != nil {
				t.Fatal(err)
			}
			site := Site{Name: "sad5", Addr: "https://10.0.0.1:8081", Token: "t", Settings: doc}
			if err := site.Validate(); err == nil {
				t.Error("принято без ошибки")
			}
		})
	}

	// A masked URL is what pull records for a password the file does not hold.
	// It is valid and means "keep what the agent has".
	var doc Doc
	if err := json.Unmarshal([]byte(`{"cameras":[{"id":"cam1","name":"Х","url":"rtsp://admin:***@10.0.0.5:554/s"}]}`), &doc); err != nil {
		t.Fatal(err)
	}
	site := Site{Name: "sad5", Addr: "https://10.0.0.1:8081", Token: "t", Settings: doc}
	if err := site.Validate(); err != nil {
		t.Errorf("маска в url отвергнута: %v", err)
	}
}

// Settings must not be shared between the stored inventory and a caller that
// edits them.
func TestFindReturnsIndependentCopy(t *testing.T) {
	inv := testInventory(t, Site{
		Name: "sad5", Addr: "https://10.0.0.1:8081", Token: "t",
		Settings: Doc{"domain": "sad5.kidsroompf.ru", "cameras": []any{
			map[string]any{"id": "cam1", "name": "Камера 1"},
		}},
	})

	site, _ := inv.Find("sad5")
	site.Settings["domain"] = "hijacked"
	if cams, _ := site.Settings.Cameras(); len(cams) > 0 {
		cams[0]["name"] = "подменена"
	}

	stored, _ := inv.Find("sad5")
	if stored.Settings.String("domain") != "sad5.kidsroompf.ru" {
		t.Error("правка копии изменила инвентарь")
	}
	if cams, _ := stored.Settings.Cameras(); len(cams) > 0 && cams[0].String("name") != "Камера 1" {
		t.Error("правка вложенной камеры изменила инвентарь")
	}
}

// An inventory entry may carry blocks the file does not own — left by an older
// build or typed in by hand. Sending them would be worse than useless: a stale
// control block could move the control listener or switch its TLS off and cut
// the hub off from the site it was configuring.
func TestStaleUnmanagedBlocksAreNotSent(t *testing.T) {
	a := newFakeAgent(t, false)

	site := a.site("sad5")
	site.Settings = Doc{
		"domain":  "sad5.kidsroompf.ru",
		"control": map[string]any{"addr": "0.0.0.0:9999", "tls": false},
		"tls":     map[string]any{"cert_pem": "старый", "key_pem": "***"},
	}

	var out strings.Builder
	o := testOptions(t, &out, site)

	failures, err := Push(o, Selection{All: true})
	if err != nil || failures != 0 {
		t.Fatalf("ошибок %d: %v", failures, err)
	}

	body := a.lastApplied(t)
	if _, present := body["control"]; present {
		t.Error("блок control отправлен на площадку")
	}
	if _, present := body["tls"]; present {
		t.Error("блок tls отправлен на площадку")
	}
	if body["domain"] != "sad5.kidsroompf.ru" {
		t.Errorf("домен не отправлен: %v", body["domain"])
	}
	if !strings.Contains(out.String(), "control/tls/web_dir") {
		t.Errorf("о пропуске блоков не сказано:\n%s", out.String())
	}
}

// And diff must not report them either: the file does not claim to own them, so
// a stale block would show every site as diverging forever.
func TestDiffIgnoresUnmanagedBlocks(t *testing.T) {
	a := newFakeAgent(t, false)

	site := a.site("sad5")
	site.Settings = Doc{
		"domain": "sad5.kidsroompf.ru",
		"cameras": []any{
			map[string]any{"id": "cam1", "name": "Камера 1", "url": "rtsp://admin:***@10.0.0.5:554/s1", "enabled": true},
		},
		"hls":      map[string]any{"always_remux": true, "segment_duration": "1s"},
		"mediamtx": map[string]any{"hls_addr": "127.0.0.1:8888", "api_addr": "127.0.0.1:9997"},
		"control":  map[string]any{"addr": "0.0.0.0:9999"},
		"tls":      map[string]any{"cert_pem": "старый"},
	}

	var out strings.Builder
	o := testOptions(t, &out, site)

	if _, err := Diff(o, Selection{All: true}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "совпадает") {
		t.Errorf("устаревшие блоки приняты за расхождение:\n%s", out.String())
	}
}

// TestWebDirIsNotPushedAcrossPlatforms: the fleet runs Windows and Linux agents
// side by side, and web_dir is a path on the site own disk. A pull from a
// Windows site records C:\ProgramData\kidsroom-agent\web; pushed to a Linux
// site that would make it serve the page out of a directory named after a
// Windows path, which the agent would dutifully create and fill with the
// factory page — a broken site that looks configured.
func TestWebDirIsNotPushedAcrossPlatforms(t *testing.T) {
	a := newFakeAgent(t, false)

	site := a.site("sad5")
	site.Settings = Doc{
		"domain":  "sad5.kidsroompf.ru",
		"web_dir": `C:\ProgramData\kidsroom-agent\web`,
	}

	var out strings.Builder
	o := testOptions(t, &out, site)

	failures, err := Push(o, Selection{All: true})
	if err != nil || failures != 0 {
		t.Fatalf("ошибок %d: %v", failures, err)
	}
	if body := a.lastApplied(t); body["web_dir"] != nil {
		t.Errorf("web_dir отправлен на площадку: %v", body["web_dir"])
	}
}

// And pull must not record it in the first place: the inventory is the source
// of truth for the whole fleet, and this field cannot be true for all of it.
func TestPullDoesNotRecordWebDir(t *testing.T) {
	a := newFakeAgent(t, false)

	var out strings.Builder
	o := testOptions(t, &out, a.site("sad5"))

	failures, err := Pull(o, Selection{All: true})
	if err != nil || failures != 0 {
		t.Fatalf("ошибок %d: %v", failures, err)
	}

	inv, err := o.inventory()
	if err != nil {
		t.Fatal(err)
	}
	stored, ok := inv.Find("sad5")
	if !ok {
		t.Fatal("площадка пропала из инвентаря")
	}
	if _, present := stored.Settings["web_dir"]; present {
		t.Errorf("web_dir записан в инвентарь: %v", stored.Settings["web_dir"])
	}
	if stored.Settings.String("domain") != "sad5.kidsroompf.ru" {
		t.Errorf("домен не записан: %v", stored.Settings["domain"])
	}
}

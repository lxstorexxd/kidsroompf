package hub

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestShowStatusTable(t *testing.T) {
	a := newFakeAgent(t, false)
	b := newFakeAgent(t, false)
	b.breakPath("/api/status", http.StatusForbidden)

	var out strings.Builder
	o := testOptions(t, &out, a.site("sad5", "msk"), b.site("sad7", "msk"))

	failures, err := ShowStatus(o, Selection{All: true})
	if err != nil {
		t.Fatal(err)
	}
	if failures != 1 {
		t.Errorf("ошибок %d, ожидалась одна", failures)
	}

	text := out.String()
	for _, want := range []string{"sad5", "ok", "1.0.0", "sad5.kidsroompf.ru", "1/1", "87 дн.", "sad7", "нет", "allow_cidrs"} {
		if !strings.Contains(text, want) {
			t.Errorf("в таблице нет %q:\n%s", want, text)
		}
	}
}

func TestShowStatusJSON(t *testing.T) {
	a := newFakeAgent(t, false)
	b := newFakeAgent(t, false)
	b.breakPath("/api/status", http.StatusUnauthorized)

	var out strings.Builder
	o := testOptions(t, &out, a.site("sad5"), b.site("sad7"))
	o.JSON = true

	if _, err := ShowStatus(o, Selection{All: true}); err != nil {
		t.Fatal(err)
	}

	var rows []struct {
		Site  string `json:"site"`
		OK    bool   `json:"ok"`
		Error string `json:"error"`
		Data  struct {
			Version string `json:"version"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out.String()), &rows); err != nil {
		t.Fatalf("вывод не разобрать как JSON: %v\n%s", err, out.String())
	}
	if len(rows) != 2 {
		t.Fatalf("строк %d", len(rows))
	}
	if !rows[0].OK || rows[0].Data.Version != "1.0.0" {
		t.Errorf("первая площадка: %+v", rows[0])
	}
	if rows[1].OK || rows[1].Error == "" {
		t.Errorf("вторая площадка должна прийти с ошибкой: %+v", rows[1])
	}
}

// The whole reason the agent's settings handler was fixed: a one-field change
// must travel as one field.
func TestSetDomainSendsMinimalPatch(t *testing.T) {
	a := newFakeAgent(t, false)

	var out strings.Builder
	o := testOptions(t, &out, a.site("sad5"))

	failures, err := SetDomain(o, Selection{Names: []string{"sad5"}}, "sad9.kidsroompf.ru")
	if err != nil || failures != 0 {
		t.Fatalf("ошибок %d: %v", failures, err)
	}

	body := a.lastApplied(t)
	if len(body) != 1 {
		t.Errorf("отправлено лишнее: %v", body)
	}
	if body["domain"] != "sad9.kidsroompf.ru" {
		t.Errorf("домен не тот: %v", body["domain"])
	}
}

func TestSetDomainRejectsNonsense(t *testing.T) {
	a := newFakeAgent(t, false)
	var out strings.Builder
	o := testOptions(t, &out, a.site("sad5"))

	// A Cyrillic name cannot be put into a certificate, so every site would
	// refuse it identically; the hub says so once, before anything is sent.
	for _, bad := range []string{"", "   ", "https://sad5.ru", "sad5.ru:8443", "два слова", "детская.рф"} {
		if _, err := SetDomain(o, Selection{All: true}, bad); err == nil {
			t.Errorf("домен %q принят", bad)
		}
	}
	if a.appliedCount() != 0 {
		t.Error("на площадку ушёл запрос с заведомо негодным доменом")
	}
}

func TestSetCertChecksDomainCoverage(t *testing.T) {
	a := newFakeAgent(t, false) // домен sad5.kidsroompf.ru
	b := newFakeAgent(t, false)
	b.setDomain("другой.example.ru")

	certFile, keyFile, _ := writeCert(t, "sad5.kidsroompf.ru")

	var out strings.Builder
	o := testOptions(t, &out, a.site("sad5"), b.site("sad7"))

	failures, err := SetCert(o, Selection{All: true}, certFile, keyFile, false)
	if err != nil {
		t.Fatal(err)
	}
	if failures != 1 {
		t.Errorf("ошибок %d, ожидалась одна (площадка с чужим доменом)", failures)
	}
	if a.appliedCount() != 1 {
		t.Errorf("своей площадке сертификат не доехал (запросов %d)", a.appliedCount())
	}
	if b.appliedCount() != 0 {
		t.Error("площадке с чужим доменом сертификат отправлен без --force")
	}
	if !strings.Contains(out.String(), "--force") {
		t.Errorf("в сообщении нет подсказки про --force:\n%s", out.String())
	}

	// With --force the operator gets what they asked for.
	out.Reset()
	if _, err := SetCert(o, Selection{Names: []string{"sad7"}}, certFile, keyFile, true); err != nil {
		t.Fatal(err)
	}
	if b.appliedCount() != 1 {
		t.Error("--force не отправил сертификат")
	}

	body := b.lastApplied(t)
	tlsBlock, ok := body["tls"].(map[string]any)
	if !ok || len(body) != 1 {
		t.Fatalf("тело запроса не то: %v", body)
	}
	if !strings.Contains(tlsBlock["cert_pem"].(string), "BEGIN CERTIFICATE") {
		t.Error("в теле нет сертификата")
	}
}

func TestSetCertRejectsBrokenPairBeforeSending(t *testing.T) {
	a := newFakeAgent(t, false)
	certFile, _, _ := writeCert(t, "sad5.kidsroompf.ru")
	_, otherKey, _ := writeCert(t, "sad5.kidsroompf.ru")

	var out strings.Builder
	o := testOptions(t, &out, a.site("sad5"))

	if _, err := SetCert(o, Selection{All: true}, certFile, otherKey, true); err == nil {
		t.Fatal("несовпадающая пара должна отвергаться")
	}
	if a.appliedCount() != 0 {
		t.Error("негодная пара ушла на площадку: так гасится вся сеть сразу")
	}
}

func TestSetCertMovesPin(t *testing.T) {
	a := newFakeAgent(t, true)
	certFile, keyFile, _ := writeCert(t, "sad5.kidsroompf.ru")
	pair, err := LoadCertPair(certFile, keyFile)
	if err != nil {
		t.Fatal(err)
	}

	// The pin has to start out correct, or the push could not reach the site at
	// all and there would be nothing to move.
	old := sha256.Sum256(a.cert().Raw)
	site := a.site("sad5")
	site.Fingerprint = hex.EncodeToString(old[:])

	var out strings.Builder
	o := testOptions(t, &out, site)

	// The stand keeps serving its own certificate, so the push itself is what
	// is under test here, not the handshake afterwards.
	if _, err := SetCert(o, Selection{All: true}, certFile, keyFile, true); err != nil {
		t.Fatal(err)
	}

	inv, err := Load(o.InventoryPath)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := inv.Find("sad5")
	if got.Fingerprint != pair.Fingerprint() {
		t.Errorf("отпечаток в инвентаре %q, ожидался %q", got.Fingerprint, pair.Fingerprint())
	}
}

func TestSetDomainClearsPin(t *testing.T) {
	a := newFakeAgent(t, true)

	sum := sha256.Sum256(a.cert().Raw)
	site := a.site("sad5")
	site.Fingerprint = hex.EncodeToString(sum[:])

	var out strings.Builder
	o := testOptions(t, &out, site)

	if _, err := SetDomain(o, Selection{All: true}, "sad9.kidsroompf.ru"); err != nil {
		t.Fatal(err)
	}

	inv, _ := Load(o.InventoryPath)
	got, _ := inv.Find("sad5")
	// A self-signed certificate is reissued for the new name, so the old pin
	// points at something that no longer exists.
	if got.Fingerprint != "" {
		t.Errorf("отпечаток остался закреплённым: %q", got.Fingerprint)
	}
}

func TestCameraAddSetRemove(t *testing.T) {
	a := newFakeAgent(t, false)
	var out strings.Builder
	o := testOptions(t, &out, a.site("sad5"))
	sel := Selection{All: true}

	if _, err := CameraAdd(o, sel, CameraSpec{ID: "cam2", Name: "Коридор", URL: "rtsp://admin:pass@10.0.0.9:554/s"}); err != nil {
		t.Fatal(err)
	}
	if ids := strings.Join(a.cameraIDs(), ","); ids != "cam1,cam2" {
		t.Fatalf("камеры на площадке: %s", ids)
	}
	// The camera that was already there keeps its masked password, which is
	// what tells the agent not to touch the stored one.
	if url, _ := a.camera("cam1")["url"].(string); !strings.Contains(url, Mask) {
		t.Errorf("маска первой камеры потеряна: %s", url)
	}
	if enabled, _ := a.camera("cam2")["enabled"].(bool); !enabled {
		t.Error("новая камера должна быть включена по умолчанию")
	}

	// A refusal by the site itself is reported per site, not as a command-level
	// error: with fifty sites the others still have to be dealt with.
	if failures, err := CameraAdd(o, sel, CameraSpec{ID: "cam2", Name: "Ещё раз", URL: "rtsp://10.0.0.9:554/s"}); err != nil || failures != 1 {
		t.Errorf("повторное добавление того же id: ошибок %d, err=%v", failures, err)
	}

	off := false
	if _, err := CameraSet(o, sel, "cam2", CameraSpec{Name: "Коридор 2", Enabled: &off}); err != nil {
		t.Fatal(err)
	}
	cam := a.camera("cam2")
	if cam["name"] != "Коридор 2" {
		t.Errorf("имя не изменилось: %v", cam["name"])
	}
	if enabled, _ := cam["enabled"].(bool); enabled {
		t.Error("камера должна была выключиться")
	}
	if url, _ := cam["url"].(string); url != "rtsp://admin:pass@10.0.0.9:554/s" {
		t.Errorf("url изменился, хотя его не просили менять: %s", url)
	}

	if _, err := CameraRemove(o, sel, "cam1"); err != nil {
		t.Fatal(err)
	}
	if ids := strings.Join(a.cameraIDs(), ","); ids != "cam2" {
		t.Errorf("после удаления: %s", ids)
	}
	if failures, err := CameraRemove(o, sel, "cam1"); err != nil || failures != 1 {
		t.Errorf("удаление несуществующей камеры: ошибок %d, err=%v", failures, err)
	}
}

func TestCameraAddValidatesLocally(t *testing.T) {
	a := newFakeAgent(t, false)
	var out strings.Builder
	o := testOptions(t, &out, a.site("sad5"))
	sel := Selection{All: true}

	cases := map[string]CameraSpec{
		"id с пробелом": {ID: "cam 3", Name: "Х", URL: "rtsp://10.0.0.9:554/s"},
		"пустое имя":    {ID: "cam3", Name: " ", URL: "rtsp://10.0.0.9:554/s"},
		"не rtsp":       {ID: "cam3", Name: "Х", URL: "http://10.0.0.9/stream"},
		"url без хоста": {ID: "cam3", Name: "Х", URL: "rtsp://"},
	}
	for name, spec := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := CameraAdd(o, sel, spec); err == nil {
				t.Error("принято без ошибки")
			}
		})
	}
	if a.appliedCount() != 0 {
		t.Error("негодная камера ушла на площадку")
	}
}

func TestCameraSetNeedsSomethingToChange(t *testing.T) {
	a := newFakeAgent(t, false)
	var out strings.Builder
	o := testOptions(t, &out, a.site("sad5"))

	if _, err := CameraSet(o, Selection{All: true}, "cam1", CameraSpec{}); err == nil {
		t.Error("команда без изменений должна подсказывать, какие флаги есть")
	}
}

func TestApplyFromFile(t *testing.T) {
	a := newFakeAgent(t, false)
	b := newFakeAgent(t, false)

	file := filepath.Join(t.TempDir(), "patch.json")
	if err := os.WriteFile(file, []byte(`{"hls":{"low_latency":true}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	o := testOptions(t, &out, a.site("sad5", "wave1"), b.site("sad7", "wave1"))

	failures, err := Apply(o, Selection{Tags: []string{"wave1"}}, file)
	if err != nil || failures != 0 {
		t.Fatalf("ошибок %d: %v", failures, err)
	}
	for name, agent := range map[string]*fakeAgent{"sad5": a, "sad7": b} {
		body := agent.lastApplied(t)
		hls, ok := body["hls"].(map[string]any)
		if !ok || hls["low_latency"] != true {
			t.Errorf("%s получила %v", name, body)
		}
		if len(body) != 1 {
			t.Errorf("%s: в теле лишние поля %v", name, body)
		}
	}
}

func TestApplyRejectsEmptyAndMissingFile(t *testing.T) {
	a := newFakeAgent(t, false)
	var out strings.Builder
	o := testOptions(t, &out, a.site("sad5"))

	empty := filepath.Join(t.TempDir(), "empty.json")
	if err := os.WriteFile(empty, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(o, Selection{All: true}, empty); err == nil {
		t.Error("пустой объект должен отвергаться: такой запрос ничего не делает")
	}
	if _, err := Apply(o, Selection{All: true}, filepath.Join(t.TempDir(), "нет.json")); err == nil {
		t.Error("отсутствующий файл должен давать ошибку")
	}
	if a.appliedCount() != 0 {
		t.Error("на площадку ушёл пустой запрос")
	}
}

func TestDryRunSendsNothing(t *testing.T) {
	a := newFakeAgent(t, false)
	var out strings.Builder
	o := testOptions(t, &out, a.site("sad5"))
	o.DryRun = true

	if _, err := SetDomain(o, Selection{All: true}, "sad9.kidsroompf.ru"); err != nil {
		t.Fatal(err)
	}
	if a.appliedCount() != 0 {
		t.Error("--dry-run отправил запрос")
	}
	if !strings.Contains(out.String(), "sad9.kidsroompf.ru") || !strings.Contains(out.String(), "dry-run") {
		t.Errorf("--dry-run должен показывать, что было бы отправлено:\n%s", out.String())
	}
}

func TestRestart(t *testing.T) {
	a := newFakeAgent(t, false)
	b := newFakeAgent(t, false)
	b.breakPath("/api/restart", http.StatusInternalServerError)

	var out strings.Builder
	o := testOptions(t, &out, a.site("sad5"), b.site("sad7"))

	failures, err := Restart(o, Selection{All: true})
	if err != nil {
		t.Fatal(err)
	}
	if failures != 1 {
		t.Errorf("ошибок %d, ожидалась одна", failures)
	}
	if a.restarts != 1 {
		t.Errorf("перезапусков на рабочей площадке: %d", a.restarts)
	}
	if !strings.Contains(out.String(), "отработали 1") {
		t.Errorf("в сводке нет числа удачных:\n%s", out.String())
	}
}

func TestAddChecksTheSiteAnswers(t *testing.T) {
	a := newFakeAgent(t, false)

	var out strings.Builder
	o := testOptions(t, &out)

	// Wrong token: the mistake has to surface now, not during a fleet-wide change.
	err := Add(o, AddParams{Name: "sad5", Addr: a.addr(), Token: "не тот"})
	if err == nil {
		t.Fatal("площадка с неверным токеном добавлена")
	}
	inv, _ := Load(o.InventoryPath)
	if len(inv.Sites) != 0 {
		t.Error("негодная площадка попала в инвентарь")
	}

	if err := Add(o, AddParams{Name: "sad5", Addr: a.addr(), Token: a.token, Tags: []string{"msk,wave1"}, Note: "Садик №5"}); err != nil {
		t.Fatal(err)
	}
	inv, _ = Load(o.InventoryPath)
	if len(inv.Sites) != 1 {
		t.Fatalf("площадок в инвентаре: %d", len(inv.Sites))
	}
	got := inv.Sites[0]
	if strings.Join(got.Tags, ",") != "msk,wave1" {
		t.Errorf("метки разобраны неверно: %v", got.Tags)
	}
	if !strings.Contains(out.String(), "1.0.0") || !strings.Contains(out.String(), "1/1") {
		t.Errorf("add должен сразу показывать состояние площадки:\n%s", out.String())
	}

	if err := Add(o, AddParams{Name: "sad5", Addr: a.addr(), Token: a.token}); err == nil {
		t.Error("повторное добавление должно отвергаться")
	}
}

func TestAddPinsFingerprint(t *testing.T) {
	a := newFakeAgent(t, true)
	var out strings.Builder
	o := testOptions(t, &out)

	if err := Add(o, AddParams{Name: "sad5", Addr: a.addr(), Token: a.token, Pin: true}); err != nil {
		t.Fatal(err)
	}
	inv, _ := Load(o.InventoryPath)
	got, _ := inv.Find("sad5")
	if len(got.Fingerprint) != 64 {
		t.Errorf("отпечаток не записан: %q", got.Fingerprint)
	}
}

func TestTokenRotateStoresNewToken(t *testing.T) {
	a := newFakeAgent(t, false)
	var out strings.Builder
	o := testOptions(t, &out, a.site("sad5"))

	if err := TokenRotate(o, "sad5"); err != nil {
		t.Fatal(err)
	}

	inv, _ := Load(o.InventoryPath)
	got, _ := inv.Find("sad5")
	if got.Token != "свежий-токен" {
		t.Fatalf("в инвентаре токен %q", got.Token)
	}
	// Printed as well: if the inventory write had failed, this is the only copy.
	if !strings.Contains(out.String(), "свежий-токен") {
		t.Errorf("новый токен не показан:\n%s", out.String())
	}

	// And the stored token must actually work: a rotation that left the
	// inventory holding the old value would cost access to the site.
	out.Reset()
	failures, err := ShowStatus(o, Selection{All: true})
	if err != nil {
		t.Fatal(err)
	}
	if failures != 0 {
		t.Errorf("после смены токена площадка недоступна:\n%s", out.String())
	}
}

func TestGetWritesFile(t *testing.T) {
	a := newFakeAgent(t, false)
	var out strings.Builder
	o := testOptions(t, &out, a.site("sad5"))

	file := filepath.Join(t.TempDir(), "sad5.json")
	if err := Get(o, "sad5", file); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var doc Doc
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("записан не JSON: %v", err)
	}
	if doc.String("domain") != "sad5.kidsroompf.ru" {
		t.Errorf("домен не тот: %v", doc["domain"])
	}
	// What comes out of get goes back into apply, so the mask has to be there.
	if !strings.Contains(string(raw), Mask) {
		t.Error("в выгрузке нет маски: значит в файле лежат секреты")
	}

	if err := Get(o, "sad99", ""); err == nil {
		t.Error("неизвестная площадка должна давать ошибку")
	}
}

func TestListHidesTokens(t *testing.T) {
	a := newFakeAgent(t, false)
	var out strings.Builder
	o := testOptions(t, &out, a.site("sad5", "msk"))

	if err := List(o); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), a.token) {
		t.Errorf("токен виден в списке:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "sad5") || !strings.Contains(out.String(), "msk") {
		t.Errorf("в списке нет площадки:\n%s", out.String())
	}
}

func TestRemoveLeavesAgentAlone(t *testing.T) {
	a := newFakeAgent(t, false)
	var out strings.Builder
	o := testOptions(t, &out, a.site("sad5"))

	if err := Remove(o, "sad5"); err != nil {
		t.Fatal(err)
	}
	inv, _ := Load(o.InventoryPath)
	if len(inv.Sites) != 0 {
		t.Error("площадка осталась в инвентаре")
	}
	if a.appliedCount() != 0 {
		t.Error("rm не должна ничего менять на самой площадке")
	}
}

// Changing more than one site without --yes must stop rather than guess, and it
// must say how to proceed.
func TestConfirmationRequiredForFleetChange(t *testing.T) {
	a := newFakeAgent(t, false)
	b := newFakeAgent(t, false)

	var out strings.Builder
	o := testOptions(t, &out, a.site("sad5"), b.site("sad7"))
	o.Yes = false

	_, err := SetDomain(o, Selection{All: true}, "common.example.ru")
	if err == nil {
		t.Fatal("изменение двух площадок прошло без подтверждения")
	}
	if !strings.Contains(err.Error(), "--yes") {
		t.Errorf("в ошибке нет подсказки: %v", err)
	}
	if a.appliedCount() != 0 || b.appliedCount() != 0 {
		t.Error("запрос ушёл на площадки без подтверждения")
	}
	if !strings.Contains(out.String(), "sad5, sad7") {
		t.Errorf("перед вопросом должен быть список площадок:\n%s", out.String())
	}

	// A single site needs no confirmation: that is the everyday case.
	out.Reset()
	if _, err := SetDomain(o, Selection{Names: []string{"sad5"}}, "sad5.example.ru"); err != nil {
		t.Fatalf("одна площадка не должна требовать подтверждения: %v", err)
	}
	if a.appliedCount() != 1 {
		t.Error("изменение одной площадки не применилось")
	}
}

func TestConfirmationAcceptsYes(t *testing.T) {
	a := newFakeAgent(t, false)
	b := newFakeAgent(t, false)

	var out strings.Builder
	o := testOptions(t, &out, a.site("sad5"), b.site("sad7"))
	o.Yes = true

	if _, err := Restart(o, Selection{All: true}); err != nil {
		t.Fatal(err)
	}
	if a.restarts != 1 || b.restarts != 1 {
		t.Errorf("перезапуски: %d и %d", a.restarts, b.restarts)
	}
}

func TestPinAndClear(t *testing.T) {
	a := newFakeAgent(t, true)
	var out strings.Builder
	o := testOptions(t, &out, a.site("sad5"))

	if _, err := Pin(o, Selection{All: true}, false); err != nil {
		t.Fatal(err)
	}
	inv, _ := Load(o.InventoryPath)
	got, _ := inv.Find("sad5")
	if len(got.Fingerprint) != 64 {
		t.Fatalf("отпечаток не закреплён: %q", got.Fingerprint)
	}

	o2 := DefaultOptions()
	o2.InventoryPath = o.InventoryPath
	o2.Out, o2.Err, o2.In = &out, &out, strings.NewReader("")
	if _, err := Pin(o2, Selection{All: true}, true); err != nil {
		t.Fatal(err)
	}
	inv, _ = Load(o.InventoryPath)
	got, _ = inv.Find("sad5")
	if got.Fingerprint != "" {
		t.Errorf("отпечаток не снят: %q", got.Fingerprint)
	}
}

func TestPinRefusesPlainHTTP(t *testing.T) {
	a := newFakeAgent(t, false)
	var out strings.Builder
	o := testOptions(t, &out, a.site("sad5"))

	failures, err := Pin(o, Selection{All: true}, false)
	if err != nil {
		t.Fatal(err)
	}
	if failures != 1 {
		t.Error("закреплять нечего, если площадка отвечает по http — это должно быть видно")
	}
}

func TestSetControlTLSFollowsTheScheme(t *testing.T) {
	// The stand starts with TLS on and is reached over https, exactly like a
	// freshly installed agent.
	a := newFakeAgent(t, true)

	sum := sha256.Sum256(a.cert().Raw)
	site := a.site("sad5")
	site.Fingerprint = hex.EncodeToString(sum[:])

	var out strings.Builder
	o := testOptions(t, &out, site)

	failures, err := SetControlTLS(o, Selection{All: true}, false)
	if err != nil {
		t.Fatal(err)
	}
	if failures != 0 {
		t.Fatalf("площадка не подтвердила переключение:\n%s", out.String())
	}

	inv, _ := Load(o.InventoryPath)
	got, _ := inv.Find("sad5")
	if !strings.HasPrefix(got.Addr, "http://") {
		t.Errorf("адрес в инвентаре остался %q — площадка была бы недостижима", got.Addr)
	}
	if got.Fingerprint != "" {
		t.Errorf("по http закреплять нечего, отпечаток должен быть снят: %q", got.Fingerprint)
	}

	body := a.lastApplied(t)
	control, ok := body["control"].(map[string]any)
	if !ok || control["tls"] != false {
		t.Errorf("тело запроса не то: %v", body)
	}
	if len(body) != 1 || len(control) != 1 {
		t.Errorf("отправлено лишнее: %v", body)
	}
}

// A site that cannot take the change must not be recorded as if it had. Writing
// a new scheme into the inventory for a site that never switched is how fifty
// sites become unreachable at once.
func TestSetControlTLSVerifiesTheChangeLanded(t *testing.T) {
	a := newFakeAgent(t, true)
	a.breakMethod(http.MethodPost, "/api/settings", http.StatusInternalServerError)

	sum := sha256.Sum256(a.cert().Raw)
	site := a.site("sad5")
	site.Fingerprint = hex.EncodeToString(sum[:])

	var out strings.Builder
	o := testOptions(t, &out, site)

	failures, err := SetControlTLS(o, Selection{All: true}, false)
	if err != nil {
		t.Fatal(err)
	}
	if failures != 1 {
		t.Fatalf("площадка отказала, но это не отражено:\n%s", out.String())
	}

	inv, _ := Load(o.InventoryPath)
	got, _ := inv.Find("sad5")
	if !strings.HasPrefix(got.Addr, "https://") {
		t.Errorf("адрес переписан, хотя площадка не переключилась: %q", got.Addr)
	}
	if got.Fingerprint != site.Fingerprint {
		t.Errorf("отпечаток снят, хотя сертификат не менялся: %q", got.Fingerprint)
	}
}

// --json must carry the agent's answer through untouched. A fleet tool that
// silently dropped fields its build does not model would hide exactly what a
// script was looking for on a site running a newer agent.
func TestStatusJSONKeepsUnknownAgentFields(t *testing.T) {
	a := newFakeAgent(t, false)
	a.mu.Lock()
	a.status["нечто_из_будущего"] = map[string]any{"значение": 42}
	a.mu.Unlock()

	var out strings.Builder
	o := testOptions(t, &out, a.site("sad5"))
	o.JSON = true

	if _, err := ShowStatus(o, Selection{All: true}); err != nil {
		t.Fatal(err)
	}

	var rows []struct {
		Site string         `json:"site"`
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(out.String()), &rows); err != nil {
		t.Fatalf("вывод не разобрать: %v\n%s", err, out.String())
	}
	if len(rows) != 1 {
		t.Fatalf("строк %d", len(rows))
	}
	if rows[0].Data["нечто_из_будущего"] == nil {
		t.Errorf("незнакомое поле агента потерялось: %v", rows[0].Data)
	}
	if rows[0].Data["version"] != "1.0.0" {
		t.Errorf("знакомые поля тоже должны быть на месте: %v", rows[0].Data)
	}
}

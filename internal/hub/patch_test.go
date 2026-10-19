package hub

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kidsroom-agent/internal/certs"
)

func settingsDoc(t *testing.T) Doc {
	t.Helper()
	// Shaped like what an agent actually answers: passwords masked, and a block
	// (mediamtx) the hub has no business touching.
	body := `{
	  "domain": "sad5.kidsroompf.ru",
	  "listen_port": 8443,
	  "cameras": [
	    {"id":"cam1","name":"Камера 1","url":"rtsp://admin:***@10.0.0.5:554/s1","enabled":true},
	    {"id":"cam2","name":"Камера 2","url":"rtsp://admin:***@10.0.0.6:554/s2","enabled":false}
	  ],
	  "mediamtx": {"hls_addr":"127.0.0.1:8888","api_addr":"127.0.0.1:9997"}
	}`
	var doc Doc
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func TestDocCameras(t *testing.T) {
	cams, err := settingsDoc(t).Cameras()
	if err != nil {
		t.Fatal(err)
	}
	if len(cams) != 2 {
		t.Fatalf("камер %d, ожидалось 2", len(cams))
	}
	if cams[0].String("id") != "cam1" || cams[1].String("name") != "Камера 2" {
		t.Errorf("камеры разобраны неверно: %+v", cams)
	}

	if i, ok := findCamera(cams, "cam2"); !ok || i != 1 {
		t.Errorf("поиск по id вернул %d, %v", i, ok)
	}
	if _, ok := findCamera(cams, "cam9"); ok {
		t.Error("найдена камера, которой нет")
	}
}

func TestDocCamerasRejectsWrongShape(t *testing.T) {
	var doc Doc
	if err := json.Unmarshal([]byte(`{"cameras":"одна"}`), &doc); err != nil {
		t.Fatal(err)
	}
	if _, err := doc.Cameras(); err == nil {
		t.Error("строка вместо списка камер должна быть ошибкой")
	}

	empty := Doc{}
	cams, err := empty.Cameras()
	if err != nil || cams != nil {
		t.Errorf("отсутствие поля cameras — не ошибка: %v, %v", cams, err)
	}
}

// The mask has to travel back untouched: that is what tells the agent to keep
// the stored password, so editing one camera never needs the others' credentials.
func TestCamerasPatchKeepsMask(t *testing.T) {
	cams, err := settingsDoc(t).Cameras()
	if err != nil {
		t.Fatal(err)
	}
	cams[0]["name"] = "Детская"

	body, err := camerasPatch(cams)
	if err != nil {
		t.Fatal(err)
	}

	var sent struct {
		Cameras []struct {
			ID      string `json:"id"`
			Name    string `json:"name"`
			URL     string `json:"url"`
			Enabled bool   `json:"enabled"`
		} `json:"cameras"`
	}
	if err := json.Unmarshal(body, &sent); err != nil {
		t.Fatal(err)
	}
	if len(sent.Cameras) != 2 {
		t.Fatalf("отправлено камер: %d", len(sent.Cameras))
	}
	if !strings.Contains(sent.Cameras[1].URL, Mask) {
		t.Errorf("маска во второй камере потеряна: %s", sent.Cameras[1].URL)
	}
	if sent.Cameras[0].Name != "Детская" {
		t.Errorf("правка не попала в тело: %+v", sent.Cameras[0])
	}
	if sent.Cameras[1].Enabled {
		t.Error("выключенная камера приехала включённой")
	}

	// Only the camera list is sent, so nothing else on the site can be clobbered
	// by a stale value the hub happened to read a moment earlier.
	var top map[string]json.RawMessage
	if err := json.Unmarshal(body, &top); err != nil {
		t.Fatal(err)
	}
	if len(top) != 1 {
		t.Errorf("в теле лишние поля: %v", top)
	}
}

func writeCert(t *testing.T, domain string) (certFile, keyFile string, info certs.Info) {
	t.Helper()
	certPEM, keyPEM, err := certs.GenerateSelfSigned(domain)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certFile = filepath.Join(dir, "cert.pem")
	keyFile = filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certFile, []byte(certPEM), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, []byte(keyPEM), 0o600); err != nil {
		t.Fatal(err)
	}

	pair, err := certs.LoadPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	return certFile, keyFile, certs.Describe(pair)
}

func TestLoadCertPair(t *testing.T) {
	certFile, keyFile, info := writeCert(t, "sad5.kidsroompf.ru")

	pair, err := LoadCertPair(certFile, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	if pair.Info.Subject != info.Subject {
		t.Errorf("subject %q, ожидался %q", pair.Info.Subject, info.Subject)
	}
	if err := pair.Covers("sad5.kidsroompf.ru"); err != nil {
		t.Errorf("свой домен не покрыт: %v", err)
	}
	// This is the check that keeps the wrong file from reaching fifty servers.
	if err := pair.Covers("sad7.kidsroompf.ru"); err == nil {
		t.Error("чужой домен признан покрытым")
	}
	if err := pair.Covers(""); err == nil {
		t.Error("пустой домен должен давать внятную ошибку")
	}
	if pair.Fingerprint() == "" || len(pair.Fingerprint()) != 64 {
		t.Errorf("отпечаток выглядит неверно: %q", pair.Fingerprint())
	}
}

func TestLoadCertPairRejectsMismatch(t *testing.T) {
	certFile, _, _ := writeCert(t, "sad5.kidsroompf.ru")
	_, otherKey, _ := writeCert(t, "sad7.kidsroompf.ru")

	if _, err := LoadCertPair(certFile, otherKey); err == nil {
		t.Error("ключ от другого сертификата должен отвергаться здесь, до отправки")
	}
	if _, err := LoadCertPair(filepath.Join(t.TempDir(), "нет.pem"), otherKey); err == nil {
		t.Error("отсутствующий файл должен давать ошибку")
	}
}

func TestCertPatchCarriesBothHalves(t *testing.T) {
	certFile, keyFile, _ := writeCert(t, "sad5.kidsroompf.ru")
	pair, err := LoadCertPair(certFile, keyFile)
	if err != nil {
		t.Fatal(err)
	}

	body, err := pair.Patch()
	if err != nil {
		t.Fatal(err)
	}
	var sent struct {
		TLS struct {
			Cert string `json:"cert_pem"`
			Key  string `json:"key_pem"`
		} `json:"tls"`
	}
	if err := json.Unmarshal(body, &sent); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sent.TLS.Cert, "BEGIN CERTIFICATE") || !strings.Contains(sent.TLS.Key, "PRIVATE KEY") {
		t.Error("в теле нет полной пары PEM")
	}
}

func TestReadPatchFile(t *testing.T) {
	dir := t.TempDir()

	ok := filepath.Join(dir, "patch.json")
	if err := os.WriteFile(ok, append([]byte("\xef\xbb\xbf"), `{"hls":{"low_latency":true}}`...), 0o600); err != nil {
		t.Fatal(err)
	}
	raw, doc, err := ReadPatchFile(ok)
	if err != nil {
		t.Fatalf("файл с BOM не прочитан: %v", err)
	}
	if len(doc) != 1 || doc["hls"] == nil {
		t.Errorf("разобрано неверно: %v", doc)
	}
	if strings.HasPrefix(string(raw), "\xef") {
		t.Error("BOM должен быть снят перед отправкой")
	}

	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte(`["домен"]`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadPatchFile(bad); err == nil {
		t.Error("список вместо объекта должен отвергаться")
	}
}

func TestDocSection(t *testing.T) {
	d := Doc{}
	d.Section("control")["tls"] = false

	body, err := d.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != `{"control":{"tls":false}}` {
		t.Errorf("получено %s", body)
	}

	// An existing section is reused rather than replaced.
	again := settingsDoc(t)
	again.Section("mediamtx")["hls_addr"] = "127.0.0.1:18888"
	mtx := again.Section("mediamtx")
	if mtx.String("api_addr") != "127.0.0.1:9997" {
		t.Errorf("соседнее поле блока потеряно: %v", mtx)
	}
}

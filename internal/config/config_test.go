package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func validConfig() *Config {
	c := Default()
	c.Control.Token = "t0ken"
	c.Cameras = []Camera{
		{ID: "cam1", Name: "Камера 1", URL: "rtsp://admin:secret@10.0.0.5:554/stream1", Enabled: true},
	}
	return c
}

func TestValidateAcceptsDefaults(t *testing.T) {
	if err := validConfig().Validate(); err != nil {
		t.Fatalf("корректная конфигурация отклонена: %v", err)
	}
}

func TestValidateRejects(t *testing.T) {
	cases := map[string]func(*Config){
		"порт вне диапазона":    func(c *Config) { c.ListenPort = 70000 },
		"пустой web_dir":        func(c *Config) { c.WebDir = "" },
		"домен со схемой":       func(c *Config) { c.Domain = "https://nursery.ru" },
		"id с пробелом":         func(c *Config) { c.Cameras[0].ID = "cam 1" },
		"id в верхнем регистре": func(c *Config) { c.Cameras[0].ID = "Cam1" },
		"пустое имя":            func(c *Config) { c.Cameras[0].Name = " " },
		"не rtsp":               func(c *Config) { c.Cameras[0].URL = "http://10.0.0.5/stream" },
		"битый CIDR":            func(c *Config) { c.Control.AllowCIDRs = []string{"10.0.0.0/99"} },
		"адрес без порта":       func(c *Config) { c.Control.Addr = "0.0.0.0" },
		"длительность":          func(c *Config) { c.HLS.SegmentDuration = "быстро" },
		"ключ без сертификата":  func(c *Config) { c.TLS.KeyPEM = "x" },
	}

	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			c := validConfig()
			mutate(c)
			if err := c.Validate(); err == nil {
				t.Fatal("ожидалась ошибка валидации, её нет")
			}
		})
	}
}

func TestValidateRejectsDuplicateID(t *testing.T) {
	c := validConfig()
	c.Cameras = append(c.Cameras, Camera{ID: "cam1", Name: "Камера 2",
		URL: "rtsp://10.0.0.6:554/s", Enabled: true})

	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "дубл") {
		t.Fatalf("дубликат id не пойман: %v", err)
	}
}

// A token is optional on loopback, where only local processes can reach the
// API, and mandatory as soon as the API is exposed to the network.
func TestTokenRequiredOffLoopback(t *testing.T) {
	c := validConfig()
	c.Control.Token = ""

	c.Control.Addr = "127.0.0.1:8081"
	if err := c.Validate(); err != nil {
		t.Fatalf("на loopback токен не обязателен: %v", err)
	}

	c.Control.Addr = "0.0.0.0:8081"
	if err := c.Validate(); err == nil {
		t.Fatal("без токена на 0.0.0.0 конфигурация должна отвергаться")
	}
}

func TestControlIsLoopbackOnly(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1:8081": true,
		"localhost:8081": true,
		"[::1]:8081":     true,
		"0.0.0.0:8081":   false,
		"10.8.0.5:8081":  false,
		":8081":          false,
	}
	for addr, want := range cases {
		c := validConfig()
		c.Control.Addr = addr
		if got := c.ControlIsLoopbackOnly(); got != want {
			t.Errorf("%s: получено %v, ожидалось %v", addr, got, want)
		}
	}
}

func TestMaskURLPassword(t *testing.T) {
	cases := map[string]string{
		"rtsp://admin:secret@10.0.0.5:554/s1": "rtsp://admin:***@10.0.0.5:554/s1",
		"rtsp://admin@10.0.0.5:554/s1":        "rtsp://admin@10.0.0.5:554/s1",
		"rtsp://10.0.0.5:554/s1":              "rtsp://10.0.0.5:554/s1",
	}
	for in, want := range cases {
		if got := MaskURLPassword(in); got != want {
			t.Errorf("%s -> %s, ожидалось %s", in, got, want)
		}
	}
}

func TestMaskedHidesSecrets(t *testing.T) {
	c := validConfig()
	c.TLS = TLSConfig{CertPEM: "cert", KeyPEM: "key"}

	m := c.Masked()
	if strings.Contains(m.Cameras[0].URL, "secret") {
		t.Error("пароль камеры попал в маскированный конфиг")
	}
	if m.TLS.KeyPEM != Mask || m.Control.Token != Mask {
		t.Error("ключ или токен не замаскированы")
	}
	if m.TLS.CertPEM != "cert" {
		t.Error("сертификат — не секрет, он должен остаться как есть")
	}
	if c.Cameras[0].URL != "rtsp://admin:secret@10.0.0.5:554/stream1" {
		t.Error("Masked изменил исходную конфигурацию")
	}
}

// A client reads the masked settings, edits a name and posts them back; the
// stored password and token must survive that round trip.
func TestMergeKeepsSecretsOnRoundTrip(t *testing.T) {
	current := validConfig()
	current.TLS = TLSConfig{CertPEM: "cert", KeyPEM: "key"}

	incoming := current.Masked()
	incoming.Cameras[0].Name = "Детская"

	next := Merge(current, incoming)

	if next.Cameras[0].URL != current.Cameras[0].URL {
		t.Errorf("пароль не восстановлен: %s", next.Cameras[0].URL)
	}
	if next.Cameras[0].Name != "Детская" {
		t.Error("изменение имени потеряно")
	}
	if next.TLS.KeyPEM != "key" {
		t.Error("приватный ключ затёрт маской")
	}
	if next.Control.Token != current.Control.Token {
		t.Error("токен должен меняться только своим эндпоинтом")
	}
}

// The percent-encoded form is what older clients may echo back.
func TestMergeAcceptsEncodedMask(t *testing.T) {
	current := validConfig()
	incoming := current.Clone()
	incoming.Cameras[0].URL = "rtsp://admin:%2A%2A%2A@10.0.0.5:554/stream1"

	if got := Merge(current, incoming).Cameras[0].URL; got != current.Cameras[0].URL {
		t.Errorf("пароль не восстановлен из %%2A-формы: %s", got)
	}
}

func TestMergeAppliesNewPassword(t *testing.T) {
	current := validConfig()
	incoming := current.Clone()
	incoming.Cameras[0].URL = "rtsp://admin:fresh@10.0.0.5:554/stream1"

	if got := Merge(current, incoming).Cameras[0].URL; got != incoming.Cameras[0].URL {
		t.Errorf("новый пароль не применён: %s", got)
	}
}

func TestMergeFillsOmittedFields(t *testing.T) {
	current := validConfig()
	incoming := &Config{Cameras: current.Cameras}

	next := Merge(current, incoming)
	if next.ListenPort != current.ListenPort {
		t.Error("порт должен остаться прежним, если не прислан")
	}
	if next.WebDir != current.WebDir {
		t.Error("web_dir должен остаться прежним, если не прислан")
	}
	if len(next.Control.AllowCIDRs) != len(current.Control.AllowCIDRs) {
		t.Error("список подсетей должен остаться прежним, если не прислан")
	}
}

func TestEnabledCameras(t *testing.T) {
	c := validConfig()
	c.Cameras = append(c.Cameras, Camera{ID: "cam2", Name: "Камера 2",
		URL: "rtsp://10.0.0.6:554/s", Enabled: false})

	if got := c.EnabledCameras(); len(got) != 1 || got[0].ID != "cam1" {
		t.Errorf("ожидалась одна включённая камера, получено %v", got)
	}
}

func TestNewTokenIsUnique(t *testing.T) {
	a, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := NewToken()
	if a == b || len(a) < 32 {
		t.Fatalf("токен предсказуем или слишком короткий: %q", a)
	}
}

// Editing config.json in Notepad or writing it with PowerShell's Out-File
// leaves a UTF-8 BOM, which the JSON parser rejects with a message about a
// character the operator never typed.
func TestLoadAcceptsBOM(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	body := `{"listen_port":9443,"domain":"nursery.test"}`
	if err := os.WriteFile(path, append([]byte("\xef\xbb\xbf"), body...), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, _, err := Load(path)
	if err != nil {
		t.Fatalf("конфиг с BOM не прочитан: %v", err)
	}
	if cfg.ListenPort != 9443 || cfg.Domain != "nursery.test" {
		t.Errorf("значения потерялись: port=%d domain=%q", cfg.ListenPort, cfg.Domain)
	}
}

// A configuration written before the mediamtx block existed must come back
// with working addresses rather than empty ones.
func TestLoadFillsMissingMediamtxAddrs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"listen_port":8443}`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, dirty, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !dirty {
		t.Error("заполнение пропусков должно помечать конфиг как требующий сохранения")
	}
	if cfg.Mediamtx.HLSAddr == "" || cfg.Mediamtx.APIAddr == "" {
		t.Errorf("адреса mediamtx не заполнены: %+v", cfg.Mediamtx)
	}
}

// Not every camera puts its credentials in the userinfo. A good part of the
// fleet is addressed as rtsp://host/user=admin&password=secret&channel=1?.sdp,
// and a password that is not masked here is one handed out by
// GET /api/settings and shown in the settings window.
func TestMaskURLPasswordCoversParameterForm(t *testing.T) {
	cases := map[string]string{
		// Как у настоящих камер: параметры лежат в пути, а запрос — это "?.sdp".
		"rtsp://192.168.26.63/user=admin&password=cam7_giS&channel=1&stream=0?.sdp": "rtsp://192.168.26.63/user=admin&password=***&channel=1&stream=0?.sdp",
		"rtsp://10.0.0.5:554/stream?user=admin&pwd=secret":                          "rtsp://10.0.0.5:554/stream?user=admin&pwd=***",
		"rtsp://10.0.0.5/PASSWORD=Secret&channel=1":                                 "rtsp://10.0.0.5/PASSWORD=***&channel=1",
		// И оба вида сразу.
		"rtsp://admin:outer@10.0.0.5/password=inner&x=1": "rtsp://admin:***@10.0.0.5/password=***&x=1",
		// Не пароль: имя лишь заканчивается на pass.
		"rtsp://10.0.0.5/bypass=1&channel=2": "rtsp://10.0.0.5/bypass=1&channel=2",
		// Логин секретом не считается.
		"rtsp://10.0.0.5/user=admin&channel=1": "rtsp://10.0.0.5/user=admin&channel=1",
	}
	for in, want := range cases {
		if got := MaskURLPassword(in); got != want {
			t.Errorf("\n  вход:     %s\n  получено: %s\n  ожидалось: %s", in, got, want)
		}
	}
}

// Masking without restoring would be worse than not masking at all: reading the
// settings and sending them back is what the settings window and the fleet tool
// both do, and the camera would come back with *** as its password.
func TestMergeRestoresParameterFormPassword(t *testing.T) {
	stored := "rtsp://192.168.26.63/user=admin&password=cam7_giS&channel=1&stream=0?.sdp"

	current := validConfig()
	current.Cameras = []Camera{{ID: "camera1", Name: "Камера 1", URL: stored, Enabled: true}}

	incoming := current.Masked()
	if !strings.Contains(incoming.Cameras[0].URL, Mask) {
		t.Fatalf("пароль не замаскирован: %s", incoming.Cameras[0].URL)
	}
	incoming.Cameras[0].Name = "Детская"

	next := Merge(current, incoming)
	if next.Cameras[0].URL != stored {
		t.Errorf("пароль не восстановлен:\n  получено: %s\n  ожидалось: %s", next.Cameras[0].URL, stored)
	}
	if next.Cameras[0].Name != "Детская" {
		t.Error("изменение имени потеряно")
	}
}

func TestMergeAppliesNewParameterFormPassword(t *testing.T) {
	current := validConfig()
	current.Cameras = []Camera{{
		ID: "camera1", Name: "Камера 1", Enabled: true,
		URL: "rtsp://192.168.26.63/user=admin&password=old&channel=1",
	}}

	incoming := current.Clone()
	incoming.Cameras[0].URL = "rtsp://192.168.26.63/user=admin&password=new&channel=1"

	if got := Merge(current, incoming).Cameras[0].URL; got != incoming.Cameras[0].URL {
		t.Errorf("новый пароль не применён: %s", got)
	}
}

// A camera whose stored URL has no password must not pick one up from the mask
// machinery.
func TestMergeLeavesMaskAloneWithoutStoredPassword(t *testing.T) {
	current := validConfig()
	current.Cameras = []Camera{{ID: "camera1", Name: "Камера 1", Enabled: true,
		URL: "rtsp://192.168.26.63/channel=1"}}

	incoming := current.Clone()
	incoming.Cameras[0].URL = "rtsp://192.168.26.63/password=***&channel=1"

	got := Merge(current, incoming).Cameras[0].URL
	if got != "rtsp://192.168.26.63/password=***&channel=1" {
		t.Errorf("получено %s", got)
	}
}

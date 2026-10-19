package control

import (
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"kidsroom-agent/internal/config"
)

func TestAllowedCIDRs(t *testing.T) {
	cidrs := config.Default().Control.AllowCIDRs

	cases := map[string]bool{
		"127.0.0.1":    true,
		"10.8.0.5":     true, // типичная VPN-подсеть
		"192.168.7.69": true,
		"172.16.4.1":   true,
		"8.8.8.8":      false,
		"172.32.0.1":   false, // вне 172.16.0.0/12
		"203.0.113.9":  false,
	}
	for ip, want := range cases {
		if got := allowed(net.ParseIP(ip), cidrs); got != want {
			t.Errorf("%s: получено %v, ожидалось %v", ip, got, want)
		}
	}
}

func TestCheckToken(t *testing.T) {
	req := func(header string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/api/status", nil)
		if header != "" {
			r.Header.Set("Authorization", header)
		}
		return r
	}

	if !checkToken(req("Bearer secret"), "secret") {
		t.Error("правильный токен отвергнут")
	}
	if !checkToken(req("bearer secret"), "secret") {
		t.Error("схема должна сравниваться без учёта регистра")
	}
	for _, header := range []string{"", "Bearer wrong", "secret", "Basic secret", "Bearer "} {
		if checkToken(req(header), "secret") {
			t.Errorf("принят неверный заголовок %q", header)
		}
	}
	// An empty stored token must never authorise anything.
	if checkToken(req("Bearer "), "") {
		t.Error("пустой токен не должен никого пускать")
	}
}

// X-Forwarded-For must be ignored: trusting it would let anyone claim an
// allowed subnet.
func TestRemoteIPIgnoresForwardedHeader(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	r.RemoteAddr = "203.0.113.9:5555"
	r.Header.Set("X-Forwarded-For", "10.0.0.1")

	if got := remoteIP(r); got.String() != "203.0.113.9" {
		t.Errorf("получен %v, ожидался реальный адрес 203.0.113.9", got)
	}
}

func TestThrottleBlocksAfterLimit(t *testing.T) {
	th := newThrottle(3, time.Minute)

	for i := 0; i < 3; i++ {
		if !th.allow("10.0.0.1") {
			t.Fatalf("попытка %d заблокирована раньше лимита", i+1)
		}
		th.fail("10.0.0.1")
	}
	if th.allow("10.0.0.1") {
		t.Error("после трёх неудач адрес должен быть заблокирован")
	}
	if !th.allow("10.0.0.2") {
		t.Error("блокировка не должна распространяться на другие адреса")
	}

	// A success clears the counter.
	th.ok("10.0.0.1")
	if !th.allow("10.0.0.1") {
		t.Error("успешная авторизация должна снимать блокировку")
	}
}

func TestThrottleExpires(t *testing.T) {
	th := newThrottle(1, 10*time.Millisecond)
	th.fail("10.0.0.1")

	if th.allow("10.0.0.1") {
		t.Fatal("адрес должен быть заблокирован сразу после неудачи")
	}
	time.Sleep(20 * time.Millisecond)
	if !th.allow("10.0.0.1") {
		t.Error("блокировка должна истекать по окну")
	}
}

func deps(cfg *config.Config) Deps {
	return Deps{
		Settings:        func() *config.Config { return cfg },
		Apply:           func(*config.Config) error { return nil },
		Status:          func() any { return map[string]string{"ok": "yes"} },
		Restart:         func() {},
		RegenerateToken: func() (string, error) { return "new", nil },
		Cert:            func() *tls.Certificate { return nil },
		Logf:            func(string, ...any) {},
	}
}

func TestGuardRequiresTokenOffLoopback(t *testing.T) {
	cfg := config.Default()
	cfg.Control.Token = "secret"
	s := New(deps(cfg))
	handler := s.handler()

	call := func(remote, header string) int {
		r := httptest.NewRequest(http.MethodGet, "/api/status", nil)
		r.RemoteAddr = remote
		if header != "" {
			r.Header.Set("Authorization", header)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, r)
		return rec.Code
	}

	if got := call("10.8.0.5:4000", ""); got != http.StatusUnauthorized {
		t.Errorf("без токена из сети: код %d, ожидался 401", got)
	}
	if got := call("10.8.0.6:4000", "Bearer secret"); got != http.StatusOK {
		t.Errorf("с токеном из сети: код %d, ожидался 200", got)
	}
	if got := call("8.8.8.8:4000", "Bearer secret"); got != http.StatusForbidden {
		t.Errorf("из чужой подсети: код %d, ожидался 403", got)
	}
	// Local processes (tray, settings window) need no token.
	if got := call("127.0.0.1:4000", ""); got != http.StatusOK {
		t.Errorf("с loopback без токена: код %d, ожидался 200", got)
	}
}

// The token is handed out locally only; over the network it is something you
// present, not something you can ask for.
func TestTokenEndpointIsLoopbackOnly(t *testing.T) {
	cfg := config.Default()
	cfg.Control.Token = "secret"
	handler := New(deps(cfg)).handler()

	r := httptest.NewRequest(http.MethodGet, "/api/token", nil)
	r.RemoteAddr = "10.8.0.5:4000"
	r.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, r)

	if rec.Code != http.StatusForbidden {
		t.Errorf("код %d, ожидался 403", rec.Code)
	}
}

// A settings document carries only what the caller wants changed. Everything it
// leaves out must survive, or a fleet-wide certificate push would take the
// cameras, the domain and the API's own TLS down with it.
func TestDecodeSettingsKeepsOmittedFields(t *testing.T) {
	current := config.Default()
	current.Domain = "sad5.kidsroompf.ru"
	current.Control.Token = "secret"
	current.Cameras = []config.Camera{
		{ID: "cam1", Name: "Камера 1", URL: "rtsp://admin:pass@10.0.0.5:554/s1", Enabled: true},
	}

	next, err := decodeSettings(current, []byte(`{"tls":{"cert_pem":"c","key_pem":"k"}}`))
	if err != nil {
		t.Fatal(err)
	}

	if next.Domain != current.Domain {
		t.Errorf("домен затёрт: %q", next.Domain)
	}
	if len(next.Cameras) != 1 || next.Cameras[0].URL != current.Cameras[0].URL {
		t.Errorf("список камер не сохранён: %+v", next.Cameras)
	}
	if !next.HLS.AlwaysRemux {
		t.Error("hls.always_remux сброшен в false")
	}
	if !next.Control.TLS {
		t.Error("control.tls сброшен в false — API остался бы без шифрования")
	}
	if next.ListenPort != current.ListenPort || next.Mediamtx.HLSAddr != current.Mediamtx.HLSAddr {
		t.Error("порт или адреса mediamtx потерялись")
	}
	if next.TLS.CertPEM != "c" || next.TLS.KeyPEM != "k" {
		t.Error("присланный сертификат не применён")
	}
}

// A false that the body states explicitly must be applied; only absence means
// "keep".
func TestDecodeSettingsAppliesExplicitFalse(t *testing.T) {
	current := config.Default()

	next, err := decodeSettings(current, []byte(`{"hls":{"always_remux":false}}`))
	if err != nil {
		t.Fatal(err)
	}
	if next.HLS.AlwaysRemux {
		t.Error("явный false не применён")
	}
	if next.HLS.SegmentDuration != current.HLS.SegmentDuration {
		t.Error("соседнее поле того же блока потерялось")
	}
}

// A camera list replaces the stored one outright. Decoding it over the stored
// slice would let a new camera inherit the name and URL of whatever used to
// occupy its position.
func TestDecodeSettingsReplacesCameraList(t *testing.T) {
	current := config.Default()
	current.Cameras = []config.Camera{
		{ID: "cam1", Name: "Камера 1", URL: "rtsp://admin:pass@10.0.0.5:554/s1", Enabled: true},
		{ID: "cam2", Name: "Камера 2", URL: "rtsp://admin:pass@10.0.0.6:554/s2", Enabled: true},
	}

	next, err := decodeSettings(current, []byte(`{"cameras":[{"id":"cam9","name":"Коридор","url":"rtsp://10.0.0.9:554/s"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Cameras) != 1 {
		t.Fatalf("камер стало %d, ожидалась одна", len(next.Cameras))
	}
	got := next.Cameras[0]
	if got.ID != "cam9" || got.Name != "Коридор" || got.URL != "rtsp://10.0.0.9:554/s" {
		t.Errorf("камера унаследовала чужие поля: %+v", got)
	}
	if got.Enabled {
		t.Error("enabled унаследован от камеры, занимавшей это место")
	}
}

func TestDecodeSettingsClearsCamerasOnEmptyArray(t *testing.T) {
	current := config.Default()
	current.Cameras = []config.Camera{{ID: "cam1", Name: "Камера 1", URL: "rtsp://10.0.0.5:554/s", Enabled: true}}

	next, err := decodeSettings(current, []byte(`{"cameras":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Cameras) != 0 {
		t.Errorf("пустой массив должен очищать список, получено %+v", next.Cameras)
	}
}

func TestDecodeSettingsRejectsNonObject(t *testing.T) {
	for _, body := range []string{`[]`, `"строка"`, `{`, ``} {
		if _, err := decodeSettings(config.Default(), []byte(body)); err == nil {
			t.Errorf("тело %q принято без ошибки", body)
		}
	}
}

// The same guarantee through the handler, since that is what the hub calls.
func TestSettingsPostKeepsOmittedFields(t *testing.T) {
	cfg := config.Default()
	cfg.Control.Token = "secret"
	cfg.Cameras = []config.Camera{{ID: "cam1", Name: "Камера 1", URL: "rtsp://10.0.0.5:554/s", Enabled: true}}

	d := deps(cfg)
	var applied *config.Config
	d.Apply = func(incoming *config.Config) error {
		applied = incoming
		return nil
	}

	r := httptest.NewRequest(http.MethodPost, "/api/settings", strings.NewReader(`{"domain":"sad7.kidsroompf.ru"}`))
	r.RemoteAddr = "127.0.0.1:4000"
	rec := httptest.NewRecorder()
	New(d).handler().ServeHTTP(rec, r)

	if rec.Code != http.StatusOK {
		t.Fatalf("код %d, ожидался 200: %s", rec.Code, rec.Body.String())
	}
	if applied == nil {
		t.Fatal("Apply не вызван")
	}
	if applied.Domain != "sad7.kidsroompf.ru" {
		t.Errorf("домен не применён: %q", applied.Domain)
	}
	if len(applied.Cameras) != 1 {
		t.Errorf("камеры потеряны: %+v", applied.Cameras)
	}
}

// Switching TLS on or off keeps the same address, and the running listener
// holds that port. Binding before releasing it fails on Windows every time,
// which would mean the switch could never be made from the network — so the
// reload has to free the port first.
func TestReloadRebindsSameAddress(t *testing.T) {
	cfg := config.Default()
	cfg.Control.Addr = "127.0.0.1:0"
	cfg.Control.TLS = false
	cfg.Control.Token = "secret"

	s := New(deps(cfg))
	if err := s.listen(cfg.Control.Addr, false); err != nil {
		t.Fatal(err)
	}
	defer s.Stop()

	// Port 0 means the kernel picks one; reload has to be told the real address,
	// exactly as a second reload of an unchanged config would be.
	s.mu.Lock()
	bound := s.ln.Addr().String()
	s.mu.Unlock()

	cfg.Control.Addr = bound
	s.mu.Lock()
	s.addr = bound
	s.mu.Unlock()

	cfg.Control.TLS = true
	if err := s.Reload(cfg); err != nil {
		t.Fatalf("переключение TLS на том же адресе не удалось: %v", err)
	}

	s.mu.Lock()
	gotAddr, gotTLS := s.addr, s.tls
	listening := s.ln != nil
	s.mu.Unlock()

	if !listening {
		t.Fatal("после перепривязки API ничего не слушает")
	}
	if gotAddr != bound || !gotTLS {
		t.Errorf("состояние после перепривязки: addr=%s tls=%v", gotAddr, gotTLS)
	}
}

// An unchanged configuration must not touch the listener at all: a settings
// POST that changes a camera should not interrupt the API serving it.
func TestReloadLeavesUnchangedListenerAlone(t *testing.T) {
	cfg := config.Default()
	cfg.Control.Addr = "127.0.0.1:0"
	cfg.Control.TLS = false
	cfg.Control.Token = "secret"

	s := New(deps(cfg))
	if err := s.listen(cfg.Control.Addr, false); err != nil {
		t.Fatal(err)
	}
	defer s.Stop()

	s.mu.Lock()
	before := s.ln
	cfg.Control.Addr = s.ln.Addr().String()
	s.addr = cfg.Control.Addr
	s.mu.Unlock()

	if err := s.Reload(cfg); err != nil {
		t.Fatal(err)
	}

	s.mu.Lock()
	after := s.ln
	s.mu.Unlock()
	if before != after {
		t.Error("слушатель пересоздан, хотя настройки не менялись")
	}
}

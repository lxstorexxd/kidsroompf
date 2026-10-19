package hub

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestClientReadsStatus(t *testing.T) {
	agent := newFakeAgent(t, false)
	c := NewClient(agent.site("sad5"), 5*time.Second)

	st, err := c.Status()
	if err != nil {
		t.Fatal(err)
	}
	if st.Version != "1.0.0" || st.Domain != "sad5.kidsroompf.ru" {
		t.Errorf("статус разобран неверно: %+v", st)
	}
	if !st.Stream.Running || st.Stream.PID != 4242 {
		t.Errorf("состояние потока разобрано неверно: %+v", st.Stream)
	}
	if st.Certificate.DaysLeft != 87 || !st.Certificate.Available {
		t.Errorf("сертификат разобран неверно: %+v", st.Certificate)
	}
	if ready, enabled := st.CamerasReady(); ready != 1 || enabled != 1 {
		t.Errorf("камеры: %d/%d, ожидалось 1/1", ready, enabled)
	}
}

// A camera that is switched off is not a problem to report, so it counts
// towards neither number.
func TestCamerasReadyIgnoresDisabled(t *testing.T) {
	var st Status
	st.Cameras = []struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Enabled bool   `json:"enabled"`
		Ready   bool   `json:"ready"`
		Readers int    `json:"readers"`
	}{
		{ID: "cam1", Enabled: true, Ready: true},
		{ID: "cam2", Enabled: true, Ready: false},
		{ID: "cam3", Enabled: false, Ready: false},
	}

	if ready, enabled := st.CamerasReady(); ready != 1 || enabled != 2 {
		t.Errorf("получено %d/%d, ожидалось 1/2", ready, enabled)
	}
}

func TestClientSendsToken(t *testing.T) {
	agent := newFakeAgent(t, false)

	bad := agent.site("sad5")
	bad.Token = "не тот токен"
	_, err := NewClient(bad, 5*time.Second).Status()
	if err == nil {
		t.Fatal("неверный токен должен приводить к ошибке")
	}
	// The message has to say what to do, not just "401".
	if !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "kidsroom-agent.exe token") {
		t.Errorf("непонятное сообщение: %v", err)
	}
}

func TestClientExplainsForbidden(t *testing.T) {
	agent := newFakeAgent(t, false)
	agent.breakPath("/api/status", http.StatusForbidden)

	_, err := NewClient(agent.site("sad5"), 5*time.Second).Status()
	if err == nil || !strings.Contains(err.Error(), "allow_cidrs") {
		t.Errorf("403 должен указывать на allow_cidrs, получено: %v", err)
	}
}

func TestClientReportsUnreachable(t *testing.T) {
	site := Site{Name: "sad99", Addr: "http://127.0.0.1:1", Token: "t"}

	_, err := NewClient(site, 2*time.Second).Status()
	if err == nil {
		t.Fatal("закрытый порт должен давать ошибку")
	}
	if !strings.Contains(err.Error(), site.Addr) {
		t.Errorf("в сообщении нет адреса площадки: %v", err)
	}
}

// Over TLS the certificate is not checked by name — the hub reaches a site by
// its VPN address while the certificate names a public domain.
func TestClientAcceptsUnverifiableCertificate(t *testing.T) {
	agent := newFakeAgent(t, true)

	if _, err := NewClient(agent.site("sad5"), 5*time.Second).Status(); err != nil {
		t.Fatalf("самоподписанный сертификат площадки должен приниматься: %v", err)
	}
}

func TestClientEnforcesPinnedFingerprint(t *testing.T) {
	agent := newFakeAgent(t, true)

	sum := sha256.Sum256(agent.cert().Raw)
	want := hex.EncodeToString(sum[:])

	site := agent.site("sad5")
	site.Fingerprint = want
	c := NewClient(site, 5*time.Second)
	if _, err := c.Status(); err != nil {
		t.Fatalf("совпадающий отпечаток отвергнут: %v", err)
	}
	if c.Fingerprint() != want {
		t.Errorf("отпечаток не запомнен: %q", c.Fingerprint())
	}

	site.Fingerprint = strings.Repeat("ab", 32)
	_, err := NewClient(site, 5*time.Second).Status()
	if err == nil || !strings.Contains(err.Error(), "отпечаток") {
		t.Errorf("подменённый сертификат должен отвергаться: %v", err)
	}
}

func TestNormaliseFingerprint(t *testing.T) {
	want := "ab12cd"
	for _, in := range []string{"ab12cd", "AB:12:CD", " ab:12:cd ", "sha256-AB12CD"} {
		if got := normaliseFingerprint(in); got != want {
			t.Errorf("%q -> %q, ожидалось %q", in, got, want)
		}
	}
}

func TestClientApplyAndRestart(t *testing.T) {
	agent := newFakeAgent(t, false)
	c := NewClient(agent.site("sad5"), 5*time.Second)

	if err := c.Apply([]byte(`{"domain":"new.example.ru"}`)); err != nil {
		t.Fatal(err)
	}
	if got := agent.lastApplied(t)["domain"]; got != "new.example.ru" {
		t.Errorf("площадка получила %v", got)
	}
	if err := c.Restart(); err != nil {
		t.Fatal(err)
	}
	if agent.restarts != 1 {
		t.Errorf("перезапусков %d, ожидался 1", agent.restarts)
	}
}

func TestClientRotateToken(t *testing.T) {
	agent := newFakeAgent(t, false)
	c := NewClient(agent.site("sad5"), 5*time.Second)

	token, err := c.RotateToken()
	if err != nil {
		t.Fatal(err)
	}
	if token != "свежий-токен" {
		t.Errorf("получен токен %q", token)
	}
	// The old token stops working immediately, which is why the hub has to
	// store the new one before doing anything else.
	if _, err := c.Status(); err == nil {
		t.Error("старый токен должен перестать работать сразу")
	}
}

func TestSettingsRawIsIndented(t *testing.T) {
	agent := newFakeAgent(t, false)

	raw, err := NewClient(agent.site("sad5"), 5*time.Second).SettingsRaw()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "\n  ") {
		t.Error("вывод должен быть с отступами: его правят в редакторе")
	}
	if !strings.HasSuffix(string(raw), "\n") {
		t.Error("файл должен заканчиваться переводом строки")
	}
}

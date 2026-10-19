package hub

import (
	"crypto/x509"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// fakeAgent stands in for a kidsroom agent's control API. It is deliberately
// thin: the agent's own merge rules are covered by the agent's tests, and what
// matters here is exactly what the hub puts on the wire.
type fakeAgent struct {
	// One port answers both schemes, so a test can follow a site that switches
	// its control API between http and https — the one change that moves the
	// hub to a different address.
	srv    *dualServer
	scheme string
	token  string

	mu       sync.Mutex
	settings map[string]any
	status   map[string]any
	applied  [][]byte // bodies of every POST /api/settings, in order
	restarts int
	newToken string
	fail     map[string]int // path -> status code to answer with
}

func newFakeAgent(t *testing.T, tls bool) *fakeAgent {
	t.Helper()

	a := &fakeAgent{
		token:    "секрет-токен",
		newToken: "свежий-токен",
		fail:     map[string]int{},
		settings: map[string]any{
			"domain":      "sad5.kidsroompf.ru",
			"listen_port": 8443,
			// A real agent always reports where it serves the page from, and the
			// value is a path on its own disk — a Windows one here, so that any
			// test that records settings shows what would be pushed to a Linux
			// site if the hub did not drop it.
			"web_dir": `C:\ProgramData\kidsroom-agent\web`,
			"cameras": []any{
				map[string]any{"id": "cam1", "name": "Камера 1", "url": "rtsp://admin:***@10.0.0.5:554/s1", "enabled": true},
			},
			"hls":      map[string]any{"always_remux": true, "segment_duration": "1s"},
			"control":  map[string]any{"addr": "0.0.0.0:8081", "tls": tls, "token": "***"},
			"mediamtx": map[string]any{"hls_addr": "127.0.0.1:8888", "api_addr": "127.0.0.1:9997"},
		},
		status: map[string]any{
			"version":     "1.0.0",
			"uptime":      "2h0m0s",
			"domain":      "sad5.kidsroompf.ru",
			"listen_port": 8443,
			"certificate": map[string]any{"subject": "sad5.kidsroompf.ru", "days_left": 87, "available": true},
			"stream":      map[string]any{"running": true, "pid": 4242},
			"cameras": []any{
				map[string]any{"id": "cam1", "name": "Камера 1", "enabled": true, "ready": true, "readers": 2},
			},
			"mediamtx_binary": map[string]any{"present": true, "version": "v1.21.2", "expected": "v1.21.2"},
		},
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		code := a.fail[r.Method+" "+r.URL.Path]
		if code == 0 {
			code = a.fail[r.URL.Path]
		}
		a.mu.Unlock()
		if code != 0 {
			writeFake(w, code, map[string]any{"ok": false, "error": "так решил стенд"})
			return
		}

		if got := r.Header.Get("Authorization"); got != "Bearer "+a.token {
			writeFake(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "нужен токен"})
			return
		}

		switch {
		case r.URL.Path == "/api/status" && r.Method == http.MethodGet:
			a.mu.Lock()
			defer a.mu.Unlock()
			writeFake(w, http.StatusOK, a.status)

		case r.URL.Path == "/api/settings" && r.Method == http.MethodGet:
			a.mu.Lock()
			defer a.mu.Unlock()
			writeFake(w, http.StatusOK, a.settings)

		case r.URL.Path == "/api/settings" && r.Method == http.MethodPost:
			body, err := io.ReadAll(r.Body)
			if err != nil || len(body) == 0 {
				writeFake(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "пустое тело"})
				return
			}
			a.mu.Lock()
			a.applied = append(a.applied, body)
			var patch map[string]any
			if json.Unmarshal(body, &patch) == nil {
				for k, v := range patch {
					a.settings[k] = v
					if k == "domain" {
						a.status["domain"] = v
					}
				}
			}
			a.mu.Unlock()
			writeFake(w, http.StatusOK, map[string]any{"ok": true})

		case r.URL.Path == "/api/restart" && r.Method == http.MethodPost:
			a.mu.Lock()
			a.restarts++
			a.mu.Unlock()
			writeFake(w, http.StatusOK, map[string]any{"ok": true})

		case r.URL.Path == "/api/token/regenerate" && r.Method == http.MethodPost:
			a.mu.Lock()
			a.token = a.newToken
			a.mu.Unlock()
			writeFake(w, http.StatusOK, map[string]any{"ok": true, "token": a.newToken})

		default:
			writeFake(w, http.StatusNotFound, map[string]any{"ok": false, "error": "нет такой ручки"})
		}
	})

	a.srv = newDualServer(t, handler)
	a.scheme = "http"
	if tls {
		a.scheme = "https"
	}
	return a
}

// addr is the address matching the scheme the stand was created with.
func (a *fakeAgent) addr() string { return a.srv.URL(a.scheme) }

// cert is the certificate the TLS side presents, for fingerprint tests.
func (a *fakeAgent) cert() *x509.Certificate { return a.srv.cert }

func writeFake(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// site is the inventory entry pointing at this stand.
func (a *fakeAgent) site(name string, tags ...string) Site {
	return Site{Name: name, Addr: a.addr(), Token: a.token, Tags: tags}
}

// lastApplied is the body of the most recent settings POST.
func (a *fakeAgent) lastApplied(t *testing.T) map[string]any {
	t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.applied) == 0 {
		t.Fatal("площадка не получила ни одного POST /api/settings")
	}
	var doc map[string]any
	if err := json.Unmarshal(a.applied[len(a.applied)-1], &doc); err != nil {
		t.Fatalf("тело запроса не разобрать: %v", err)
	}
	return doc
}

func (a *fakeAgent) appliedCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.applied)
}

func (a *fakeAgent) setDomain(d string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.status["domain"] = d
	a.settings["domain"] = d
}

func (a *fakeAgent) breakPath(path string, code int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.fail[path] = code
}

// breakMethod refuses one method on a path and leaves the others working, which
// is how a site that cannot accept a change but still reports its state looks.
func (a *fakeAgent) breakMethod(method, path string, code int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.fail[method+" "+path] = code
}

// cameraIDs is what the stand holds after the hub has edited it.
func (a *fakeAgent) cameraIDs() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	list, _ := a.settings["cameras"].([]any)
	out := make([]string, 0, len(list))
	for _, item := range list {
		if cam, ok := item.(map[string]any); ok {
			id, _ := cam["id"].(string)
			out = append(out, id)
		}
	}
	return out
}

func (a *fakeAgent) camera(id string) map[string]any {
	a.mu.Lock()
	defer a.mu.Unlock()
	list, _ := a.settings["cameras"].([]any)
	for _, item := range list {
		if cam, ok := item.(map[string]any); ok {
			if got, _ := cam["id"].(string); got == id {
				return cam
			}
		}
	}
	return nil
}

// testOptions points the hub at a temporary inventory holding these sites.
func testOptions(t *testing.T, out *strings.Builder, sites ...Site) *Options {
	t.Helper()

	inv := testInventory(t, sites...)
	if err := inv.Save(); err != nil {
		t.Fatal(err)
	}

	o := DefaultOptions()
	o.InventoryPath = inv.Path()
	o.Out = out
	o.Err = out
	o.In = strings.NewReader("")
	o.Yes = true // confirmation is covered separately
	return o
}

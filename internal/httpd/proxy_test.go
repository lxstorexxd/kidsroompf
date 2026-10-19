package httpd

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"kidsroom-agent/internal/config"
)

func proxyFor(known ...string) *hlsProxy {
	set := map[string]bool{}
	for _, k := range known {
		set[k] = true
	}
	return newHLSProxy(func(id string) bool { return set[id] },
		func() string { return "127.0.0.1:8888" }, func(string, ...any) {})
}

// An unknown id must not reach mediamtx: otherwise the internal path namespace
// could be probed from the public port.
func TestProxyRejectsUnknownCamera(t *testing.T) {
	p := proxyFor("cam1")

	for _, path := range []string{"cam2/index.m3u8", "secret/index.m3u8", "cam1", "", "cam1/../../etc"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/"+path, nil)
		p.ServeHTTP(rec, req)

		if rec.Code != http.StatusNotFound {
			t.Errorf("%q: код %d, ожидался 404", path, rec.Code)
		}
	}
}

func TestProxyRejectsWrite(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/cam1/index.m3u8", nil)
	proxyFor("cam1").ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("код %d, ожидался 405", rec.Code)
	}
}

// mediamtx redirects to its own path; the browser is on /hls/<id>, so the
// Location has to be lifted into that namespace or the session is never
// established.
func TestRewriteLocationsAddsPrefix(t *testing.T) {
	resp := &http.Response{
		Header:  http.Header{"Location": []string{"/cam1/index.m3u8?cookieCheck=1"}},
		Request: &http.Request{URL: &url.URL{Path: "/cam1/index.m3u8"}},
	}

	if err := rewriteLocations(resp); err != nil {
		t.Fatal(err)
	}
	if got := resp.Header.Get("Location"); got != "/hls/cam1/index.m3u8?cookieCheck=1" {
		t.Errorf("Location = %q", got)
	}
}

func TestRewriteLocationsLeavesForeignTargets(t *testing.T) {
	resp := &http.Response{
		Header:  http.Header{"Location": []string{"https://example.org/elsewhere"}},
		Request: &http.Request{URL: &url.URL{Path: "/cam1/index.m3u8"}},
	}

	_ = rewriteLocations(resp)
	if got := resp.Header.Get("Location"); got != "https://example.org/elsewhere" {
		t.Errorf("чужой Location изменён: %q", got)
	}
}

func TestRewriteCookiePath(t *testing.T) {
	cases := map[string]string{
		// No Path attribute: the browser derives it from /hls/<id>, so leave it.
		"hlsSession=abc; HttpOnly; Secure":         "hlsSession=abc; HttpOnly; Secure",
		"hlsSession=abc; Path=/cam1; Secure":       "hlsSession=abc; Path=/hls/cam1; Secure",
		"hlsSession=abc; path=/cam1/sub; HttpOnly": "hlsSession=abc; Path=/hls/cam1/sub; HttpOnly",
		"other=1; Path=/elsewhere":                 "other=1; Path=/elsewhere",
	}
	for in, want := range cases {
		if got := rewriteCookiePath(in, "cam1"); got != want {
			t.Errorf("%q -> %q, ожидалось %q", in, got, want)
		}
	}
}

// The web directory is public, but nothing above it is.
func TestStaticRejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Default()
	cfg.WebDir = dir
	s := New(cfg, func(string, ...any) {})

	for _, path := range []string{"/../config.json", "/..%2fconfig.json", "/sub/../../config.json"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		s.handleStatic(rec, req)

		if rec.Code != http.StatusNotFound {
			t.Errorf("%q: код %d, ожидался 404", path, rec.Code)
		}
	}
}

// Only ids and names go out; a source URL with credentials must never appear.
func TestCamerasJSONHidesSources(t *testing.T) {
	cfg := config.Default()
	cfg.Cameras = []config.Camera{
		{ID: "cam1", Name: "Камера 1", URL: "rtsp://admin:secret@10.0.0.5:554/s", Enabled: true},
		{ID: "cam2", Name: "Камера 2", URL: "rtsp://admin:secret@10.0.0.6:554/s", Enabled: false},
	}
	s := New(cfg, func(string, ...any) {})

	rec := httptest.NewRecorder()
	s.handleCameras(rec, httptest.NewRequest(http.MethodGet, "/cameras.json", nil))

	body := rec.Body.String()
	if want := `[{"id":"cam1","name":"Камера 1"}]`; body != want+"\n" {
		t.Errorf("тело = %q, ожидалось %q", body, want)
	}
}

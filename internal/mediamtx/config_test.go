package mediamtx

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"kidsroom-agent/internal/config"
)

func testConfig() *config.Config {
	c := config.Default()
	c.Cameras = []config.Camera{
		{ID: "cam1", Name: "Камера 1", URL: "rtsp://admin:secret@10.0.0.5:554/s1", Enabled: true},
		{ID: "cam2", Name: "Камера 2", URL: "rtsp://admin:secret@10.0.0.6:554/s2", Enabled: false},
	}
	return c
}

func parse(t *testing.T, cfg *config.Config) map[string]any {
	t.Helper()
	raw, err := BuildConfig(cfg)
	if err != nil {
		t.Fatalf("BuildConfig: %v", err)
	}
	var out map[string]any
	if err := yaml.Unmarshal(raw, &out); err != nil {
		t.Fatalf("сгенерирован не-YAML: %v", err)
	}
	return out
}

// Everything except HLS must stay off: cameras are pulled, and viewers reach
// the stream only through the agent's own TLS port.
func TestBuildConfigDisablesOtherServers(t *testing.T) {
	got := parse(t, testConfig())

	for _, key := range []string{"rtsp", "rtmp", "webrtc", "srt", "moq", "metrics", "pprof", "playback"} {
		if got[key] != false {
			t.Errorf("%s должен быть выключен, получено %v", key, got[key])
		}
	}
	if got["hls"] != true {
		t.Error("hls должен быть включён")
	}
}

// mediamtx must never be reachable from outside; only the agent talks to it.
func TestBuildConfigBindsLoopbackOnly(t *testing.T) {
	got := parse(t, testConfig())

	for key, want := range map[string]string{"hlsAddress": DefaultHLSAddr, "apiAddress": DefaultAPIAddr} {
		if got[key] != want {
			t.Errorf("%s = %v, ожидалось %s", key, got[key], want)
		}
		if !strings.HasPrefix(want, "127.0.0.1:") {
			t.Errorf("%s должен слушать loopback, задано %s", key, want)
		}
	}
}

func TestBuildConfigOnlyEnabledCameras(t *testing.T) {
	got := parse(t, testConfig())

	paths, ok := got["paths"].(map[string]any)
	if !ok {
		t.Fatalf("paths не разобрались: %T", got["paths"])
	}
	if len(paths) != 1 {
		t.Fatalf("ожидался один путь, получено %d: %v", len(paths), paths)
	}
	cam1, ok := paths["cam1"].(map[string]any)
	if !ok {
		t.Fatal("нет пути cam1")
	}
	if cam1["source"] != "rtsp://admin:secret@10.0.0.5:554/s1" {
		t.Errorf("не тот источник: %v", cam1["source"])
	}
	// TCP keeps the stream intact over links where UDP is filtered or lossy.
	if cam1["rtspTransport"] != "tcp" {
		t.Errorf("rtspTransport = %v, ожидалось tcp", cam1["rtspTransport"])
	}
}

// With alwaysRemux on, the camera must stay connected, otherwise the first
// viewer would wait for a fresh RTSP handshake.
func TestBuildConfigOnDemandFollowsAlwaysRemux(t *testing.T) {
	cfg := testConfig()

	cfg.HLS.AlwaysRemux = true
	paths := parse(t, cfg)["paths"].(map[string]any)
	if paths["cam1"].(map[string]any)["sourceOnDemand"] != false {
		t.Error("при alwaysRemux источник не должен быть on-demand")
	}

	cfg.HLS.AlwaysRemux = false
	paths = parse(t, cfg)["paths"].(map[string]any)
	if paths["cam1"].(map[string]any)["sourceOnDemand"] != true {
		t.Error("без alwaysRemux источник должен быть on-demand")
	}
}

func TestBuildConfigHLSVariant(t *testing.T) {
	cfg := testConfig()

	if got := parse(t, cfg)["hlsVariant"]; got != "fmp4" {
		t.Errorf("по умолчанию ожидался fmp4, получено %v", got)
	}

	cfg.HLS.LowLatency = true
	if got := parse(t, cfg)["hlsVariant"]; got != "lowLatency" {
		t.Errorf("при low_latency ожидался lowLatency, получено %v", got)
	}
}

func TestBuildConfigDefaultsSegmentDuration(t *testing.T) {
	cfg := testConfig()
	cfg.HLS.SegmentDuration = ""

	if got := parse(t, cfg)["hlsSegmentDuration"]; got != "1s" {
		t.Errorf("пустая длительность должна становиться 1s, получено %v", got)
	}
}

// Two agents on one machine must be able to coexist, so the internal ports
// have to follow the configuration instead of being fixed.
func TestBuildConfigHonoursCustomPorts(t *testing.T) {
	cfg := testConfig()
	cfg.Mediamtx.HLSAddr = "127.0.0.1:18888"
	cfg.Mediamtx.APIAddr = "127.0.0.1:19997"

	got := parse(t, cfg)
	if got["hlsAddress"] != "127.0.0.1:18888" {
		t.Errorf("hlsAddress = %v", got["hlsAddress"])
	}
	if got["apiAddress"] != "127.0.0.1:19997" {
		t.Errorf("apiAddress = %v", got["apiAddress"])
	}
}

// A configuration written before the block existed must not produce an empty
// listen address.
func TestBuildConfigFallsBackToDefaultPorts(t *testing.T) {
	cfg := testConfig()
	cfg.Mediamtx.HLSAddr = ""
	cfg.Mediamtx.APIAddr = ""

	got := parse(t, cfg)
	if got["hlsAddress"] != DefaultHLSAddr || got["apiAddress"] != DefaultAPIAddr {
		t.Errorf("ожидались адреса по умолчанию, получено %v и %v",
			got["hlsAddress"], got["apiAddress"])
	}
}

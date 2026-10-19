package mediamtx

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"kidsroom-agent/internal/config"
)

// DefaultHLSAddr and DefaultAPIAddr are the loopback addresses mediamtx uses
// unless the configuration says otherwise. mediamtx never listens anywhere
// else: the public side is the agent's own TLS port, which proxies HLS.
const (
	DefaultHLSAddr = "127.0.0.1:8888"
	DefaultAPIAddr = "127.0.0.1:9997"
)

// APIBase is the root of the mediamtx control API used for health checks.
func APIBase(cfg *config.Config) string {
	return "http://" + addrOr(cfg.Mediamtx.APIAddr, DefaultAPIAddr)
}

func addrOr(addr, fallback string) string {
	if addr == "" {
		return fallback
	}
	return addr
}

// mtxConfig mirrors the subset of mediamtx.yml the agent sets. mediamtx exits
// on unknown keys, so every field here is verified against the reference
// mediamtx.yml of v1.21.2 and nothing else is emitted.
type mtxConfig struct {
	LogLevel        string   `yaml:"logLevel"`
	LogDestinations []string `yaml:"logDestinations"`

	API        bool   `yaml:"api"`
	APIAddress string `yaml:"apiAddress"`
	Metrics    bool   `yaml:"metrics"`
	PProf      bool   `yaml:"pprof"`
	Playback   bool   `yaml:"playback"`

	// Every ingest/egress server except HLS is off: cameras are pulled, and
	// viewers only ever reach HLS through the agent.
	RTSP   bool `yaml:"rtsp"`
	RTMP   bool `yaml:"rtmp"`
	WebRTC bool `yaml:"webrtc"`
	SRT    bool `yaml:"srt"`
	MoQ    bool `yaml:"moq"`

	HLS                bool     `yaml:"hls"`
	HLSAddress         string   `yaml:"hlsAddress"`
	HLSAllowOrigins    []string `yaml:"hlsAllowOrigins"`
	HLSAlwaysRemux     bool     `yaml:"hlsAlwaysRemux"`
	HLSVariant         string   `yaml:"hlsVariant"`
	HLSSegmentCount    int      `yaml:"hlsSegmentCount"`
	HLSSegmentDuration string   `yaml:"hlsSegmentDuration"`
	HLSPartDuration    string   `yaml:"hlsPartDuration"`

	Paths map[string]mtxPath `yaml:"paths"`
}

type mtxPath struct {
	Source         string `yaml:"source"`
	SourceOnDemand bool   `yaml:"sourceOnDemand"`
	RTSPTransport  string `yaml:"rtspTransport"`
}

// BuildConfig renders mediamtx.yml for the given agent settings.
func BuildConfig(cfg *config.Config) ([]byte, error) {
	variant := "fmp4"
	if cfg.HLS.LowLatency {
		variant = "lowLatency"
	}
	segDur := cfg.HLS.SegmentDuration
	if segDur == "" {
		segDur = "1s"
	}

	m := mtxConfig{
		LogLevel:        "info",
		LogDestinations: []string{"stdout"},

		API:        true,
		APIAddress: addrOr(cfg.Mediamtx.APIAddr, DefaultAPIAddr),
		Metrics:    false,
		PProf:      false,
		Playback:   false,

		RTSP:   false,
		RTMP:   false,
		WebRTC: false,
		SRT:    false,
		MoQ:    false,

		HLS:             true,
		HLSAddress:      addrOr(cfg.Mediamtx.HLSAddr, DefaultHLSAddr),
		HLSAllowOrigins: []string{"*"},
		// Keep remuxing even with nobody watching, so the first parent to open
		// the page gets a picture immediately instead of waiting for a connect.
		HLSAlwaysRemux:     cfg.HLS.AlwaysRemux,
		HLSVariant:         variant,
		HLSSegmentCount:    7,
		HLSSegmentDuration: segDur,
		HLSPartDuration:    "200ms",

		Paths: map[string]mtxPath{},
	}

	for _, cam := range cfg.EnabledCameras() {
		m.Paths[cam.ID] = mtxPath{
			Source: cam.URL,
			// alwaysRemux requires a permanent connection; on-demand would drop
			// the camera as soon as the last viewer leaves.
			SourceOnDemand: !cfg.HLS.AlwaysRemux,
			RTSPTransport:  "tcp",
		}
	}

	body, err := yaml.Marshal(m)
	if err != nil {
		return nil, err
	}
	const header = "# Создаётся автоматически kidsroom-agent.\n" +
		"# Правки будут перезаписаны при следующем применении настроек.\n"
	return append([]byte(header), body...), nil
}

// WriteConfig renders and atomically replaces mediamtx.yml.
func WriteConfig(path string, cfg *config.Config) error {
	data, err := BuildConfig(cfg)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".mediamtx-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("замена %s: %w", path, err)
	}
	return nil
}

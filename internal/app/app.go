// Package app wires the agent together: configuration state, the mediamtx
// supervisor, the public TLS server and the control API.
package app

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"reflect"
	"strings"
	"sync"
	"time"

	"kidsroom-agent/internal/certs"
	"kidsroom-agent/internal/config"
	"kidsroom-agent/internal/control"
	"kidsroom-agent/internal/httpd"
	"kidsroom-agent/internal/logx"
	"kidsroom-agent/internal/mediamtx"
	"kidsroom-agent/internal/paths"
	"kidsroom-agent/internal/webdefaults"
)

// EndpointFile holds the loopback URL of the control API, for the tray and
// the settings window.
func EndpointFile() string { return paths.EndpointFile() }

type App struct {
	configPath string

	mu  sync.RWMutex
	cfg *config.Config

	logger *log.Logger
	logw   io.Closer

	pub *httpd.Server
	ctl *control.Server
	sup *mediamtx.Supervisor

	startedAt time.Time
}

// New loads the configuration, prepares the data directory and builds the
// subsystems without starting them.
func New(alsoStderr bool) (*App, error) {
	if err := os.MkdirAll(paths.LogsDir(), 0o755); err != nil {
		return nil, err
	}
	lw, err := logx.New(paths.AgentLogFile())
	if err != nil {
		return nil, err
	}
	var sink io.Writer = lw
	if alsoStderr {
		sink = io.MultiWriter(lw, os.Stderr)
	}
	logger := log.New(sink, "", log.LstdFlags)

	cfg, dirty, err := config.Load(paths.ConfigFile())
	if err != nil {
		return nil, err
	}

	a := &App{
		configPath: paths.ConfigFile(),
		cfg:        cfg,
		logger:     logger,
		logw:       lw,
		startedAt:  time.Now(),
	}
	a.logf("kidsroom-agent запускается; настройки %s, логи %s", paths.ConfigFile(), paths.LogsDir())

	if dirty {
		if err := config.Save(a.configPath, cfg); err != nil {
			return nil, err
		}
		a.logf("конфигурация создана: %s", a.configPath)
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("конфигурация неверна: %w", err)
	}

	// The page must exist on disk before the first request arrives.
	if err := webdefaults.Install(cfg.WebDir, a.logf); err != nil {
		return nil, fmt.Errorf("подготовка каталога страницы: %w", err)
	}

	a.pub = httpd.New(cfg, a.logf)
	a.sup = mediamtx.New(cfg, a.mediamtxLog(), a.logf)
	a.ctl = control.New(control.Deps{
		Settings:        a.Snapshot,
		Apply:           a.Apply,
		Status:          func() any { return a.Status() },
		Restart:         a.sup.Restart,
		RegenerateToken: a.RegenerateToken,
		Cert:            a.certificate,
		Logf:            a.logf,
	})

	pair, err := a.ensureCertificate(cfg)
	if err != nil {
		return nil, err
	}
	a.pub.SetCertificate(pair)

	return a, nil
}

func (a *App) mediamtxLog() io.Writer {
	w, err := logx.New(paths.MediamtxLogFile())
	if err != nil {
		a.logf("не удалось открыть лог mediamtx: %v (вывод будет отброшен)", err)
		return io.Discard
	}
	return w
}

func (a *App) logf(format string, args ...any) {
	a.logger.Printf(format, args...)
}

// Snapshot returns the live configuration.
func (a *App) Snapshot() *config.Config {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.cfg
}

func (a *App) setCfg(cfg *config.Config) {
	a.mu.Lock()
	a.cfg = cfg
	a.mu.Unlock()
}

func (a *App) certificate() *tls.Certificate {
	return a.pub.CertificateCurrent()
}

// ensureCertificate loads the provisioned pair, or issues a self-signed one so
// the public port always comes up.
func (a *App) ensureCertificate(cfg *config.Config) (*tls.Certificate, error) {
	if cfg.TLS.CertPEM != "" && cfg.TLS.KeyPEM != "" {
		pair, err := certs.LoadPair(cfg.TLS.CertPEM, cfg.TLS.KeyPEM)
		if err == nil {
			return pair, nil
		}
		// A broken certificate must not keep the nursery offline.
		a.logf("сохранённый сертификат не загрузился (%v), генерирую self-signed", err)
	}

	certPEM, keyPEM, err := certs.GenerateSelfSigned(cfg.Domain)
	if err != nil {
		return nil, fmt.Errorf("генерация self-signed сертификата: %w", err)
	}
	next := cfg.Clone()
	next.TLS = config.TLSConfig{CertPEM: certPEM, KeyPEM: keyPEM, SelfSigned: true}
	if err := config.Save(a.configPath, next); err != nil {
		return nil, err
	}
	a.setCfg(next)
	a.logf("выписан self-signed сертификат на %q", nonEmpty(cfg.Domain, "localhost"))
	return certs.LoadPair(certPEM, keyPEM)
}

func nonEmpty(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

// Run starts every subsystem and blocks until ctx is cancelled.
func (a *App) Run(ctx context.Context) error {
	if err := a.pub.Start(ctx); err != nil {
		return err
	}
	if err := a.ctl.Start(ctx); err != nil {
		return err
	}
	a.writeEndpointHint()

	if err := a.sup.Start(ctx); err != nil {
		// Without mediamtx there is no video, but the page and the API must
		// stay up so the problem can be seen and fixed remotely.
		a.logf("ВНИМАНИЕ: mediamtx не запущен: %v", err)
	}

	a.logf("агент готов: страница %s", a.PublicURL())
	<-ctx.Done()

	a.logf("останов по сигналу")
	a.sup.Stop()
	a.ctl.Stop()
	a.pub.Stop()
	if a.logw != nil {
		_ = a.logw.Close()
	}
	return nil
}

// writeEndpointHint publishes the loopback control URL for the tray and GUI.
func (a *App) writeEndpointHint() {
	url := a.ctl.URL()
	if err := os.WriteFile(EndpointFile(), []byte(url+"\n"), 0o644); err != nil {
		a.logf("не удалось записать %s: %v", EndpointFile(), err)
	}
}

// PublicURL is the address to hand to parents.
func (a *App) PublicURL() string {
	cfg := a.Snapshot()
	host := nonEmpty(cfg.Domain, "localhost")
	return fmt.Sprintf("https://%s:%d/", host, cfg.ListenPort)
}

// Apply merges an incoming settings document into the live configuration,
// validates it, stores it and pushes it to the subsystems.
func (a *App) Apply(incoming *config.Config) error {
	cur := a.Snapshot()
	next := config.Merge(cur, incoming)

	if err := next.Validate(); err != nil {
		return err
	}

	certChanged := next.TLS.CertPEM != cur.TLS.CertPEM || next.TLS.KeyPEM != cur.TLS.KeyPEM
	var pair *tls.Certificate
	if certChanged {
		loaded, err := certs.LoadPair(next.TLS.CertPEM, next.TLS.KeyPEM)
		if err != nil {
			// Reject before storing: a bad certificate would take the page down.
			return fmt.Errorf("сертификат отклонён: %w", err)
		}
		pair = loaded
		next.TLS.SelfSigned = false
	} else if next.Domain != cur.Domain && cur.TLS.SelfSigned {
		// The self-signed certificate names the domain, so reissue it.
		certPEM, keyPEM, err := certs.GenerateSelfSigned(next.Domain)
		if err != nil {
			return fmt.Errorf("перевыпуск self-signed сертификата: %w", err)
		}
		next.TLS = config.TLSConfig{CertPEM: certPEM, KeyPEM: keyPEM, SelfSigned: true}
		if pair, err = certs.LoadPair(certPEM, keyPEM); err != nil {
			return err
		}
		certChanged = true
	}

	if err := config.Save(a.configPath, next); err != nil {
		return fmt.Errorf("сохранение настроек: %w", err)
	}
	a.setCfg(next)

	var probs []string

	if certChanged && pair != nil {
		a.pub.SetCertificate(pair)
	}
	if next.WebDir != cur.WebDir {
		if err := webdefaults.Install(next.WebDir, a.logf); err != nil {
			probs = append(probs, "каталог страницы: "+err.Error())
		}
	}
	if err := a.pub.Reload(next); err != nil {
		probs = append(probs, "публичный сервер: "+err.Error())
	}
	if err := a.ctl.Reload(next); err != nil {
		probs = append(probs, "control API: "+err.Error())
	} else {
		a.writeEndpointHint()
	}
	if streamChanged(cur, next) {
		if err := a.sup.Apply(next); err != nil {
			probs = append(probs, "mediamtx: "+err.Error())
		}
	}

	if len(probs) > 0 {
		// The settings are stored; say exactly which part did not take effect.
		return fmt.Errorf("настройки сохранены, но часть не применилась: %s", strings.Join(probs, "; "))
	}
	return nil
}

// streamChanged reports whether mediamtx has to be reconfigured.
func streamChanged(a, b *config.Config) bool {
	return !reflect.DeepEqual(a.Cameras, b.Cameras) || a.HLS != b.HLS
}

// RegenerateToken issues a new control API token and stores it.
func (a *App) RegenerateToken() (string, error) {
	token, err := config.NewToken()
	if err != nil {
		return "", err
	}
	next := a.Snapshot().Clone()
	next.Control.Token = token
	if err := config.Save(a.configPath, next); err != nil {
		return "", err
	}
	a.setCfg(next)
	return token, nil
}

// CameraStatus joins the configured camera with what mediamtx reports.
type CameraStatus struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	Ready   bool   `json:"ready"`
	Readers int    `json:"readers"`
	Source  string `json:"source,omitempty"`
	URL     string `json:"url"`
}

// Status is the report served by GET /api/status and shown in the tray.
type Status struct {
	Version     string               `json:"version"`
	Uptime      string               `json:"uptime"`
	PublicURL   string               `json:"public_url"`
	Domain      string               `json:"domain"`
	ListenPort  int                  `json:"listen_port"`
	WebDir      string               `json:"web_dir"`
	ControlAddr string               `json:"control_addr"`
	Certificate certs.Info           `json:"certificate"`
	Stream      mediamtx.Status      `json:"stream"`
	Cameras     []CameraStatus       `json:"cameras"`
	Warnings    []string             `json:"warnings,omitempty"`
	Mediamtx    mediamtxBinaryStatus `json:"mediamtx_binary"`
}

type mediamtxBinaryStatus struct {
	Path     string `json:"path"`
	Present  bool   `json:"present"`
	Version  string `json:"version"`
	Expected string `json:"expected"`
}

// Status collects the current state of the whole agent.
func (a *App) Status() Status {
	cfg := a.Snapshot()
	stream := a.sup.Status()

	ready := map[string]mediamtx.PathStatus{}
	for _, p := range stream.Paths {
		ready[p.Name] = p
	}

	cams := make([]CameraStatus, 0, len(cfg.Cameras))
	for _, c := range cfg.Cameras {
		cs := CameraStatus{
			ID:      c.ID,
			Name:    c.Name,
			Enabled: c.Enabled,
			URL:     fmt.Sprintf("https://%s:%d/hls/%s/index.m3u8", nonEmpty(cfg.Domain, "localhost"), cfg.ListenPort, c.ID),
		}
		if p, ok := ready[c.ID]; ok {
			cs.Ready, cs.Readers, cs.Source = p.Ready, p.Readers, p.Source
		}
		cams = append(cams, cs)
	}

	st := Status{
		Version:     Version,
		Uptime:      time.Since(a.startedAt).Round(time.Second).String(),
		PublicURL:   a.PublicURL(),
		Domain:      cfg.Domain,
		ListenPort:  cfg.ListenPort,
		WebDir:      cfg.WebDir,
		ControlAddr: cfg.Control.Addr,
		Certificate: a.pub.CertificateInfo(),
		Stream:      stream,
		Cameras:     cams,
		Mediamtx: mediamtxBinaryStatus{
			Path:     paths.MediamtxBinary(),
			Version:  a.sup.BinaryVersion(),
			Expected: mediamtx.Version,
		},
	}
	if _, err := os.Stat(paths.MediamtxBinary()); err == nil {
		st.Mediamtx.Present = true
	} else {
		st.Warnings = append(st.Warnings,
			"не найден "+paths.MediamtxName+" — выполните "+paths.AgentCmd+" fetch-mediamtx")
	}

	// A mismatched mediamtx rejects configuration keys it does not know and
	// exits at once, which from the outside looks like an endless restart loop
	// with no reason given.
	if v := st.Mediamtx.Version; v != "" && v != mediamtx.Version {
		st.Warnings = append(st.Warnings, fmt.Sprintf(
			"версия mediamtx %s не совпадает с ожидаемой %s — выполните %s fetch-mediamtx",
			v, mediamtx.Version, paths.AgentCmd))
	}

	if len(cfg.EnabledCameras()) == 0 {
		st.Warnings = append(st.Warnings, "ни одна камера не включена")
	}
	if st.Certificate.Available && st.Certificate.SelfSign {
		st.Warnings = append(st.Warnings, "сертификат самоподписанный: браузер будет предупреждать")
	}
	if st.Certificate.Available && st.Certificate.DaysLeft < 14 {
		st.Warnings = append(st.Warnings,
			fmt.Sprintf("сертификат истекает через %d дн.", st.Certificate.DaysLeft))
	}
	for _, c := range cams {
		if c.Enabled && !c.Ready {
			st.Warnings = append(st.Warnings, "камера не отдаёт поток: "+c.Name)
		}
	}
	return st
}

// Token returns the control API token for the local settings window.
func (a *App) Token() string { return a.Snapshot().Control.Token }

// ErrNotConfigured is returned when a command needs settings that do not exist.
var ErrNotConfigured = errors.New("конфигурация не найдена")

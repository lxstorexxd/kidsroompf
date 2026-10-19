// Package config holds the agent settings: schema, defaults, validation,
// atomic persistence and secret masking for the control API.
package config

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"kidsroom-agent/internal/paths"
)

// Mask replaces secrets in API responses. Sending it back means "keep the
// stored value", so a client can read-modify-write without ever holding the
// real password.
const Mask = "***"

var idRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// Camera is one RTSP source pulled by mediamtx and republished as HLS.
type Camera struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	URL     string `json:"url"`
	Enabled bool   `json:"enabled"`
}

// TLSConfig carries the certificate served on the public port. It is stored
// inline so a single settings POST can provision a whole machine.
type TLSConfig struct {
	CertPEM    string `json:"cert_pem"`
	KeyPEM     string `json:"key_pem"`
	SelfSigned bool   `json:"self_signed"`
}

type HLSConfig struct {
	AlwaysRemux     bool   `json:"always_remux"`
	SegmentDuration string `json:"segment_duration"`
	LowLatency      bool   `json:"low_latency"`
}

// ControlConfig describes the settings API, which is reachable from the LAN
// and over VPN and therefore always guarded by a token and a subnet allowlist.
type ControlConfig struct {
	Addr       string   `json:"addr"`
	TLS        bool     `json:"tls"`
	Token      string   `json:"token"`
	AllowCIDRs []string `json:"allow_cidrs"`
}

// MediamtxConfig holds the loopback addresses mediamtx listens on. They are
// configurable because two agents on one machine would otherwise fight over
// the same ports, and because something else may already use them.
type MediamtxConfig struct {
	HLSAddr string `json:"hls_addr"`
	APIAddr string `json:"api_addr"`
}

type Config struct {
	Domain     string         `json:"domain"`
	ListenPort int            `json:"listen_port"`
	WebDir     string         `json:"web_dir"`
	TLS        TLSConfig      `json:"tls"`
	Cameras    []Camera       `json:"cameras"`
	HLS        HLSConfig      `json:"hls"`
	Control    ControlConfig  `json:"control"`
	Mediamtx   MediamtxConfig `json:"mediamtx"`
}

// Default returns a usable configuration for a fresh install.
func Default() *Config {
	return &Config{
		ListenPort: 8443,
		WebDir:     paths.DefaultWebDir(),
		Cameras:    []Camera{},
		HLS: HLSConfig{
			AlwaysRemux:     true,
			SegmentDuration: "1s",
			LowLatency:      false,
		},
		Mediamtx: MediamtxConfig{
			HLSAddr: "127.0.0.1:8888",
			APIAddr: "127.0.0.1:9997",
		},
		Control: ControlConfig{
			Addr: "0.0.0.0:8081",
			TLS:  true,
			AllowCIDRs: []string{
				"127.0.0.0/8",
				"10.0.0.0/8",
				"172.16.0.0/12",
				"192.168.0.0/16",
				"fd00::/8",
			},
		},
	}
}

// NewToken returns a fresh control API token.
func NewToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// Load reads the config file, filling in defaults for absent fields. A missing
// file yields the defaults plus a freshly generated token; dirty reports that
// the caller should persist what Load had to fill in.
func Load(path string) (cfg *Config, dirty bool, err error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		cfg = Default()
		if cfg.Control.Token, err = NewToken(); err != nil {
			return nil, false, err
		}
		return cfg, true, nil
	}
	if err != nil {
		return nil, false, err
	}
	cfg = Default()
	// Notepad and PowerShell's Out-File both put a UTF-8 BOM in front, and the
	// JSON parser would otherwise refuse the file with a message about a
	// character nobody typed.
	raw = bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf"))
	if err := json.Unmarshal(raw, cfg); err != nil {
		return nil, false, fmt.Errorf("разбор %s: %w", path, err)
	}
	if cfg.Control.Token == "" {
		if cfg.Control.Token, err = NewToken(); err != nil {
			return nil, false, err
		}
		dirty = true
	}
	if cfg.WebDir == "" {
		cfg.WebDir = paths.DefaultWebDir()
		dirty = true
	}
	if cfg.Mediamtx.HLSAddr == "" || cfg.Mediamtx.APIAddr == "" {
		d := Default().Mediamtx
		if cfg.Mediamtx.HLSAddr == "" {
			cfg.Mediamtx.HLSAddr = d.HLSAddr
		}
		if cfg.Mediamtx.APIAddr == "" {
			cfg.Mediamtx.APIAddr = d.APIAddr
		}
		dirty = true
	}
	return cfg, dirty, nil
}

// Save writes the config atomically and restricts its ACL to SYSTEM and the
// local administrators, since it holds camera passwords and the API token.
func Save(path string, cfg *Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	harden(path)
	return nil
}

// Validate reports every problem it finds, so a bad settings POST comes back
// with a complete list instead of one error at a time.
func (c *Config) Validate() error {
	var probs []string

	if c.ListenPort < 1 || c.ListenPort > 65535 {
		probs = append(probs, fmt.Sprintf("listen_port %d вне диапазона 1-65535", c.ListenPort))
	}
	if strings.TrimSpace(c.WebDir) == "" {
		probs = append(probs, "web_dir пуст")
	}
	if c.Domain != "" && strings.ContainsAny(c.Domain, ` /\:`) {
		probs = append(probs, "domain должен быть именем хоста без схемы и порта")
	}

	seen := map[string]bool{}
	for i, cam := range c.Cameras {
		where := fmt.Sprintf("cameras[%d]", i)
		if !idRe.MatchString(cam.ID) {
			probs = append(probs, where+": недопустимый id "+quote(cam.ID))
		}
		if seen[cam.ID] {
			probs = append(probs, where+": id дублируется: "+cam.ID)
		}
		seen[cam.ID] = true
		if strings.TrimSpace(cam.Name) == "" {
			probs = append(probs, where+": name пуст")
		}
		if err := validateSource(cam.URL); err != nil {
			probs = append(probs, where+": "+err.Error())
		}
	}

	if c.HLS.SegmentDuration != "" {
		if _, err := time.ParseDuration(c.HLS.SegmentDuration); err != nil {
			probs = append(probs, "hls.segment_duration: нужна длительность вида 1s")
		}
	}

	if _, _, err := net.SplitHostPort(c.Control.Addr); err != nil {
		probs = append(probs, "control.addr: нужен вид host:port")
	}

	for name, addr := range map[string]string{
		"mediamtx.hls_addr": c.Mediamtx.HLSAddr,
		"mediamtx.api_addr": c.Mediamtx.APIAddr,
	} {
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			probs = append(probs, name+": нужен вид host:port")
			continue
		}
		// mediamtx must never be reachable from outside: the public side is
		// the agent's own TLS port.
		if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
			probs = append(probs, name+": адрес должен быть loopback (127.0.0.1 или ::1)")
		}
	}
	if c.Mediamtx.HLSAddr == c.Mediamtx.APIAddr {
		probs = append(probs, "mediamtx.hls_addr и mediamtx.api_addr совпадают")
	}
	for _, cidr := range c.Control.AllowCIDRs {
		if _, _, err := net.ParseCIDR(cidr); err != nil {
			probs = append(probs, "control.allow_cidrs: не разобрать "+cidr)
		}
	}
	if !c.ControlIsLoopbackOnly() && c.Control.Token == "" {
		probs = append(probs, "control.token обязателен, когда API слушает не только loopback")
	}

	if (c.TLS.CertPEM == "") != (c.TLS.KeyPEM == "") {
		probs = append(probs, "tls: нужны и сертификат, и приватный ключ")
	}

	if len(probs) > 0 {
		return errors.New(strings.Join(probs, "; "))
	}
	return nil
}

func quote(s string) string {
	if len(s) > 40 {
		s = s[:40]
	}
	return `"` + s + `"`
}

func validateSource(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return errors.New("url пуст")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return errors.New("url не разобрать: " + err.Error())
	}
	switch u.Scheme {
	case "rtsp", "rtsps":
	default:
		return errors.New("поддерживаются только схемы rtsp:// и rtsps://")
	}
	if u.Host == "" {
		return errors.New("в url нет хоста")
	}
	return nil
}

// ControlIsLoopbackOnly reports whether the control API is bound to a loopback
// address only, which is the one case where requests may skip the token.
func (c *Config) ControlIsLoopbackOnly() bool {
	host, _, err := net.SplitHostPort(c.Control.Addr)
	if err != nil {
		return false
	}
	if host == "" {
		return false // an empty host means every interface
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return strings.EqualFold(host, "localhost")
	}
	return ip.IsLoopback()
}

// EnabledCameras returns the cameras that should be streaming.
func (c *Config) EnabledCameras() []Camera {
	out := make([]Camera, 0, len(c.Cameras))
	for _, cam := range c.Cameras {
		if cam.Enabled {
			out = append(out, cam)
		}
	}
	return out
}

// Clone returns a deep copy, so a handler can build a candidate config without
// touching the live one.
func (c *Config) Clone() *Config {
	cp := *c
	cp.Cameras = append([]Camera(nil), c.Cameras...)
	cp.Control.AllowCIDRs = append([]string(nil), c.Control.AllowCIDRs...)
	return &cp
}

// Masked returns a copy safe to send over the network: camera passwords, the
// private key and the token are replaced by Mask.
func (c *Config) Masked() *Config {
	cp := c.Clone()
	for i := range cp.Cameras {
		cp.Cameras[i].URL = MaskURLPassword(cp.Cameras[i].URL)
	}
	if cp.TLS.KeyPEM != "" {
		cp.TLS.KeyPEM = Mask
	}
	if cp.Control.Token != "" {
		cp.Control.Token = Mask
	}
	return cp
}

// credParamRe matches a credential carried as a parameter rather than as
// userinfo: rtsp://host/user=admin&password=secret&channel=1?.sdp is how a
// number of IP cameras want to be addressed, and the password is then not in
// the part url.Parse calls the userinfo.
//
// It is matched over the whole URL on purpose. In that form the parameters sit
// in the path and "?.sdp" is the query, so asking url.Query() for them finds
// nothing. The word boundary keeps the match off names that merely end in
// one of these, so "bypass=1" is left alone.
var credParamRe = regexp.MustCompile(`(?i)\b(password|passwd|pwd|pass)=([^&?#]*)`)

// MaskURLPassword rewrites rtsp://user:secret@host/path as rtsp://user:***@host/path,
// and password=secret as password=*** wherever it appears in the URL.
//
// Both forms are in use across the fleet, and a password that is not masked
// here is a password handed out by GET /api/settings and shown in the settings
// window.
//
// The userinfo is spliced in by hand because url.UserPassword would
// percent-encode the asterisks into %2A%2A%2A, which is correct but reads
// badly in the settings window and in an API response.
func MaskURLPassword(raw string) string {
	masked := credParamRe.ReplaceAllString(raw, "${1}="+Mask)

	u, err := url.Parse(masked)
	if err != nil || u.User == nil {
		return masked
	}
	if _, hasPass := u.User.Password(); !hasPass {
		return masked
	}
	user := u.User.Username()
	u.User = nil

	rest := u.String()
	const sep = "://"
	i := strings.Index(rest, sep)
	if i < 0 {
		return masked
	}
	return rest[:i+len(sep)] + user + ":" + Mask + "@" + rest[i+len(sep):]
}

// Merge folds an incoming (possibly masked) config into the current one and
// returns the candidate to validate and store. Masked fields keep their stored
// value, and the token is never taken from a request body.
func Merge(current, incoming *Config) *Config {
	next := incoming.Clone()

	if next.TLS.KeyPEM == Mask || next.TLS.KeyPEM == "" {
		next.TLS.KeyPEM = current.TLS.KeyPEM
		if next.TLS.CertPEM == "" {
			next.TLS.CertPEM = current.TLS.CertPEM
			next.TLS.SelfSigned = current.TLS.SelfSigned
		}
	}

	byID := map[string]Camera{}
	for _, cam := range current.Cameras {
		byID[cam.ID] = cam
	}
	for i, cam := range next.Cameras {
		if old, ok := byID[cam.ID]; ok {
			next.Cameras[i].URL = unmaskURL(cam.URL, old.URL)
		}
	}

	// The token only ever changes through the dedicated regenerate endpoint.
	next.Control.Token = current.Control.Token

	if next.WebDir == "" {
		next.WebDir = current.WebDir
	}
	if next.Control.Addr == "" {
		next.Control.Addr = current.Control.Addr
	}
	if len(next.Control.AllowCIDRs) == 0 {
		next.Control.AllowCIDRs = append([]string(nil), current.Control.AllowCIDRs...)
	}
	if next.ListenPort == 0 {
		next.ListenPort = current.ListenPort
	}
	if next.HLS.SegmentDuration == "" {
		next.HLS.SegmentDuration = current.HLS.SegmentDuration
	}
	if next.Mediamtx.HLSAddr == "" {
		next.Mediamtx.HLSAddr = current.Mediamtx.HLSAddr
	}
	if next.Mediamtx.APIAddr == "" {
		next.Mediamtx.APIAddr = current.Mediamtx.APIAddr
	}
	return next
}

// unmaskURL restores the stored password when the incoming URL carries the mask.
//
// Every form that MaskURLPassword hides has to be restored here, or reading the
// settings and sending them back — what the settings window, the fleet tool and
// any provisioning script all do — would replace the password with three
// asterisks and take the camera off the air.
func unmaskURL(incoming, stored string) string {
	out := unmaskCredParams(incoming, stored)

	in, err := url.Parse(out)
	if err != nil || in.User == nil {
		return out
	}
	pass, hasPass := in.User.Password()
	if !hasPass || pass != Mask {
		return out
	}
	old, err := url.Parse(stored)
	if err != nil || old.User == nil {
		return out
	}
	oldPass, ok := old.User.Password()
	if !ok {
		return out
	}
	in.User = url.UserPassword(in.User.Username(), oldPass)
	return in.String()
}

// unmaskCredParams puts the stored value back into every masked credential
// parameter, matching them up in the order they appear.
func unmaskCredParams(incoming, stored string) string {
	if !credParamRe.MatchString(incoming) {
		return incoming
	}
	old := credParamRe.FindAllStringSubmatch(stored, -1)
	if len(old) == 0 {
		return incoming
	}

	n := 0
	return credParamRe.ReplaceAllStringFunc(incoming, func(match string) string {
		defer func() { n++ }()

		parts := credParamRe.FindStringSubmatch(match)
		if parts[2] != Mask || n >= len(old) {
			return match
		}
		return parts[1] + "=" + old[n][2]
	})
}

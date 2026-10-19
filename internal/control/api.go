// Package control serves the settings API. It is reachable from the LAN and
// over VPN, so every request passes a subnet allowlist and a bearer token
// before any body is parsed.
package control

import (
	"context"
	"crypto/subtle"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"kidsroom-agent/internal/config"
)

// maxBody caps a settings POST; a certificate chain plus a camera list is
// kilobytes, so anything larger is a mistake or an attack.
const maxBody = 1 << 20

// Deps wires the API to the running agent.
type Deps struct {
	// Settings returns the live configuration.
	Settings func() *config.Config
	// Apply merges, validates, stores and activates an incoming config.
	Apply func(incoming *config.Config) error
	// Status returns the JSON-serialisable status report.
	Status func() any
	// Restart restarts the stream (mediamtx) without touching the listeners.
	Restart func()
	// RegenerateToken issues and stores a new control token.
	RegenerateToken func() (string, error)
	// Cert provides the certificate for the API's own TLS, shared with the
	// public port.
	Cert func() *tls.Certificate
	Logf func(string, ...any)
}

type Server struct {
	deps Deps

	mu   sync.Mutex
	srv  *http.Server
	ln   net.Listener
	addr string
	tls  bool

	throttle *throttle
}

func New(deps Deps) *Server {
	return &Server{deps: deps, throttle: newThrottle(5, time.Minute)}
}

// Start binds the control address from the current settings.
func (s *Server) Start(ctx context.Context) error {
	cfg := s.deps.Settings()
	if err := s.listen(cfg.Control.Addr, cfg.Control.TLS); err != nil {
		return err
	}
	go func() {
		<-ctx.Done()
		s.Stop()
	}()
	return nil
}

// Reload rebinds when the address or the TLS switch changed.
func (s *Server) Reload(cfg *config.Config) error {
	s.mu.Lock()
	same := s.addr == cfg.Control.Addr && s.tls == cfg.Control.TLS
	s.mu.Unlock()
	if same {
		return nil
	}
	s.deps.Logf("control API переезжает на %s (tls=%v)", cfg.Control.Addr, cfg.Control.TLS)
	return s.listen(cfg.Control.Addr, cfg.Control.TLS)
}

func (s *Server) listen(addr string, useTLS bool) error {
	s.mu.Lock()
	sameAddr := s.ln != nil && s.addr == addr
	s.mu.Unlock()

	// Turning TLS on or off keeps the address, and the running listener still
	// holds the port — binding first would fail every time with "only one usage
	// of each socket address", which would make the switch impossible to perform
	// remotely. So in that case the old listener goes down first. The API is
	// unreachable for the few milliseconds in between; that is a management
	// channel, not the stream parents are watching.
	if sameAddr {
		s.closeNow()
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		if sameAddr {
			return fmt.Errorf("control API освободил %s, но не смог занять снова: %w — перезапустите службу", addr, err)
		}
		return fmt.Errorf("control API не смог занять %s: %w", addr, err)
	}

	srv := &http.Server{
		Handler:           s.handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	if useTLS {
		srv.TLSConfig = &tls.Config{
			MinVersion: tls.VersionTLS12,
			GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
				if pair := s.deps.Cert(); pair != nil {
					return pair, nil
				}
				return nil, errors.New("сертификат ещё не загружен")
			},
		}
	}

	s.mu.Lock()
	old, oldLn := s.srv, s.ln
	s.srv, s.ln, s.addr, s.tls = srv, ln, addr, useTLS
	s.mu.Unlock()

	if old != nil {
		_ = old.Close()
		if oldLn != nil {
			_ = oldLn.Close()
		}
	}

	go func() {
		var err error
		if useTLS {
			err = srv.ServeTLS(ln, "", "")
		} else {
			err = srv.Serve(ln)
		}
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.deps.Logf("control API остановлен с ошибкой: %v", err)
		}
	}()

	scheme := "http"
	if useTLS {
		scheme = "https"
	}
	s.deps.Logf("control API слушает %s://%s", scheme, addr)
	return nil
}

// closeNow drops the listener and every connection on it at once, without
// waiting for requests to finish.
//
// Waiting is not an option here: the request that asked for the change is
// itself one of those connections, so a graceful shutdown would block until it
// times out. The caller of this change loses its answer and has to read the
// settings back to see whether it took effect — which is what the fleet tool
// does.
func (s *Server) closeNow() {
	s.mu.Lock()
	srv, ln := s.srv, s.ln
	s.srv, s.ln = nil, nil
	s.mu.Unlock()

	if srv != nil {
		_ = srv.Close()
	}
	if ln != nil {
		_ = ln.Close()
	}
}

func (s *Server) Stop() {
	s.mu.Lock()
	srv, ln := s.srv, s.ln
	s.srv, s.ln = nil, nil
	s.mu.Unlock()

	if srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}
	if ln != nil {
		_ = ln.Close()
	}
}

// URL is the address the GUI and the tray should talk to.
func (s *Server) URL() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	scheme := "http"
	if s.tls {
		scheme = "https"
	}
	_, port, err := net.SplitHostPort(s.addr)
	if err != nil {
		return scheme + "://" + s.addr
	}
	return scheme + "://127.0.0.1:" + port
}

func (s *Server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/settings", s.handleSettings)
	mux.HandleFunc("/api/restart", s.handleRestart)
	mux.HandleFunc("/api/token/regenerate", s.handleRegenerateToken)
	mux.HandleFunc("/api/token", s.handleToken)
	return s.guard(mux)
}

// guard enforces, in order: the subnet allowlist, the failed-attempt throttle
// and the bearer token. Requests from loopback skip the token so the local GUI
// and tray work without configuration.
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cfg := s.deps.Settings()

		ip := remoteIP(r)
		if ip == nil {
			writeErr(w, http.StatusForbidden, "не удалось определить адрес клиента")
			return
		}

		if !allowed(ip, cfg.Control.AllowCIDRs) {
			s.deps.Logf("control API: отказ по адресу %s (нет в allow_cidrs)", ip)
			writeErr(w, http.StatusForbidden, "адрес не входит в control.allow_cidrs")
			return
		}

		if !ip.IsLoopback() {
			if !s.throttle.allow(ip.String()) {
				writeErr(w, http.StatusTooManyRequests, "слишком много неудачных попыток, подождите минуту")
				return
			}
			if !checkToken(r, cfg.Control.Token) {
				s.throttle.fail(ip.String())
				s.deps.Logf("control API: неверный токен с адреса %s", ip)
				w.Header().Set("WWW-Authenticate", `Bearer realm="kidsroom-agent"`)
				writeErr(w, http.StatusUnauthorized, "нужен заголовок Authorization: Bearer <token>")
				return
			}
			s.throttle.ok(ip.String())
		}

		next.ServeHTTP(w, r)
	})
}

func checkToken(r *http.Request, want string) bool {
	if want == "" {
		return false
	}
	got := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(got) <= len(prefix) || !strings.EqualFold(got[:len(prefix)], prefix) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(strings.TrimSpace(got[len(prefix):])), []byte(want)) == 1
}

func remoteIP(r *http.Request) net.IP {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	// X-Forwarded-For is deliberately ignored: the agent is contacted directly,
	// and trusting the header would let anyone spoof an allowed subnet.
	return net.ParseIP(host)
}

func allowed(ip net.IP, cidrs []string) bool {
	for _, c := range cidrs {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			continue
		}
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "только GET")
		return
	}
	writeJSON(w, http.StatusOK, s.deps.Status())
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, s.deps.Settings().Masked())
	case http.MethodPost, http.MethodPut:
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
		if err != nil {
			writeErr(w, http.StatusBadRequest, "не прочитать тело запроса: "+err.Error())
			return
		}
		incoming, err := decodeSettings(s.deps.Settings(), body)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := s.deps.Apply(incoming); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		s.deps.Logf("настройки применены через control API (%s)", r.RemoteAddr)
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":       true,
			"settings": s.deps.Settings().Masked(),
		})
	default:
		writeErr(w, http.StatusMethodNotAllowed, "только GET или POST")
	}
}

// decodeSettings turns a settings document into the full configuration to apply.
//
// An absent key means "keep what is stored", so the body is decoded over a copy
// of the live configuration instead of into a zero value: encoding/json touches
// only the fields the document actually carries. Decoding into a zero Config
// would make a one-line patch — the way a fleet tool pushes a single certificate
// to every site — wipe the camera list, the domain and every boolean the body
// did not mention.
func decodeSettings(current *config.Config, body []byte) (*config.Config, error) {
	var present map[string]json.RawMessage
	if err := json.Unmarshal(body, &present); err != nil {
		return nil, errors.New("не разобрать JSON: " + err.Error())
	}

	next := current.Clone()
	// A camera list in the body replaces the stored one outright. Without this
	// the decoder would lay the incoming cameras over the stored ones element by
	// element, and a camera that omits a field would silently inherit it from
	// whatever used to occupy its position.
	if _, ok := present["cameras"]; ok {
		next.Cameras = nil
	}
	if err := json.Unmarshal(body, next); err != nil {
		return nil, errors.New("не разобрать JSON: " + err.Error())
	}
	return next, nil
}

func (s *Server) handleRestart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "только POST")
		return
	}
	s.deps.Restart()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleToken hands the token to the local settings window, which has to show
// it so the operator can configure a remote client. It answers loopback only:
// over the network the token is something you present, not something you ask for.
func (s *Server) handleToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "только GET")
		return
	}
	if ip := remoteIP(r); ip == nil || !ip.IsLoopback() {
		writeErr(w, http.StatusForbidden, "токен выдаётся только локально")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"token": s.deps.Settings().Control.Token})
}

func (s *Server) handleRegenerateToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "только POST")
		return
	}
	token, err := s.deps.RegenerateToken()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.deps.Logf("токен control API перегенерирован (%s)", r.RemoteAddr)
	// The new token is returned once, in full: there is no other way to learn it.
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "token": token})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]any{"ok": false, "error": msg})
}

// throttle limits failed authentication attempts per remote address.
type throttle struct {
	limit  int
	window time.Duration

	mu    sync.Mutex
	fails map[string]*entry
}

type entry struct {
	count int
	until time.Time
}

func newThrottle(limit int, window time.Duration) *throttle {
	return &throttle{limit: limit, window: window, fails: map[string]*entry{}}
}

func (t *throttle) allow(ip string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	e, ok := t.fails[ip]
	if !ok {
		return true
	}
	if time.Now().After(e.until) {
		delete(t.fails, ip)
		return true
	}
	return e.count < t.limit
}

func (t *throttle) fail(ip string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	e, ok := t.fails[ip]
	if !ok || time.Now().After(e.until) {
		t.fails[ip] = &entry{count: 1, until: time.Now().Add(t.window)}
		return
	}
	e.count++
	// Keep the map from growing without bound on a long-lived service.
	if len(t.fails) > 1024 {
		for k, v := range t.fails {
			if time.Now().After(v.until) {
				delete(t.fails, k)
			}
		}
	}
}

func (t *throttle) ok(ip string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.fails, ip)
}

// Package httpd serves the public side of the agent: the editable player page,
// the camera list and the HLS reverse proxy. Nothing administrative lives here.
package httpd

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"kidsroom-agent/internal/certs"
	"kidsroom-agent/internal/config"
	"kidsroom-agent/internal/mediamtx"
)

// Server is the public TLS endpoint. The certificate and the camera list can
// both be swapped at runtime without dropping the listener.
type Server struct {
	logf func(string, ...any)

	mu  sync.RWMutex
	cfg *config.Config

	cert atomic.Pointer[tls.Certificate]

	srvMu sync.Mutex
	srv   *http.Server
	ln    net.Listener
	port  int

	proxy *hlsProxy
}

// New creates the server for the given settings.
func New(cfg *config.Config, logf func(string, ...any)) *Server {
	s := &Server{cfg: cfg.Clone(), logf: logf}
	s.proxy = newHLSProxy(s.isKnownCamera, s.hlsUpstream, logf)
	return s
}

// SetCertificate installs the certificate served to new TLS handshakes.
func (s *Server) SetCertificate(pair *tls.Certificate) {
	s.cert.Store(pair)
	info := certs.Describe(pair)
	s.logf("сертификат установлен: subject=%q истекает %s (через %d дн.)",
		info.Subject, info.NotAfter.Format("2006-01-02"), info.DaysLeft)
}

// CertificateInfo describes the certificate currently in use.
func (s *Server) CertificateInfo() certs.Info {
	return certs.Describe(s.cert.Load())
}

// CertificateCurrent returns the live certificate, shared with the control API
// so both listeners always present the same identity.
func (s *Server) CertificateCurrent() *tls.Certificate {
	return s.cert.Load()
}

// Reload swaps the settings. When the port changes the listener is recreated;
// everything else (cameras, web dir) takes effect on the next request.
func (s *Server) Reload(cfg *config.Config) error {
	s.mu.Lock()
	s.cfg = cfg.Clone()
	s.mu.Unlock()

	s.srvMu.Lock()
	samePort := s.port == cfg.ListenPort
	s.srvMu.Unlock()
	if samePort {
		return nil
	}
	s.logf("порт публичного сервера меняется на %d, пересоздаю слушатель", cfg.ListenPort)
	return s.relisten(cfg.ListenPort)
}

func (s *Server) snapshot() *config.Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}

// Start binds the public port and serves until ctx is cancelled.
func (s *Server) Start(ctx context.Context) error {
	if err := s.relisten(s.snapshot().ListenPort); err != nil {
		return err
	}
	go func() {
		<-ctx.Done()
		s.Stop()
	}()
	return nil
}

func (s *Server) relisten(port int) error {
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return fmt.Errorf("не удалось занять порт %d: %w", port, err)
	}

	srv := &http.Server{
		Handler:           s.handler(),
		ReadHeaderTimeout: 10 * time.Second,
		// No write deadline: HLS segment delivery and low-latency blocking
		// requests must not be cut off mid-stream.
		IdleTimeout: 120 * time.Second,
		TLSConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
			GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
				if pair := s.cert.Load(); pair != nil {
					return pair, nil
				}
				return nil, errors.New("сертификат ещё не загружен")
			},
		},
	}

	s.srvMu.Lock()
	old, oldLn := s.srv, s.ln
	s.srv, s.ln, s.port = srv, ln, port
	s.srvMu.Unlock()

	if old != nil {
		_ = old.Close()
		if oldLn != nil {
			_ = oldLn.Close()
		}
	}

	go func() {
		err := srv.ServeTLS(ln, "", "")
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.logf("публичный сервер остановлен с ошибкой: %v", err)
		}
	}()
	s.logf("публичный HTTPS-сервер слушает :%d", port)
	return nil
}

// Stop closes the listener.
func (s *Server) Stop() {
	s.srvMu.Lock()
	srv, ln := s.srv, s.ln
	s.srv, s.ln = nil, nil
	s.srvMu.Unlock()

	if srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}
	if ln != nil {
		_ = ln.Close()
	}
}

// Port reports the port currently bound.
func (s *Server) Port() int {
	s.srvMu.Lock()
	defer s.srvMu.Unlock()
	return s.port
}

func (s *Server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/cameras.json", s.handleCameras)
	mux.Handle("/hls/", http.StripPrefix("/hls/", s.proxy))
	mux.HandleFunc("/", s.handleStatic)
	return securityHeaders(mux)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

type cameraDTO struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// handleCameras is the only dynamic response on the public port. It carries
// names and ids only — never a source URL or credentials.
func (s *Server) handleCameras(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	cams := s.snapshot().EnabledCameras()
	out := make([]cameraDTO, 0, len(cams))
	for _, c := range cams {
		out = append(out, cameraDTO{ID: c.ID, Name: c.Name})
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(out)
}

// hlsUpstream is the loopback address mediamtx serves HLS on.
func (s *Server) hlsUpstream() string {
	if addr := s.snapshot().Mediamtx.HLSAddr; addr != "" {
		return addr
	}
	return mediamtx.DefaultHLSAddr
}

func (s *Server) isKnownCamera(id string) bool {
	for _, c := range s.snapshot().EnabledCameras() {
		if c.ID == id {
			return true
		}
	}
	return false
}

// contentTypes pins the types that matter, because Windows resolves MIME types
// through the registry, where .js is sometimes mapped to text/plain.
var contentTypes = map[string]string{
	".html":  "text/html; charset=utf-8",
	".css":   "text/css; charset=utf-8",
	".js":    "application/javascript; charset=utf-8",
	".json":  "application/json; charset=utf-8",
	".svg":   "image/svg+xml",
	".png":   "image/png",
	".jpg":   "image/jpeg",
	".ico":   "image/x-icon",
	".woff2": "font/woff2",
}

// handleStatic serves the editable web directory straight from disk, so an
// edit to index.html shows up on the next refresh with no restart.
func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	root := s.snapshot().WebDir
	upath := path.Clean("/" + strings.TrimPrefix(r.URL.Path, "/"))
	if upath == "/" {
		upath = "/index.html"
	}

	full := filepath.Join(root, filepath.FromSlash(upath))
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	fullAbs, err := filepath.Abs(full)
	if err != nil || !withinRoot(rootAbs, fullAbs) {
		http.NotFound(w, r)
		return
	}

	info, err := os.Stat(fullAbs)
	if err != nil || info.IsDir() {
		// Directory listings stay off: the web directory is public by design,
		// but enumerating it is not useful to a viewer.
		http.NotFound(w, r)
		return
	}

	f, err := os.Open(fullAbs)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()

	ext := strings.ToLower(filepath.Ext(fullAbs))
	if ct, ok := contentTypes[ext]; ok {
		w.Header().Set("Content-Type", ct)
	} else if ct := mime.TypeByExtension(ext); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	if ext == ".html" {
		w.Header().Set("Cache-Control", "no-cache")
	} else {
		w.Header().Set("Cache-Control", "max-age=60")
	}
	http.ServeContent(w, r, info.Name(), info.ModTime(), f)
}

func withinRoot(root, candidate string) bool {
	if strings.EqualFold(root, candidate) {
		return true
	}
	prefix := root
	if !strings.HasSuffix(prefix, string(os.PathSeparator)) {
		prefix += string(os.PathSeparator)
	}
	return strings.HasPrefix(strings.ToLower(candidate), strings.ToLower(prefix))
}

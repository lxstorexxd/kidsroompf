package hub

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// maxResponse caps what the hub reads from an agent. A settings document with a
// certificate chain is kilobytes; anything far larger means something else is
// answering on that port.
const maxResponse = 4 << 20

// Client talks to one agent's control API.
type Client struct {
	site Site
	http *http.Client

	mu   sync.Mutex
	seen string // SHA-256 of the certificate the agent presented
}

// NewClient builds a client for a site.
//
// The agent's certificate is deliberately not verified against a name: the hub
// reaches a site by its VPN address while the certificate is issued for the
// public domain, so a name check would fail on every site in the fleet. When
// the inventory carries a fingerprint it is enforced instead, which is the same
// trade SSH makes.
func NewClient(site Site, timeout time.Duration) *Client {
	c := &Client{site: site}
	c.http = &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				MinVersion:         tls.VersionTLS12,
				InsecureSkipVerify: true,
				VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
					if len(rawCerts) == 0 {
						return errors.New("площадка не предъявила сертификат")
					}
					sum := sha256.Sum256(rawCerts[0])
					got := hex.EncodeToString(sum[:])

					c.mu.Lock()
					c.seen = got
					c.mu.Unlock()

					want := normaliseFingerprint(site.Fingerprint)
					if want == "" {
						return nil
					}
					if got != want {
						return fmt.Errorf("отпечаток сертификата не совпадает с записанным в инвентаре:\n  получен %s\n  ожидался %s", got, want)
					}
					return nil
				},
			},
			// One command touches a site once or twice; keeping connections
			// around for 50 sites buys nothing.
			DisableKeepAlives: true,
		},
	}
	return c
}

// Fingerprint is the SHA-256 of the certificate the agent presented, available
// after the first request. It is empty for a plain-HTTP site.
func (c *Client) Fingerprint() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.seen
}

// normaliseFingerprint accepts the colon-separated form that openssl prints as
// well as plain hex, in either case.
func normaliseFingerprint(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, ":", "")
	s = strings.TrimPrefix(s, "sha256-")
	return strings.TrimSpace(s)
}

// do performs one request and returns the answer verbatim alongside whatever
// out was decoded from, so a caller that needs the agent's exact words can keep
// them.
func (c *Client) do(method, path string, body []byte, out any) ([]byte, error) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, strings.TrimRight(c.site.Addr, "/")+path, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.site.Token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", c.site.Addr, unwrapTransport(err))
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse))
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		return raw, httpError(resp.StatusCode, raw)
	}
	if out == nil {
		return raw, nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return raw, fmt.Errorf("ответ площадки не разобрать: %w", err)
	}
	return raw, nil
}

// httpError turns an agent's refusal into one actionable line. The agent always
// answers errors as {"ok":false,"error":"..."}; a different body means
// something other than an agent is listening there.
func httpError(code int, raw []byte) error {
	var e struct {
		Error string `json:"error"`
	}
	msg := ""
	if json.Unmarshal(raw, &e) == nil {
		msg = strings.TrimSpace(e.Error)
	}

	switch code {
	case http.StatusUnauthorized:
		return errors.New("401: токен не принят — проверьте его в инвентаре и на площадке (kidsroom-agent.exe token)")
	case http.StatusForbidden:
		return errors.New("403: адрес этой машины не входит в control.allow_cidrs на площадке")
	case http.StatusTooManyRequests:
		return errors.New("429: площадка временно блокирует запросы после неудачных попыток входа, подождите минуту")
	}
	if msg != "" {
		return fmt.Errorf("код %d: %s", code, msg)
	}
	return fmt.Errorf("код %d", code)
}

// unwrapTransport strips the url.Error wrapper so the printed line is the
// reason, not the repeated request URL.
func unwrapTransport(err error) error {
	var ue interface{ Unwrap() error }
	if errors.As(err, &ue) {
		if inner := ue.Unwrap(); inner != nil {
			return inner
		}
	}
	return err
}

// Status is the part of the agent's status report the hub shows. It is a
// separate type on purpose: the hub must keep working against an agent whose
// schema has moved on, so it decodes the fields it needs and ignores the rest.
type Status struct {
	Version     string `json:"version"`
	Uptime      string `json:"uptime"`
	PublicURL   string `json:"public_url"`
	Domain      string `json:"domain"`
	ListenPort  int    `json:"listen_port"`
	Certificate struct {
		Subject   string    `json:"subject"`
		Issuer    string    `json:"issuer"`
		Hosts     []string  `json:"hosts"`
		NotAfter  time.Time `json:"not_after"`
		DaysLeft  int       `json:"days_left"`
		SelfSign  bool      `json:"self_signed"`
		Available bool      `json:"available"`
	} `json:"certificate"`
	Stream struct {
		Running   bool   `json:"running"`
		PID       int    `json:"pid"`
		Uptime    string `json:"uptime"`
		Restarts  int    `json:"restarts"`
		LastError string `json:"last_error"`
	} `json:"stream"`
	Cameras []struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Enabled bool   `json:"enabled"`
		Ready   bool   `json:"ready"`
		Readers int    `json:"readers"`
	} `json:"cameras"`
	Warnings []string `json:"warnings"`
	Mediamtx struct {
		Present  bool   `json:"present"`
		Version  string `json:"version"`
		Expected string `json:"expected"`
	} `json:"mediamtx_binary"`

	// raw is the agent's answer as it arrived. --json hands this through
	// untouched, so a field this build of the hub never heard of still reaches
	// whatever reads the output.
	raw []byte
}

// Raw is the agent's status answer exactly as it arrived.
func (s *Status) Raw() []byte { return s.raw }

// CamerasReady counts the enabled cameras that actually deliver video.
func (s *Status) CamerasReady() (ready, enabled int) {
	for _, c := range s.Cameras {
		if !c.Enabled {
			continue
		}
		enabled++
		if c.Ready {
			ready++
		}
	}
	return ready, enabled
}

// Status fetches the agent's status report.
func (c *Client) Status() (*Status, error) {
	var st Status
	raw, err := c.do(http.MethodGet, "/api/status", nil, &st)
	if err != nil {
		return nil, err
	}
	st.raw = raw
	return &st, nil
}

// Settings fetches the settings document with secrets masked, as a raw JSON
// tree. Keeping it untyped is what lets the hub edit one field of a site whose
// agent knows settings this build has never heard of.
func (c *Client) Settings() (Doc, error) {
	var doc Doc
	if _, err := c.do(http.MethodGet, "/api/settings", nil, &doc); err != nil {
		return nil, err
	}
	return doc, nil
}

// SettingsRaw fetches the settings document verbatim, for `get` to write out.
func (c *Client) SettingsRaw() ([]byte, error) {
	raw, err := c.do(http.MethodGet, "/api/settings", nil, nil)
	if err != nil {
		return nil, err
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, raw, "", "  "); err != nil {
		return raw, nil
	}
	return append(pretty.Bytes(), '\n'), nil
}

// Apply sends a settings document. Keys it leaves out keep their stored value,
// so a one-field patch is a safe thing to send.
func (c *Client) Apply(body []byte) error {
	_, err := c.do(http.MethodPost, "/api/settings", body, nil)
	return err
}

// Restart restarts the stream without touching the listeners.
func (c *Client) Restart() error {
	_, err := c.do(http.MethodPost, "/api/restart", nil, nil)
	return err
}

// RotateToken asks the agent for a new control token and returns it. The caller
// must store it: the old one stops working the moment this returns.
func (c *Client) RotateToken() (string, error) {
	var out struct {
		Token string `json:"token"`
	}
	if _, err := c.do(http.MethodPost, "/api/token/regenerate", nil, &out); err != nil {
		return "", err
	}
	if out.Token == "" {
		return "", errors.New("площадка не вернула новый токен")
	}
	return out.Token, nil
}

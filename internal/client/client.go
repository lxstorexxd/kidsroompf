// Package client talks to the local control API. The tray and the settings
// window use it instead of touching config.json, so there is exactly one code
// path that changes settings.
package client

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"kidsroom-agent/internal/app"
	"kidsroom-agent/internal/config"
)

// DefaultURL is used when the agent has not written its endpoint hint yet.
const DefaultURL = "https://127.0.0.1:8081"

type Client struct {
	base string
	http *http.Client
}

// New reads the endpoint hint left by the running agent and returns a client
// for it. Requests go to loopback, where the API accepts them without a token.
func New() *Client {
	base := DefaultURL
	if raw, err := os.ReadFile(app.EndpointFile()); err == nil {
		if s := strings.TrimSpace(string(raw)); s != "" {
			base = s
		}
	}
	return &Client{
		base: base,
		http: &http.Client{
			Timeout: 10 * time.Second,
			Transport: &http.Transport{
				// The agent's certificate is issued for the domain, not for
				// 127.0.0.1, and this connection never leaves the machine.
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			},
		},
	}
}

// URL is the base address in use.
func (c *Client) URL() string { return c.base }

func (c *Client) do(method, path string, body any, out any) error {
	var rdr io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(buf)
	}
	req, err := http.NewRequest(method, c.base+path, rdr)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("агент не отвечает (%s): %w", c.base, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(raw, &e) == nil && e.Error != "" {
			return fmt.Errorf("%s", e.Error)
		}
		return fmt.Errorf("агент ответил кодом %d", resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(raw, out)
}

// Status fetches the agent status report.
func (c *Client) Status() (*app.Status, error) {
	var st app.Status
	if err := c.do(http.MethodGet, "/api/status", nil, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

// Settings fetches the current settings with secrets masked.
func (c *Client) Settings() (*config.Config, error) {
	var cfg config.Config
	if err := c.do(http.MethodGet, "/api/settings", nil, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// Save pushes settings. Masked values are kept by the agent.
func (c *Client) Save(cfg *config.Config) error {
	return c.do(http.MethodPost, "/api/settings", cfg, nil)
}

// Restart restarts the stream.
func (c *Client) Restart() error {
	return c.do(http.MethodPost, "/api/restart", nil, nil)
}

// Token reads the control token (loopback only).
func (c *Client) Token() (string, error) {
	var out struct {
		Token string `json:"token"`
	}
	if err := c.do(http.MethodGet, "/api/token", nil, &out); err != nil {
		return "", err
	}
	return out.Token, nil
}

// RegenerateToken issues a new control token and returns it.
func (c *Client) RegenerateToken() (string, error) {
	var out struct {
		Token string `json:"token"`
	}
	if err := c.do(http.MethodPost, "/api/token/regenerate", nil, &out); err != nil {
		return "", err
	}
	return out.Token, nil
}

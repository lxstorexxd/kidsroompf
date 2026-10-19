package httpd

import (
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"
)

// hlsProxy forwards /hls/<camera>/... to the loopback HLS server of mediamtx.
// Only ids of enabled cameras pass, so the internal path namespace cannot be
// enumerated from the outside.
type hlsProxy struct {
	known func(id string) bool
	rp    *httputil.ReverseProxy
}

// newHLSProxy forwards to whatever address mediamtx currently listens on;
// upstream is read per request because the port is configurable and can change
// while the agent runs.
func newHLSProxy(known func(string) bool, upstream func() string, logf func(string, ...any)) *hlsProxy {
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			target := &url.URL{Scheme: "http", Host: upstream()}
			pr.SetURL(target)
			// mediamtx keys its HLS muxers by Host; a public Host header would
			// not resolve on the loopback side.
			pr.Out.Host = target.Host
			pr.Out.Header.Del("X-Forwarded-For")
		},
		// Flush as soon as bytes arrive: buffering adds latency to segments and
		// breaks low-latency HLS part delivery entirely.
		FlushInterval: -1,
		Transport: &http.Transport{
			DialContext:         (&net.Dialer{Timeout: 2 * time.Second}).DialContext,
			MaxIdleConnsPerHost: 32,
			IdleConnTimeout:     90 * time.Second,
		},
		// mediamtx 1.21 answers the first HLS request with a redirect to
		// <path>/index.m3u8?cookieCheck=1 and hands out an hlsSession cookie.
		// Those paths are internal, so the redirect has to be lifted into the
		// /hls/ namespace or the browser would land on a 404 and never get a
		// session.
		ModifyResponse: rewriteLocations,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			logf("прокси HLS %s: %v", r.URL.Path, err)
			// The player retries on 503, which is what a brief mediamtx restart
			// should look like from the browser's side.
			http.Error(w, "поток недоступен", http.StatusServiceUnavailable)
		},
	}
	return &hlsProxy{known: known, rp: rp}
}

// rewriteLocations maps mediamtx's own paths back into /hls/<id>/.
//
// The session cookie itself needs no rewriting: mediamtx sends it without a
// Path attribute, so the browser derives the default path from the request URI
// it actually used (/hls/<id>), which is exactly right. An explicit Path is
// still handled, in case a future version starts sending one.
func rewriteLocations(resp *http.Response) error {
	if resp.Request == nil {
		return nil
	}
	id, _, ok := strings.Cut(strings.TrimPrefix(resp.Request.URL.Path, "/"), "/")
	if !ok || id == "" {
		return nil
	}

	if loc := resp.Header.Get("Location"); strings.HasPrefix(loc, "/"+id+"/") {
		resp.Header.Set("Location", "/hls"+loc)
	}

	if cookies := resp.Header.Values("Set-Cookie"); len(cookies) > 0 {
		fixed := make([]string, 0, len(cookies))
		changed := false
		for _, c := range cookies {
			next := rewriteCookiePath(c, id)
			changed = changed || next != c
			fixed = append(fixed, next)
		}
		if changed {
			resp.Header.Del("Set-Cookie")
			for _, c := range fixed {
				resp.Header.Add("Set-Cookie", c)
			}
		}
	}
	return nil
}

func rewriteCookiePath(cookie, id string) string {
	for _, attr := range strings.Split(cookie, ";") {
		trimmed := strings.TrimSpace(attr)
		if !strings.HasPrefix(strings.ToLower(trimmed), "path=") {
			continue
		}
		value := trimmed[len("path="):]
		if value == "/"+id || strings.HasPrefix(value, "/"+id+"/") {
			return strings.Replace(cookie, attr, " Path=/hls"+value, 1)
		}
	}
	return cookie
}

func (p *hlsProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// r.URL.Path arrives without the /hls/ prefix: "cam1/index.m3u8".
	id, rest, found := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
	if !found || id == "" || rest == "" || strings.Contains(rest, "..") {
		http.NotFound(w, r)
		return
	}
	if !p.known(id) {
		http.NotFound(w, r)
		return
	}

	// Cache headers come from mediamtx, which already marks playlists
	// no-cache; setting our own here would only duplicate the header.
	r.URL.Path = "/" + id + "/" + rest
	p.rp.ServeHTTP(w, r)
}

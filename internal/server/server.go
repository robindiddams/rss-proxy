// Package server wires HTTP routing and server lifecycle.
package server

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/robindiddams/rss-proxy/internal/config"
	"github.com/robindiddams/rss-proxy/internal/media"
	"github.com/robindiddams/rss-proxy/internal/policy"
	"github.com/robindiddams/rss-proxy/internal/proxyurl"
	"github.com/robindiddams/rss-proxy/internal/rss"
	"github.com/robindiddams/rss-proxy/internal/upstream"
)

// Server holds composed handlers and the underlying http.Server.
type Server struct {
	Config   *config.Config
	Policy   *policy.Policy
	Client   *upstream.Client
	Proxy    *proxyurl.Generator
	Rewriter  *rss.Rewriter
	Media     *media.Handler
	ProxyBase *url.URL
	Logf      func(format string, args ...any)
	httpSrv   *http.Server
}

// New constructs a Server from validated config.
func New(cfg *config.Config, logf func(string, ...any)) (*Server, error) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	pol := &policy.Policy{
		AllowPrivateIPs:   cfg.AllowPrivateIPs,
		AllowHTTPUpstream: cfg.AllowHTTPUpstream,
		AllowHosts:        cfg.UpstreamAllow,
	}
	proxy, err := proxyurl.New(cfg.PublicBaseURL)
	if err != nil {
		return nil, fmt.Errorf("proxy url: %w", err)
	}
	client := upstream.New(pol, cfg.UserAgent, cfg.FeedTimeout)
	rw := &rss.Rewriter{Policy: pol, Proxy: proxy, Logf: logf}
	s := &Server{
		Config: cfg, Policy: pol, Client: client, Proxy: proxy, ProxyBase: proxy.Base(),
		Rewriter: rw, Media: &media.Handler{Client: client}, Logf: logf,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.index)
	mux.HandleFunc("/healthz", s.health)
	mux.HandleFunc("/rss/untls", s.feed)
	mux.Handle("/media/untls", s.Media)
	mux.Handle("/media/untls/", s.Media)
	s.httpSrv = &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           s.logRequests(mux),
		ReadHeaderTimeout: 10 * time.Second,
	}
	return s, nil
}

// Handler returns the root HTTP handler (useful for tests / custom servers).
func (s *Server) Handler() http.Handler {
	return s.httpSrv.Handler
}

// ListenAndServe starts the HTTP server.
func (s *Server) ListenAndServe() error {
	s.Logf("rss-proxy listening on %s, public base %s", s.Config.ListenAddr, s.Config.PublicBaseURL)
	return s.httpSrv.ListenAndServe()
}

// Shutdown gracefully stops the server.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.httpSrv.Shutdown(ctx)
}

func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	feedURL := r.URL.Query().Get("url")
	feedURL = strings.TrimSpace(feedURL)
	var result string
	if feedURL != "" {
		q := url.Values{}
		q.Set("url", feedURL)
		u := *s.ProxyBase
		u.Path = strings.TrimSuffix(s.ProxyBase.Path, "/") + "/rss/untls"
		u.RawQuery = q.Encode()
		result = u.String()
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(indexPage(result, feedURL)))
}

// logRequests wraps h with per-request logging: method, path, remote addr,
// status, size, and duration.
func (s *Server) logRequests(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		h.ServeHTTP(rw, r)
		s.Logf("%s %s %s -> %d %dB %s",
			clientIP(r), r.Method, r.URL.RequestURI(),
			rw.status, rw.size, time.Since(start).Round(time.Millisecond))
	})
}

func clientIP(r *http.Request) string {
	if ip := r.RemoteAddr; ip != "" {
		return ip
	}
	return "?"
}

type statusRecorder struct {
	http.ResponseWriter
	status int
	size   int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	n, err := r.ResponseWriter.Write(b)
	r.size += n
	return n, err
}

func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodGet {
		_, _ = w.Write([]byte("ok\n"))
	}
}

// feedRequestHeaders forwarded to the upstream feed for conditional requests.
var feedForwardHeaders = []string{"If-Modified-Since", "If-None-Match", "If-Range"}

// feedForwardResponseHeaders preserved after rewriting (Content-Length recalculated).
var feedForwardResponseHeaders = []string{
	"ETag", "Last-Modified", "Cache-Control", "Expires",
}

func (s *Server) feed(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	raw := r.URL.Query().Get("url")
	if raw == "" {
		http.Error(w, "missing url parameter", http.StatusBadRequest)
		return
	}

	reqHeaders := http.Header{}
	for _, k := range feedForwardHeaders {
		if v := r.Header.Get(k); v != "" {
			reqHeaders.Set(k, v)
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.Config.FeedTimeout)
	defer cancel()

	// Always fetch upstream as GET: HEAD upstream would give an empty body,
	// so we could not compute the rewritten Content-Length. For a downstream
	// HEAD we suppress only the body after computing the same headers as GET.
	resp, err := s.Client.Fetch(ctx, http.MethodGet, raw, reqHeaders)
	if err != nil {
		s.Logf("feed: upstream error for %s: %v", raw, err)
		http.Error(w, "upstream unavailable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// Preserve a clean upstream 304: nothing to rewrite.
	if resp.StatusCode == http.StatusNotModified {
		for _, k := range feedForwardResponseHeaders {
			if v := resp.Header.Get(k); v != "" {
				w.Header().Set(k, v)
			}
		}
		w.WriteHeader(http.StatusNotModified)
		return
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		http.Error(w, "upstream feed status "+strconv.Itoa(resp.StatusCode), http.StatusBadGateway)
		return
	}

	body, err := upstream.LimitedBody(resp.Body, s.Config.MaxFeedBytes)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	result, err := s.Rewriter.Rewrite(body)
	if err != nil {
		s.Logf("feed: xml/rewrite error for %s: %v", raw, err)
		http.Error(w, "upstream feed malformed", http.StatusBadGateway)
		return
	}

	for _, k := range feedForwardResponseHeaders {
		if v := resp.Header.Get(k); v != "" {
			w.Header().Set(k, v)
		}
	}
	w.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(result.Bytes)))
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodGet {
		_, _ = w.Write(result.Bytes)
	}
}

func indexPage(result, feedURL string) string {
	res := ""
	if result != "" {
		res = `<p class="result"><label>Subscribe URL (paste into iTunes 10.4):</label>` +
			`<input readonly value="` + htmlEscape(result) + `" onclick="this.select()">` +
			`<button onclick="navigator.clipboard.writeText(this.previousElementSibling.value)">Copy</button>` +
			`</p>`
	}
	return `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>rss-proxy</title>
<style>
body{font:15px/1.5 -apple-system,BlinkMacSystemFont,Segoe UI,Helvetica,Arial,sans-serif;max-width:720px;margin:32px auto;padding:0 16px;color:#222}
h1{font-size:20px;margin:0 0 8px}
p.hint{color:#666;margin:0 0 24px}
label{display:block;font-weight:600;margin:8px 0}
input[type=text]{width:100%;box-sizing:border-box;padding:8px;font:inherit;border:1px solid #ccc;border-radius:4px}
button{padding:6px 14px;font:inherit;border:1px solid #888;background:#f4f4f4;border-radius:4px;cursor:pointer}
button:hover{background:#eee}
.result{margin-top:24px;background:#f7f7f9;border:1px solid #ddd;border-radius:6px;padding:12px}
.result input{font-family:ui-monospace,SFMono-Regular,Menlo,monospace;font-size:13px}
</style>
</head>
<body>
<h1>Podcast HTTPS&rarr;HTTP Proxy</h1>
<p class="hint">Enter a modern HTTPS podcast feed URL. You'll get a plain-HTTP URL your legacy client can subscribe to.</p>
<form method="get" action="/">
<label for="url">Upstream feed URL (https)</label>
<input id="url" name="url" type="text" placeholder="https://feeds.megaphone.fm/TBIEA9794787572" value="` + htmlEscape(feedURL) + `">
<button type="submit">Generate</button>
</form>
` + res + `
</body>
</html>`
}

func htmlEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, `"`, "&quot;")
	return s
}

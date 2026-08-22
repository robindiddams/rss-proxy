package server_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/robindiddams/rss-proxy/internal/config"
	"github.com/robindiddams/rss-proxy/internal/server"
)

func newServer(t *testing.T, baseURL string, opts ...func(*config.Config)) *server.Server {
	t.Helper()
	cfg := config.Config{
		ListenAddr:        ":0",
		PublicBaseURL:     baseURL,
		FeedTimeout:       5 * time.Second,
		MaxFeedBytes:      1 * 1024 * 1024,
		UserAgent:         "rss-proxy-test",
		AllowPrivateIPs:   true, // httptest listens on loopback
		AllowHTTPUpstream: true, // httptest is http
	}
	for _, o := range opts {
		o(&cfg)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	s, err := server.New(&cfg, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func do(t *testing.T, h http.Handler, method, target string, headers http.Header) *http.Response {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	for k, vs := range headers {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr.Result()
}

// ---- feed endpoint: methods ----

func TestFeed_MethodNotAllowed(t *testing.T) {
	s := newServer(t, "http://h:1")
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		resp := do(t, s.Handler(), m, "/rss/untls?url=https://x", nil)
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("%s: got %d, want 405", m, resp.StatusCode)
		}
		if resp.Header.Get("Allow") != "GET, HEAD" {
			t.Errorf("%s: Allow = %q", m, resp.Header.Get("Allow"))
		}
	}
}

func TestFeed_MissingURL(t *testing.T) {
	s := newServer(t, "http://h:1")
	resp := do(t, s.Handler(), http.MethodGet, "/rss/untls", nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("got %d, want 400", resp.StatusCode)
	}
}

func TestFeed_GET_HEAD(t *testing.T) {
	data, _ := os.ReadFile("../../testdata/feed.xml")
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		w.Header().Set("ETag", `"abc"`)
		w.Write(data)
	}))
	defer up.Close()

	s := newServer(t, "http://proxy.invalid")
	target := "/rss/untls?url=" + url.QueryEscape(up.URL+"/feed.xml")

	resp := do(t, s.Handler(), http.MethodGet, target, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "proxy.invalid/media/untls/") {
		t.Errorf("GET body missing rewritten proxy urls")
	}
	if resp.Header.Get("ETag") != `"abc"` {
		t.Errorf("ETag not preserved: %q", resp.Header.Get("ETag"))
	}
	if cl := resp.Header.Get("Content-Length"); cl == "" {
		t.Errorf("Content-Length missing")
	} else if cl != fmt.Sprintf("%d", len(body)) {
		t.Errorf("Content-Length %q != body len %d", cl, len(body))
	}

	// HEAD: same status + headers, no body.
	head := do(t, s.Handler(), http.MethodHead, target, nil)
	if head.StatusCode != http.StatusOK {
		t.Errorf("HEAD got %d", head.StatusCode)
	}
	hb, _ := io.ReadAll(head.Body)
	if len(hb) != 0 {
		t.Errorf("HEAD returned body of %d bytes", len(hb))
	}
	if head.Header.Get("ETag") != `"abc"` {
		t.Errorf("HEAD ETag not preserved")
	}
	if head.Header.Get("Content-Length") != resp.Header.Get("Content-Length") {
		t.Errorf("HEAD Content-Length %q != GET %q", head.Header.Get("Content-Length"), resp.Header.Get("Content-Length"))
	}
	if head.Header.Get("Content-Type") != resp.Header.Get("Content-Type") {
		t.Errorf("HEAD Content-Type %q != GET %q", head.Header.Get("Content-Type"), resp.Header.Get("Content-Type"))
	}
}

func TestFeed_Conditional304(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") == `"abc"` {
			w.Header().Set("ETag", `"abc"`)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"abc"`)
		w.Write([]byte("<rss version='2.0'><channel><title>x</title></channel></rss>"))
	}))
	defer up.Close()
	s := newServer(t, "http://h:1")
	target := "/rss/untls?url=" + url.QueryEscape(up.URL+"/feed.xml")
	resp := do(t, s.Handler(), http.MethodGet, target, http.Header{"If-None-Match": {`"abc"`}})
	if resp.StatusCode != http.StatusNotModified {
		t.Errorf("got %d, want 304", resp.StatusCode)
	}
	if resp.Header.Get("ETag") != `"abc"` {
		t.Errorf("304 should preserve ETag")
	}
}

func TestFeed_MalformedXML_502(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<rss version='2.0'><channel><title>oops</channel></rss>"))
	}))
	defer up.Close()
	s := newServer(t, "http://h:1")
	resp := do(t, s.Handler(), http.MethodGet, "/rss/untls?url="+url.QueryEscape(up.URL), nil)
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("got %d, want 502", resp.StatusCode)
	}
}

func TestFeed_Oversized_502(t *testing.T) {
	big := bytes.Repeat([]byte("x"), 2*1024*1024)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(big)
	}))
	defer up.Close()
	s := newServer(t, "http://h:1", func(c *config.Config) { c.MaxFeedBytes = 1 * 1024 * 1024 })
	resp := do(t, s.Handler(), http.MethodGet, "/rss/untls?url="+url.QueryEscape(up.URL), nil)
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("got %d, want 502", resp.StatusCode)
	}
}

// ---- media endpoint ----

func TestMedia_MethodsAndMissingURL(t *testing.T) {
	s := newServer(t, "http://h:1")
	for _, m := range []string{http.MethodPost, http.MethodPut} {
		resp := do(t, s.Handler(), m, "/media/untls?url=https://x", nil)
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("%s: got %d", m, resp.StatusCode)
		}
	}
	resp := do(t, s.Handler(), http.MethodGet, "/media/untls", nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("missing url: got %d", resp.StatusCode)
	}
}

func TestMedia_StreamingNoWholeFileBuffer(t *testing.T) {
	// Upstream writes in two chunks with a delay; client should receive the
	// first chunk before the second is produced.
	ch := make(chan struct{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/mpeg")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("FIRST-CHUNK"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-ch // block until test signals second chunk
		w.Write([]byte("SECOND-CHUNK"))
	}))
	defer up.Close()
	defer close(ch)
	s := newServer(t, "http://h:1")

	// Use a real http client against an httptest server to observe streaming.
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/media/untls?url="+url.QueryEscape(up.URL), nil)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req = req.WithContext(ctx)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	buf := make([]byte, 11)
	if _, err := io.ReadFull(resp.Body, buf); err != nil {
		t.Fatalf("did not receive first chunk promptly: %v", err)
	}
	if string(buf) != "FIRST-CHUNK" {
		t.Errorf("first chunk = %q", buf)
	}
	// Release the second chunk.
	ch <- struct{}{}
	rest, _ := io.ReadAll(resp.Body)
	if string(rest) != "SECOND-CHUNK" {
		t.Errorf("rest = %q", rest)
	}
}

func TestMedia_Range206(t *testing.T) {
	payload := bytes.Repeat([]byte("ABCDE"), 10) // 50 bytes
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rng := r.Header.Get("Range")
		if rng == "bytes=10-19" {
			w.Header().Set("Content-Range", "bytes 10-19/50")
			w.Header().Set("Content-Length", "10")
			w.Header().Set("Accept-Ranges", "bytes")
			w.WriteHeader(http.StatusPartialContent)
			w.Write(payload[10:20])
			return
		}
		w.Header().Set("Accept-Ranges", "bytes")
		w.WriteHeader(http.StatusOK)
		w.Write(payload)
	}))
	defer up.Close()
	s := newServer(t, "http://h:1")
	resp := do(t, s.Handler(), http.MethodGet, "/media/untls?url="+url.QueryEscape(up.URL),
		http.Header{"Range": {"bytes=10-19"}})
	if resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("got %d, want 206", resp.StatusCode)
	}
	if resp.Header.Get("Content-Range") != "bytes 10-19/50" {
		t.Errorf("Content-Range = %q", resp.Header.Get("Content-Range"))
	}
	b, _ := io.ReadAll(resp.Body)
	if string(b) != string(payload[10:20]) {
		t.Errorf("body = %q", b)
	}
}

func TestMedia_Unsatisfiable416(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Range", "bytes */50")
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
	}))
	defer up.Close()
	s := newServer(t, "http://h:1")
	resp := do(t, s.Handler(), http.MethodGet, "/media/untls?url="+url.QueryEscape(up.URL),
		http.Header{"Range": {"bytes=1000-2000"}})
	if resp.StatusCode != http.StatusRequestedRangeNotSatisfiable {
		t.Errorf("got %d, want 416", resp.StatusCode)
	}
	if resp.Header.Get("Content-Range") != "bytes */50" {
		t.Errorf("Content-Range not forwarded")
	}
}

func TestMedia_Conditional304(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") == `"v1"` {
			w.Header().Set("ETag", `"v1"`)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Write([]byte("data"))
	}))
	defer up.Close()
	s := newServer(t, "http://h:1")
	resp := do(t, s.Handler(), http.MethodGet, "/media/untls?url="+url.QueryEscape(up.URL),
		http.Header{"If-None-Match": {`"v1"`}})
	if resp.StatusCode != http.StatusNotModified {
		t.Errorf("got %d, want 304", resp.StatusCode)
	}
}

func TestMedia_HEAD(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "42")
		w.Header().Set("Content-Type", "audio/mpeg")
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodGet {
			w.Write(bytes.Repeat([]byte("x"), 42))
		}
	}))
	defer up.Close()
	s := newServer(t, "http://h:1")
	resp := do(t, s.Handler(), http.MethodHead, "/media/untls?url="+url.QueryEscape(up.URL), nil)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("HEAD got %d", resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	if len(b) != 0 {
		t.Errorf("HEAD returned body of %d bytes", len(b))
	}
	if resp.Header.Get("Content-Length") != "42" {
		t.Errorf("Content-Length = %q", resp.Header.Get("Content-Length"))
	}
}

// ---- redirects ----

func TestRedirect_Allowed(t *testing.T) {
	final := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<rss version='2.0'><channel><title>ok</title></channel></rss>"))
	}))
	defer final.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, final.URL+"/feed.xml", http.StatusFound)
	}))
	defer redirector.Close()
	s := newServer(t, "http://h:1")
	resp := do(t, s.Handler(), http.MethodGet, "/rss/untls?url="+url.QueryEscape(redirector.URL), nil)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("got %d, want 200 (followed allowed redirect)", resp.StatusCode)
	}
}

func TestRedirect_BlockedHost(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<rss/>"))
	}))
	defer target.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Redirect to a host that is NOT in the allowlist.
		http.Redirect(w, r, "http://nonallowed.invalid/feed.xml", http.StatusFound)
	}))
	defer redirector.Close()
	s := newServer(t, "http://h:1", func(c *config.Config) {
		c.UpstreamAllow = []string{"127.0.0.1"} // only allow the redirector host
	})
	// Rebuild policy with allowlist; server.New already used config, so we need
	// the allowlist to apply. Since newServer sets config before server.New, this works.
	resp := do(t, s.Handler(), http.MethodGet, "/rss/untls?url="+url.QueryEscape(redirector.URL), nil)
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("got %d, want 502 (blocked host redirect)", resp.StatusCode)
	}
}

func TestRedirect_PrivateAddressRejected(t *testing.T) {
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://127.0.0.1:9/feed.xml", http.StatusFound)
	}))
	defer redirector.Close()
	s := newServer(t, "http://h:1", func(c *config.Config) {
		c.AllowPrivateIPs = false // reject private addresses
		c.UpstreamAllow = []string{"127.0.0.1"}
	})
	resp := do(t, s.Handler(), http.MethodGet, "/rss/untls?url="+url.QueryEscape(redirector.URL), nil)
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("got %d, want 502 (private redirect)", resp.StatusCode)
	}
}

func TestRedirect_Loop(t *testing.T) {
	var loop *httptest.Server
	loop = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, loop.URL+"/again", http.StatusFound)
	}))
	defer loop.Close()
	s := newServer(t, "http://h:1")
	resp := do(t, s.Handler(), http.MethodGet, "/rss/untls?url="+url.QueryEscape(loop.URL), nil)
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("got %d, want 502 (redirect loop)", resp.StatusCode)
	}
}

func TestRedirect_TooMany(t *testing.T) {
	var count int
	var chain *httptest.Server
	chain = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count++
		http.Redirect(w, r, fmt.Sprintf("%s/%d", chain.URL, count), http.StatusFound)
	}))
	defer chain.Close()
	s := newServer(t, "http://h:1")
	resp := do(t, s.Handler(), http.MethodGet, "/rss/untls?url="+url.QueryEscape(chain.URL), nil)
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("got %d, want 502 (too many redirects)", resp.StatusCode)
	}
}

// ---- public base URL with path prefix ----

func TestFeed_PathPrefixBaseURL(t *testing.T) {
	data, _ := os.ReadFile("../../testdata/feed.xml")
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(data)
	}))
	defer up.Close()
	s := newServer(t, "http://proxy.invalid/pfx")
	resp := do(t, s.Handler(), http.MethodGet, "/rss/untls?url="+url.QueryEscape(up.URL), nil)
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "proxy.invalid/pfx/media/untls/") {
		t.Errorf("rewritten urls missing path prefix;\n%s", body)
	}
}

// ---- downstream cancellation during media streaming ----

func TestMedia_DownstreamCancellation(t *testing.T) {
	started := make(chan struct{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		close(started)
		// Keep writing slowly until client disconnects.
		for i := 0; i < 1000; i++ {
			if _, err := w.Write([]byte("x")); err != nil {
				return
			}
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			time.Sleep(5 * time.Millisecond)
		}
	}))
	defer up.Close()
	s := newServer(t, "http://h:1")
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/media/untls?url="+url.QueryEscape(up.URL), nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	<-started
	// Read a byte then cancel.
	buf := make([]byte, 1)
	resp.Body.Read(buf)
	cancel()
	// After cancel, reading should return promptly (error or EOF).
	done := make(chan struct{})
	go func() {
		io.ReadAll(resp.Body)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Error("media stream did not stop promptly after downstream cancel")
	}
}

func TestHealth(t *testing.T) {
	s := newServer(t, "http://h:1")
	resp := do(t, s.Handler(), http.MethodGet, "/healthz", nil)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("got %d", resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(b), "ok") {
		t.Errorf("health body = %q", b)
	}
	head := do(t, s.Handler(), http.MethodHead, "/healthz", nil)
	if head.StatusCode != http.StatusOK {
		t.Errorf("HEAD health got %d", head.StatusCode)
	}
}

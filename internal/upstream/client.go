// Package upstream provides an HTTP client that enforces the upstream policy
// on the initial URL and every redirect target, with loop and depth limits.
package upstream

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/robindiddams/rss-proxy/internal/policy"
)

const maxRedirects = 10

// Client is a policy-enforcing HTTP client.
type Client struct {
	Policy      *policy.Policy
	UserAgent   string
	Timeout     time.Duration
	Transport   *http.Transport
	httpClient  *http.Client
}

// New builds a Client. Timeout applies to the whole fetch (feed use);
// media callers should use a context per request instead.
func New(p *policy.Policy, userAgent string, timeout time.Duration) *Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	policyDial := p.DialContext(dialer)
	tr := &http.Transport{
		DialContext:       policyDial,
		ForceAttemptHTTP2: true,
		MaxIdleConns:      20,
		IdleConnTimeout:   90 * time.Second,
	}
	c := &Client{Policy: p, UserAgent: userAgent, Timeout: timeout, Transport: tr}
	c.httpClient = &http.Client{
		Transport: tr,
		CheckRedirect: c.CheckRedirect,
		Timeout:       0, // per-request control via context
	}
	return c
}

// CheckRedirect is invoked by net/http for each redirect. It validates the
// next URL against policy and tracks visited URLs to detect loops.
func (c *Client) CheckRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirects {
		return fmt.Errorf("upstream: too many redirects (>%d)", maxRedirects)
	}
	if err := c.Policy.CheckURL(req.URL); err != nil {
		return fmt.Errorf("upstream: redirect rejected: %w", err)
	}
	// Loop detection: compare scheme+host+path+query.
	key := req.URL.Scheme + "://" + req.URL.Host + req.URL.Path + "?" + req.URL.RawQuery
	for _, v := range via {
		k := v.URL.Scheme + "://" + v.URL.Host + v.URL.Path + "?" + v.URL.RawQuery
		if k == key {
			return fmt.Errorf("upstream: redirect loop detected at %s", redact(req.URL))
		}
	}
	return nil
}

// Fetch sends a GET/HEAD with policy headers and returns the response. The
// caller is responsible for closing the body and enforcing size limits.
func (c *Client) Fetch(ctx context.Context, method, rawURL string, headers http.Header) (*http.Response, error) {
	u, err := c.Policy.CheckURLString(rawURL)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", c.UserAgent)
	for k, vs := range headers {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("upstream fetch: %w", err)
	}
	return resp, nil
}

// LimitedBody returns a reader that enforces maxBytes and a closer wrapping r.
func LimitedBody(r io.ReadCloser, maxBytes int64) (io.ReadCloser, error) {
	return &limitedBody{r: io.LimitReader(r, maxBytes+1), src: r, max: maxBytes}, nil
}

type limitedBody struct {
	r   io.Reader
	src io.Closer
	max int64
	n   int64
}

func (l *limitedBody) Read(p []byte) (int, error) {
	n, err := l.r.Read(p)
	l.n += int64(n)
	if err == io.EOF {
		if l.n > l.max {
			return n, fmt.Errorf("upstream: response exceeds %d bytes", l.max)
		}
		return n, io.EOF
	}
	if err != nil {
		return n, err
	}
	if l.n > l.max {
		return n, fmt.Errorf("upstream: response exceeds %d bytes", l.max)
	}
	return n, nil
}
func (l *limitedBody) Close() error { return l.src.Close() }

// redact removes credentials from a URL for logging.
func redact(u *url.URL) string {
	if u == nil {
		return ""
	}
	c := *u
	c.User = nil
	return c.String()
}

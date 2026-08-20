package upstream_test

import (
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/robindiddams/rss-proxy/internal/policy"
	"github.com/robindiddams/rss-proxy/internal/upstream"
)

func newClient(t *testing.T, p *policy.Policy) *upstream.Client {
	t.Helper()
	return upstream.New(p, "rss-proxy-test", 5*time.Second)
}

func req(u string) *http.Request {
	return &http.Request{URL: mustURL(u)}
}
func mustURL(s string) *url.URL {
	u, err := url.Parse(s)
	if err != nil {
		panic(err)
	}
	return u
}

func TestCheckRedirect_BlockedHost(t *testing.T) {
	c := newClient(t, &policy.Policy{AllowHosts: []string{"allowed.invalid"}})
	via := []*http.Request{req("https://allowed.invalid/start")}
	err := c.CheckRedirect(req("https://evil.invalid/feed"), via)
	if err == nil {
		t.Fatal("expected redirect to non-allowlisted host to be rejected")
	}
}

func TestCheckRedirect_AllowedHost(t *testing.T) {
	c := newClient(t, &policy.Policy{AllowHosts: []string{"allowed.invalid"}})
	via := []*http.Request{req("https://allowed.invalid/start")}
	if err := c.CheckRedirect(req("https://allowed.invalid/feed"), via); err != nil {
		t.Fatalf("allowed redirect rejected: %v", err)
	}
}

func TestCheckRedirect_Loop(t *testing.T) {
	c := newClient(t, &policy.Policy{})
	via := []*http.Request{
		req("https://up.invalid/a"),
		req("https://up.invalid/b"),
	}
	// redirect back to /a -> loop.
	if err := c.CheckRedirect(req("https://up.invalid/a"), via); err == nil {
		t.Fatal("expected loop detection")
	}
}

func TestCheckRedirect_TooMany(t *testing.T) {
	c := newClient(t, &policy.Policy{})
	via := make([]*http.Request, 0)
	for i := 0; i < 11; i++ {
		via = append(via, req("https://up.invalid/"+string(rune('a'+i))))
	}
	if err := c.CheckRedirect(req("https://up.invalid/z"), via); err == nil {
		t.Fatal("expected redirect limit error")
	}
}

func TestCheckRedirect_HTTPUpstreamRejectedByDefault(t *testing.T) {
	c := newClient(t, &policy.Policy{})
	via := []*http.Request{req("https://up.invalid/start")}
	if err := c.CheckRedirect(req("http://up.invalid/insecure"), via); err == nil {
		t.Fatal("expected http redirect rejected by default")
	}
}

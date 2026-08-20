package policy_test

import (
	"context"
	"net"
	"net/url"
	"strings"
	"testing"

	"github.com/robindiddams/rss-proxy/internal/policy"
)

func TestPolicy_CheckURL_Schemes(t *testing.T) {
	p := &policy.Policy{}
	if _, err := p.CheckURLString("http://example.invalid/x"); err == nil {
		t.Error("http upstream allowed by default")
	}
	if _, err := p.CheckURLString("ftp://example.invalid/x"); err == nil {
		t.Error("ftp allowed")
	}
	if _, err := p.CheckURLString("https://example.invalid/x"); err != nil {
		t.Errorf("https rejected: %v", err)
	}
	p2 := &policy.Policy{AllowHTTPUpstream: true}
	if _, err := p2.CheckURLString("http://example.invalid/x"); err != nil {
		t.Errorf("http upstream rejected when allowed: %v", err)
	}
}

func TestPolicy_CheckURL_Credentials(t *testing.T) {
	p := &policy.Policy{}
	if _, err := p.CheckURLString("https://user:pass@example.invalid/x"); err == nil {
		t.Error("embedded credentials allowed")
	}
}

func TestPolicy_CheckURL_NotAbsolute(t *testing.T) {
	p := &policy.Policy{}
	if _, err := p.CheckURLString("/relative"); err == nil {
		t.Error("relative url allowed")
	}
}

func TestPolicy_HostAllowlist(t *testing.T) {
	p := &policy.Policy{AllowHosts: []string{"megaphone.fm"}}
	cases := []struct {
		host string
		ok   bool
	}{
		{"megaphone.fm", true},
		{"feeds.megaphone.fm", true},
		{"a.b.feeds.megaphone.fm", true},
		{"notmegaphone.fm", false},
		{"megaphone.fm.evil.invalid", false},
		{"MEGAPHONE.FM", true},
	}
	for _, c := range cases {
		u := &url.URL{Scheme: "https", Host: c.host}
		err := p.CheckURL(u)
		if c.ok && err != nil {
			t.Errorf("host %s: expected allowed, got %v", c.host, err)
		}
		if !c.ok && err == nil {
			t.Errorf("host %s: expected rejected", c.host)
		}
	}
}

func TestPolicy_EmptyAllowlistAllowsAnyHost(t *testing.T) {
	p := &policy.Policy{}
	u := &url.URL{Scheme: "https", Host: "anything.invalid"}
	if err := p.CheckURL(u); err != nil {
		t.Errorf("empty allowlist should permit any host: %v", err)
	}
}

func TestPolicy_CheckAddress(t *testing.T) {
	p := &policy.Policy{}
	bad := []string{
		"127.0.0.1", "::1", "10.0.0.1", "172.16.0.1", "192.168.1.1",
		"169.254.1.1", "224.0.0.1", "0.0.0.0", "100.64.0.1", "fe80::1",
		"ff02::1",
	}
	for _, s := range bad {
		if err := p.CheckAddress(net.ParseIP(s)); err == nil {
			t.Errorf("address %s should be rejected", s)
		}
	}
	good := []string{"8.8.8.8", "1.1.1.1", "2606:4700:4700::1111"}
	for _, s := range good {
		if err := p.CheckAddress(net.ParseIP(s)); err != nil {
			t.Errorf("public address %s rejected: %v", s, err)
		}
	}
}

func TestPolicy_MixedDNSResultsRejected(t *testing.T) {
	p := &policy.Policy{}
	mixed := []net.IP{net.ParseIP("8.8.8.8"), net.ParseIP("127.0.0.1")}
	if err := p.CheckResolvedIPs(mixed); err == nil {
		t.Error("mixed DNS results (one private) should be rejected")
	}
	allPublic := []net.IP{net.ParseIP("8.8.8.8"), net.ParseIP("1.1.1.1")}
	if err := p.CheckResolvedIPs(allPublic); err != nil {
		t.Errorf("all-public results rejected: %v", err)
	}
}

func TestPolicy_AllowPrivateIPsSkipsAddressCheck(t *testing.T) {
	p := &policy.Policy{AllowPrivateIPs: true}
	if err := p.CheckAddress(net.ParseIP("127.0.0.1")); err != nil {
		t.Errorf("allow-private-ips should skip: %v", err)
	}
}

func TestPolicy_CGNATRange(t *testing.T) {
	p := &policy.Policy{}
	for _, s := range []string{"100.64.0.1", "100.127.255.254"} {
		if err := p.CheckAddress(net.ParseIP(s)); err == nil {
			t.Errorf("CGNAT %s should be rejected", s)
		}
	}
	for _, s := range []string{"100.63.255.255", "100.128.0.0"} {
		if err := p.CheckAddress(net.ParseIP(s)); err != nil {
			t.Errorf("non-CGNAT %s rejected: %v", s, err)
		}
	}
}

func TestPolicy_DialContext_RejectsLoopback(t *testing.T) {
	p := &policy.Policy{}
	dial := p.DialContext(&net.Dialer{})
	_, err := dial(context.Background(), "tcp", "127.0.0.1:80")
	if err == nil {
		t.Error("dial to 127.0.0.1 should be rejected")
	}
}

func TestPolicy_DialContext_AllowPrivateIPs(t *testing.T) {
	p := &policy.Policy{AllowPrivateIPs: true}
	dial := p.DialContext(&net.Dialer{})
	_, err := dial(context.Background(), "tcp", "127.0.0.1:1")
	if err != nil && containsPolicyRej(err) {
		t.Errorf("allow-private-ips dial rejected by policy: %v", err)
	}
}

func containsPolicyRej(err error) bool {
	return strings.Contains(err.Error(), "policy:")
}

package config_test

import (
	"strings"
	"testing"
	"time"

	"github.com/robindiddams/rss-proxy/internal/config"
)

func load(t *testing.T, env map[string]string, args ...string) (config.Config, error) {
	t.Helper()
	get := func(k string) string { return env[k] }
	return config.Load(args, get)
}

func TestConfig_DefaultsValid(t *testing.T) {
	c, err := load(t, map[string]string{"RSS_PROXY_PUBLIC_BASE_URL": "http://localhost:8080"})
	if err != nil {
		t.Fatal(err)
	}
	if c.PublicBaseURL != "http://localhost:8080/" {
		t.Errorf("public base url normalized = %q", c.PublicBaseURL)
	}
	if c.ListenAddr != ":8080" {
		t.Errorf("listen = %q", c.ListenAddr)
	}
	if c.FeedTimeout <= 0 || c.MaxFeedBytes <= 0 {
		t.Errorf("limits must be positive: timeout=%v max=%d", c.FeedTimeout, c.MaxFeedBytes)
	}
}

func TestConfig_RejectsHTTPSPublicBaseURL(t *testing.T) {
	_, err := load(t, map[string]string{"RSS_PROXY_PUBLIC_BASE_URL": "https://localhost:8080"})
	if err == nil || !strings.Contains(err.Error(), "http scheme") {
		t.Fatalf("expected http-scheme rejection, got %v", err)
	}
}

func TestConfig_RejectsMissingPublicBaseURL(t *testing.T) {
	_, err := load(t, map[string]string{})
	if err == nil || !strings.Contains(err.Error(), "public-base-url") {
		t.Fatalf("expected missing public-base-url error, got %v", err)
	}
}

func TestConfig_RejectsNonPositiveLimits(t *testing.T) {
	env := map[string]string{
		"RSS_PROXY_PUBLIC_BASE_URL":   "http://localhost:8080",
		"RSS_PROXY_FEED_TIMEOUT":      "0s",
		"RSS_PROXY_MAX_FEED_BYTES":    "0",
	}
	for k, v := range map[string]string{
		"RSS_PROXY_FEED_TIMEOUT":   "-5s",
		"RSS_PROXY_MAX_FEED_BYTES": "-1",
	} {
		env[k] = v
	}
	// Two separate cases:
	for _, tc := range []struct {
		key, val, want string
	}{
		{"RSS_PROXY_FEED_TIMEOUT", "0s", "feed-timeout"},
		{"RSS_PROXY_MAX_FEED_BYTES", "0", "max-feed-bytes"},
	} {
		e := map[string]string{"RSS_PROXY_PUBLIC_BASE_URL": "http://localhost:8080"}
		e[tc.key] = tc.val
		_, err := load(t, e)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s=%s: expected %q error, got %v", tc.key, tc.val, tc.want, err)
		}
	}
}

func TestConfig_RejectsCredentialsInBaseURL(t *testing.T) {
	_, err := load(t, map[string]string{"RSS_PROXY_PUBLIC_BASE_URL": "http://user:pass@localhost:8080"})
	if err == nil || !strings.Contains(err.Error(), "credentials") {
		t.Fatalf("expected credentials rejection, got %v", err)
	}
}

func TestConfig_RejectsQueryOrFragmentInBaseURL(t *testing.T) {
	for _, u := range []string{"http://localhost:8080/?x=1", "http://localhost:8080/#frag"} {
		_, err := load(t, map[string]string{"RSS_PROXY_PUBLIC_BASE_URL": u})
		if err == nil {
			t.Fatalf("expected rejection for %q", u)
		}
	}
}

func TestConfig_AllowHostsEnv(t *testing.T) {
	c, err := load(t, map[string]string{
		"RSS_PROXY_PUBLIC_BASE_URL": "http://localhost:8080",
		"RSS_PROXY_ALLOW_HOSTS":     "megaphone.fm,cdn.example.invalid",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(c.UpstreamAllow) != 2 {
		t.Errorf("allow hosts = %v", c.UpstreamAllow)
	}
}

func TestConfig_FlagAllowHost(t *testing.T) {
	c, err := load(t, map[string]string{"RSS_PROXY_PUBLIC_BASE_URL": "http://localhost:8080"},
		"-allow-host", "a.invalid", "-allow-host", "b.invalid")
	if err != nil {
		t.Fatal(err)
	}
	if len(c.UpstreamAllow) != 2 {
		t.Errorf("allow hosts = %v", c.UpstreamAllow)
	}
}

func TestConfig_PathPrefixNormalized(t *testing.T) {
	c, err := load(t, map[string]string{"RSS_PROXY_PUBLIC_BASE_URL": "http://localhost:8080/proxy"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(c.PublicBaseURL, "http://localhost:8080/proxy") {
		t.Errorf("base = %q", c.PublicBaseURL)
	}
}

func TestConfig_InvalidEnvValuesFail(t *testing.T) {
	cases := []struct {
		key, val, want string
	}{
		{"RSS_PROXY_FEED_TIMEOUT", "notaduration", "invalid duration"},
		{"RSS_PROXY_MAX_FEED_BYTES", "notanint", "invalid integer"},
		{"RSS_PROXY_ALLOW_PRIVATE_IPS", "notabool", "invalid boolean"},
		{"RSS_PROXY_ALLOW_HTTP_UPSTREAM", "maybe", "invalid boolean"},
	}
	for _, tc := range cases {
		env := map[string]string{"RSS_PROXY_PUBLIC_BASE_URL": "http://localhost:8080"}
		env[tc.key] = tc.val
		_, err := load(t, env)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s=%s: expected %q error, got %v", tc.key, tc.val, tc.want, err)
		}
	}
}

func TestConfig_ValidEnvValuesAccepted(t *testing.T) {
	c, err := load(t, map[string]string{
		"RSS_PROXY_PUBLIC_BASE_URL":   "http://localhost:8080",
		"RSS_PROXY_FEED_TIMEOUT":      "30s",
		"RSS_PROXY_MAX_FEED_BYTES":    "2097152",
		"RSS_PROXY_ALLOW_PRIVATE_IPS": "true",
	})
	if err != nil {
		t.Fatalf("valid env rejected: %v", err)
	}
	if c.FeedTimeout != 30*time.Second {
		t.Errorf("feed timeout = %v", c.FeedTimeout)
	}
	if c.MaxFeedBytes != 2097152 {
		t.Errorf("max feed bytes = %d", c.MaxFeedBytes)
	}
	if !c.AllowPrivateIPs {
		t.Errorf("allow private ips = false")
	}
}

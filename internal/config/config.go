// Package config loads and validates rss-proxy configuration.
package config

import (
	"errors"
	"flag"
	"fmt"
	"net/url"
	"strconv"
	"time"
)

// Config is the validated runtime configuration.
type Config struct {
	ListenAddr        string
	PublicBaseURL     string // absolute, scheme http, no creds/query/fragment
	UpstreamAllow     []string
	FeedTimeout       time.Duration
	MaxFeedBytes      int64
	UserAgent         string
	AllowPrivateIPs   bool
	AllowHTTPUpstream bool
}

// Load parses flags and environment variables and validates the result.
// Invalid configuration fails with a descriptive error.
func Load(args []string, getenv func(string) string) (Config, error) {
	fs := flag.NewFlagSet("rss-proxy", flag.ContinueOnError)
	var c Config

	// Resolve env defaults first so invalid env values fail at startup rather
	// than silently falling back. Flag values (if set) override these.
	feedTimeout, errDur := envDur(getenv, "RSS_PROXY_FEED_TIMEOUT", 15*time.Second)
	maxFeedBytes, errInt := envInt(getenv, "RSS_PROXY_MAX_FEED_BYTES", 4*1024*1024)
	allowPriv, errBoolPriv := envBool(getenv, "RSS_PROXY_ALLOW_PRIVATE_IPS", false)
	allowHTTP, errBoolHTTP := envBool(getenv, "RSS_PROXY_ALLOW_HTTP_UPSTREAM", false)

	fs.StringVar(&c.ListenAddr, "listen", env(getenv, "RSS_PROXY_LISTEN", ":8080"), "HTTP listen address")
	fs.StringVar(&c.PublicBaseURL, "public-base-url", env(getenv, "RSS_PROXY_PUBLIC_BASE_URL", ""), "public base URL (http scheme, absolute)")
	var allowFlag stringSliceFlag
	fs.Var(&allowFlag, "allow-host", "upstream hostname allowlist (repeatable); env RSS_PROXY_ALLOW_HOSTS comma-separated")
	fs.DurationVar(&c.FeedTimeout, "feed-timeout", feedTimeout, "feed fetch timeout")
	fs.Int64Var(&c.MaxFeedBytes, "max-feed-bytes", maxFeedBytes, "maximum feed size in bytes")
	fs.StringVar(&c.UserAgent, "user-agent", env(getenv, "RSS_PROXY_USER_AGENT", "rss-proxy/1.0 (podcast compatibility proxy)"), "upstream User-Agent")
	fs.BoolVar(&c.AllowPrivateIPs, "allow-private-ips", allowPriv, "allow private/loopback upstream addresses (local testing)")
	fs.BoolVar(&c.AllowHTTPUpstream, "allow-http-upstream", allowHTTP, "allow plain-http upstream schemes (local testing)")

	if err := fs.Parse(args); err != nil {
		return c, err
	}
	for _, e := range []error{errDur, errInt, errBoolPriv, errBoolHTTP} {
		if e != nil {
			return c, e
		}
	}
	c.UpstreamAllow = allowFlag

	// Env fallback for allow-hosts when flag not used.
	if len(c.UpstreamAllow) == 0 {
		if v := getenv("RSS_PROXY_ALLOW_HOSTS"); v != "" {
			c.UpstreamAllow = splitCSV(v)
		}
	}

	if err := c.Validate(); err != nil {
		return c, err
	}
	return c, nil
}

// Validate enforces the invariants described in REQUIREMENTS.md.
func (c *Config) Validate() error {
	if c.PublicBaseURL == "" {
		return errors.New("public-base-url is required")
	}
	u, err := url.Parse(c.PublicBaseURL)
	if err != nil {
		return fmt.Errorf("public-base-url parse: %w", err)
	}
	if !u.IsAbs() {
		return errors.New("public-base-url must be absolute")
	}
	if u.Scheme != "http" {
		return fmt.Errorf("public-base-url must use http scheme, got %q", u.Scheme)
	}
	if u.Host == "" {
		return errors.New("public-base-url must have a host")
	}
	if u.User != nil {
		return errors.New("public-base-url must not contain credentials")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return errors.New("public-base-url must not contain query or fragment")
	}
	if u.Path == "" {
		u.Path = "/"
	}
	c.PublicBaseURL = u.String()

	if c.FeedTimeout <= 0 {
		return fmt.Errorf("feed-timeout must be positive, got %v", c.FeedTimeout)
	}
	if c.MaxFeedBytes <= 0 {
		return fmt.Errorf("max-feed-bytes must be positive, got %d", c.MaxFeedBytes)
	}
	if c.UserAgent == "" {
		return errors.New("user-agent must not be empty")
	}
	return nil
}

// ---- env helpers ----

func env(g func(string) string, key, def string) string {
	if g == nil {
		return def
	}
	if v := g(key); v != "" {
		return v
	}
	return def
}
func envDur(g func(string) string, key string, def time.Duration) (time.Duration, error) {
	if g == nil {
		return def, nil
	}
	if v := g(key); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return def, fmt.Errorf("%s: invalid duration %q: %w", key, v, err)
		}
		return d, nil
	}
	return def, nil
}
func envInt(g func(string) string, key string, def int64) (int64, error) {
	if g == nil {
		return def, nil
	}
	if v := g(key); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return def, fmt.Errorf("%s: invalid integer %q: %w", key, v, err)
		}
		return n, nil
	}
	return def, nil
}
func envBool(g func(string) string, key string, def bool) (bool, error) {
	if g == nil {
		return def, nil
	}
	if v := g(key); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return def, fmt.Errorf("%s: invalid boolean %q: %w", key, v, err)
		}
		return b, nil
	}
	return def, nil
}

func splitCSV(s string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == ',' {
			p := s[start:i]
			if p != "" {
				out = append(out, p)
			}
			start = i + 1
		}
	}
	return out
}

// stringSliceFlag implements flag.Value for repeatable string flags.
type stringSliceFlag []string

func (s *stringSliceFlag) String() string { return "" }
func (s *stringSliceFlag) Set(v string) error {
	*s = append(*s, v)
	return nil
}

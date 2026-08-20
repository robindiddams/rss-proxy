// Package policy enforces upstream URL and network address policy.
package policy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// Policy decides whether an upstream URL and its resolved addresses are allowed.
type Policy struct {
	AllowPrivateIPs   bool
	AllowHTTPUpstream bool
	AllowHosts        []string // exact hosts + bounded subdomains; empty = any policy-compliant public host
}

// ErrRejected signals a policy rejection carrying a reason.
type ErrRejected struct {
	Reason string
	URL    string
}

func (e *ErrRejected) Error() string { return fmt.Sprintf("policy: %s: %s", e.Reason, e.URL) }

// CheckURL validates a parsed upstream URL (scheme, credentials, host, allowlist).
// It does not perform DNS resolution.
func (p *Policy) CheckURL(u *url.URL) error {
	if u == nil {
		return errors.New("nil url")
	}
	if u.User != nil {
		return &ErrRejected{Reason: "embedded credentials", URL: u.String()}
	}
	scheme := strings.ToLower(u.Scheme)
	switch scheme {
	case "https":
		// default allowed
	case "http":
		if !p.AllowHTTPUpstream {
			return &ErrRejected{Reason: "http upstream not allowed", URL: u.String()}
		}
	default:
		return &ErrRejected{Reason: "unsupported scheme " + u.Scheme, URL: u.String()}
	}
	host := u.Hostname()
	if host == "" {
		return &ErrRejected{Reason: "missing host", URL: u.String()}
	}
	if len(p.AllowHosts) > 0 && !hostAllowed(host, p.AllowHosts) {
		return &ErrRejected{Reason: "host not in allowlist", URL: u.String()}
	}
	return nil
}

// CheckURLString parses and checks a raw URL string.
func (p *Policy) CheckURLString(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("url parse: %w", err)
	}
	if !u.IsAbs() {
		return nil, &ErrRejected{Reason: "not absolute", URL: raw}
	}
	if err := p.CheckURL(u); err != nil {
		return nil, err
	}
	return u, nil
}

// hostAllowed reports whether host matches the allowlist. A list entry
// "example.com" matches "example.com" exactly and any "*.example.com"
// subdomain (one or more additional labels). Matching is case-insensitive.
func hostAllowed(host string, allow []string) bool {
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	for _, a := range allow {
		ent := strings.ToLower(strings.TrimSuffix(a, "."))
		if h == ent {
			return true
		}
		if strings.HasSuffix(h, "."+ent) {
			return true
		}
	}
	return false
}

// CheckAddress reports whether an IP is permitted by the address policy.
func (p *Policy) CheckAddress(ip net.IP) error {
	if p.AllowPrivateIPs {
		return nil
	}
	if !ip.IsGlobalUnicast() {
		return fmt.Errorf("address %s is not global unicast", ip)
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return fmt.Errorf("address %s is private/loopback/link-local/multicast/unspecified", ip)
	}
	// Carrier-grade NAT: 100.64.0.0/10
	if v4 := ip.To4(); v4 != nil {
		if v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127 {
			return fmt.Errorf("address %s is CGNAT range", ip)
		}
	}
	return nil
}

// CheckResolvedIPs rejects if any resolved IP is disallowed (mixed results rejected).
func (p *Policy) CheckResolvedIPs(ips []net.IP) error {
	if p.AllowPrivateIPs {
		return nil
	}
	for _, ip := range ips {
		if err := p.CheckAddress(ip); err != nil {
			return err
		}
	}
	return nil
}

// DialContext returns a dialer function that resolves the host and enforces the
// address policy at connection time (defeating DNS rebinding). The first
// policy-compliant address is dialed.
func (p *Policy) DialContext(dialer *net.Dialer) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		// Resolve ourselves so we can filter before connecting.
		resolver := net.DefaultResolver
		ips, err := resolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, fmt.Errorf("resolve %s: %w", host, err)
		}
		if len(ips) == 0 {
			return nil, fmt.Errorf("no addresses for %s", host)
		}
		var allowed []net.IP
		for _, ia := range ips {
			ip := ia.IP
			if p.AllowPrivateIPs {
				allowed = append(allowed, ip)
				continue
			}
			if err := p.CheckAddress(ip); err != nil {
				continue
			}
			allowed = append(allowed, ip)
		}
		if len(allowed) == 0 {
			return nil, fmt.Errorf("policy: no permitted address for %s (all %d resolved rejected)", host, len(ips))
		}
		// Dial the first permitted address.
		var lastErr error
		for _, ip := range allowed {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			default:
			}
			c, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if err == nil {
				return c, nil
			}
			lastErr = err
		}
		if lastErr == nil {
			lastErr = fmt.Errorf("dial %s failed", host)
		}
		return nil, lastErr
	}
}

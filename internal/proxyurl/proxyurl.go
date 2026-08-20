// Package proxyurl builds media-proxy URLs under the configured public base URL.
package proxyurl

import (
	"net/url"
	"strings"
)

// Generator builds /media/untls URLs.
type Generator struct {
	base *url.URL
}
// Base returns a copy of the parsed public base URL.
func (g *Generator) Base() *url.URL {
	c := *g.base
	return &c
}

// New parses the public base URL (already validated http absolute).
func New(baseURL string) (*Generator, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, err
	}
	if u.Path == "" {
		u.Path = "/"
	}
	// Trim trailing slash for clean join.
	u.Path = strings.TrimSuffix(u.Path, "/")
	return &Generator{base: u}, nil
}

// MediaURL returns the proxy media URL for an upstream URL. The upstream
// path is appended after /media/untls so the URL ends in the original file
// extension (e.g. .mp3), which legacy clients like iTunes 10.4 use to
// determine the media type. The full upstream URL is carried in the url query
// parameter, which the proxy uses to fetch the resource.
func (g *Generator) MediaURL(upstream string) string {
	u, err := url.Parse(upstream)
	if err != nil {
		// Fall back to the plain form if the upstream is somehow unparseable.
		q := url.Values{}
		q.Set("url", upstream)
		out := *g.base
		out.Path = strings.TrimSuffix(g.base.Path, "/") + "/media/untls"
		out.RawQuery = q.Encode()
		return out.String()
	}
	q := url.Values{}
	q.Set("url", upstream)
	out := *g.base
	out.Path = strings.TrimSuffix(g.base.Path, "/") + "/media/untls" + u.EscapedPath()
	out.RawQuery = q.Encode()
	return out.String()
}

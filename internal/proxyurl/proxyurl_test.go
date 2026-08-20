package proxyurl_test

import (
	"net/url"
	"testing"

	"github.com/robindiddams/rss-proxy/internal/proxyurl"
)

func TestProxyURL_MediaURL(t *testing.T) {
	g, err := proxyurl.New("http://localhost:8080")
	if err != nil {
		t.Fatal(err)
	}
	got := g.MediaURL("https://cdn.example.invalid/ep.mp3?t=1&u=2")
	u, err := url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	if u.Scheme != "http" {
		t.Errorf("scheme = %q", u.Scheme)
	}
	if u.Path != "/media/untls/ep.mp3" {
		t.Errorf("path = %q, want /media/untls/ep.mp3", u.Path)
	}
	if u.Query().Get("url") != "https://cdn.example.invalid/ep.mp3?t=1&u=2" {
		t.Errorf("query url = %q", u.Query().Get("url"))
	}
}

func TestProxyURL_PathPrefix(t *testing.T) {
	g, err := proxyurl.New("http://h.example.invalid/pfx")
	if err != nil {
		t.Fatal(err)
	}
	got := g.MediaURL("https://up.invalid/ep.mp3")
	u, _ := url.Parse(got)
	if u.Path != "/pfx/media/untls/ep.mp3" {
		t.Errorf("path = %q, want /pfx/media/untls/ep.mp3", u.Path)
	}
	if u.Query().Get("url") != "https://up.invalid/ep.mp3" {
		t.Errorf("query url = %q", u.Query().Get("url"))
	}
}

func TestProxyURL_Roundtrip(t *testing.T) {
	g, _ := proxyurl.New("http://h:1/x")
	orig := "https://up.invalid/path?a='s'&b=\"d\""
	got := g.MediaURL(orig)
	u, _ := url.Parse(got)
	if u.Query().Get("url") != orig {
		t.Errorf("roundtrip lost data: got %q want %q", u.Query().Get("url"), orig)
	}
}

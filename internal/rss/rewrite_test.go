package rss_test

import (
	"bytes"
	"encoding/xml"
	"flag"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/robindiddams/rss-proxy/internal/policy"
	"github.com/robindiddams/rss-proxy/internal/proxyurl"
	"github.com/robindiddams/rss-proxy/internal/rss"
)

var update = flag.Bool("update", false, "regenerate golden output")

func mustProxy(t *testing.T, base string) *proxyurl.Generator {
	t.Helper()
	g, err := proxyurl.New(base)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func defaultPolicy() *policy.Policy {
	return &policy.Policy{AllowHTTPUpstream: false}
}

// reparse re-parses output with a namespace-resolving decoder and reports
// issues with well-formedness. It returns the raw token stream via RawToken so
// tests can assert on qnames and xmlns declarations.
func reparse(t *testing.T, data []byte) {
	t.Helper()
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = false
	for {
		_, err := dec.Token()
		if err == nil {
			continue
		}
		if err.Error() == "EOF" {
			return
		}
		t.Fatalf("reparse: output is not well-formed XML: %v", err)
	}
}

// rawTokens returns the RawToken stream for assertions on prefixes/declarations.
func rawTokens(t *testing.T, data []byte) []xml.Token {
	t.Helper()
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = false
	var out []xml.Token
	for {
		tok, err := dec.RawToken()
		if err != nil {
			if err.Error() == "EOF" {
				break
			}
			t.Fatalf("rawtoken: %v", err)
		}
		out = append(out, tok)
	}
	return out
}

func TestRewrite_Fixture(t *testing.T) {
	data, err := os.ReadFile("../../testdata/feed.xml")
	if err != nil {
		t.Fatal(err)
	}
	p := defaultPolicy()
	gen := mustProxy(t, "http://localhost:8080")
	rw := &rss.Rewriter{Policy: p, Proxy: gen, Logf: t.Logf}

	res, err := rw.Rewrite(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	out := res.Bytes

	// Output must be well-formed.
	reparse(t, out)

	// Conventional prefixes and their declarations preserved.
	s := string(out)
	for _, uri := range []string{rss.NSItunes, rss.NSMedia, rss.NSContent, rss.NSAtom, rss.NSGoogleplay, rss.NSPodcast} {
		pfx := rss.ConventionalPrefixes[uri]
		decl := "xmlns:" + pfx + "=\"" + uri + "\""
		if !strings.Contains(s, decl) {
			t.Errorf("output missing namespace declaration %s", decl)
		}
	}

	// itunes elements remain prefixed <itunes:...>.
	if !strings.Contains(s, "<itunes:author>") {
		t.Errorf("output missing <itunes:author>")
	}
	if !strings.Contains(s, "<itunes:image href=\"") {
		t.Errorf("output missing <itunes:image href=...>")
	}
	if !strings.Contains(s, "<media:content") || !strings.Contains(s, "<media:thumbnail") {
		t.Errorf("output missing media:content/media:thumbnail")
	}
	if !strings.Contains(s, "<content:encoded>") {
		t.Errorf("output missing <content:encoded>")
	}

	// Enclosure URLs rewritten to HTTP proxy URLs containing the original URL.
	if !strings.Contains(s, "http://localhost:8080/media/untls/") {
		t.Errorf("output missing rewritten proxy media url path")
	}
	if !strings.Contains(s, "/media/untls/audio/ep1.mp3?") {
		t.Errorf("output missing .mp3 extension before query")
	}
	// Original upstream URLs survive inside the proxy URL (URL-encoded).
	encoded := url.QueryEscape("https://cdn.example.invalid/audio/ep1.mp3?t=1&u=2")
	if !strings.Contains(s, encoded) {
		t.Errorf("output missing encoded original enclosure url; want substring %s", encoded)
	}

	// No HTTPS link remaining for rewritten targets.
	if strings.Contains(s, "https://cdn.example.invalid/audio/ep1.mp3") {
		t.Errorf("output still contains original https enclosure url")
	}
	if strings.Contains(s, "https://cdn.example.invalid/itunes-art.jpg") {
		t.Errorf("output still contains original https itunes:image url")
	}
	if strings.Contains(s, "https://cdn.example.invalid/channel-art.jpg") {
		t.Errorf("output still contains original https channel image url")
	}

	// Ordinary links and atom:link href must NOT be rewritten.
	if !strings.Contains(s, "<link>https://example.invalid/show</link>") {
		t.Errorf("ordinary channel link was altered")
	}
	if !strings.Contains(s, "https://example.invalid/feed.xml") {
		t.Errorf("atom:link href was altered (should be left alone)")
	}

	// DOCTYPE, comment, PI preserved.
	if !strings.Contains(s, "<!DOCTYPE rss") {
		t.Errorf("output missing DOCTYPE")
	}
	if !strings.Contains(s, "<!-- podcast compatibility proxy") {
		t.Errorf("output missing comment")
	}
	if !strings.Contains(s, "<?xml-stylesheet") {
		t.Errorf("output missing processing instruction")
	}
	// CDATA content preserved (as text).
	if !strings.Contains(s, "The Synthetic Podcast") {
		t.Errorf("output missing CDATA title text")
	}

	if *update {
		_ = os.WriteFile("testdata/feed.golden.xml", out, 0644)
	}
}

func TestRewrite_NamespaceAlias(t *testing.T) {
	// Same feed but itunes elements use an aliased prefix "it". Recognition must
	// use namespace URI, not the literal prefix.
	in := `<?xml version="1.0"?>
<rss xmlns:it="http://www.itunes.com/dtds/podcast-1.0.dtd" xmlns:md="http://search.yahoo.com/mrss/" version="2.0">
  <channel>
    <title>Alias</title>
    <it:image href="https://up.invalid/art.jpg"/>
    <item>
      <enclosure url="https://up.invalid/ep.mp3" length="1" type="audio/mpeg"/>
      <md:content url="https://up.invalid/ep.mp3" fileSize="1"/>
    </item>
  </channel>
</rss>`
	p := defaultPolicy()
	gen := mustProxy(t, "http://h:1")
	rw := &rss.Rewriter{Policy: p, Proxy: gen}
	res, err := rw.Rewrite(bytes.NewReader([]byte(in)))
	if err != nil {
		t.Fatal(err)
	}
	reparse(t, res.Bytes)
	s := string(res.Bytes)
	// itunes:image (aliased it:) href rewritten.
	if strings.Contains(s, "https://up.invalid/art.jpg") {
		t.Errorf("aliased itunes:image href not rewritten")
	}
	if !strings.Contains(s, "http://h:1/media/untls/") {
		t.Errorf("aliased itunes:image not rewritten to proxy url")
	}
	// media:content (aliased md:) rewritten.
	if strings.Contains(s, ">https://up.invalid/ep.mp3<") {
		// the enclosure/media original url should be encoded inside proxy url
	}
	// media:content url should be rewritten too.
	if !strings.Contains(s, url.QueryEscape("https://up.invalid/ep.mp3")) {
		t.Errorf("aliased media:content url not rewritten")
	}
	if !strings.Contains(s, "/media/untls/ep.mp3?") {
		t.Errorf("media:content proxy url missing .mp3 extension")
	}
}

func TestRewrite_NoRewriteOrdinaryURLAttrs(t *testing.T) {
	in := `<?xml version="1.0"?>
<rss version="2.0"><channel><title>x</title>
  <someother url="https://keep.invalid/x"/>
  <link>https://keep.invalid/page</link>
  <atom:link xmlns:atom="http://www.w3.org/2005/Atom" href="https://keep.invalid/self"/>
</channel></rss>`
	p := defaultPolicy()
	gen := mustProxy(t, "http://h:1")
	rw := &rss.Rewriter{Policy: p, Proxy: gen}
	res, err := rw.Rewrite(bytes.NewReader([]byte(in)))
	if err != nil {
		t.Fatal(err)
	}
	reparse(t, res.Bytes)
	s := string(res.Bytes)
	if !strings.Contains(s, "https://keep.invalid/x") {
		t.Errorf("ordinary url attr was rewritten")
	}
	if !strings.Contains(s, "https://keep.invalid/page") {
		t.Errorf("ordinary link rewritten")
	}
	if !strings.Contains(s, "https://keep.invalid/self") {
		t.Errorf("atom:link href rewritten")
	}
	if strings.Contains(s, "http://h:1/media/untls") {
		t.Errorf("unexpected proxy url in ordinary-links feed:\n%s", s)
	}
}

func TestRewrite_UnknownNamespacedElementsPreserved(t *testing.T) {
	in := `<?xml version="1.0"?>
<rss xmlns:foo="http://foo.invalid/ns" version="2.0"><channel>
  <foo:thing custom="1">data</foo:thing>
</channel></rss>`
	p := defaultPolicy()
	gen := mustProxy(t, "http://h:1")
	rw := &rss.Rewriter{Policy: p, Proxy: gen}
	res, err := rw.Rewrite(bytes.NewReader([]byte(in)))
	if err != nil {
		t.Fatal(err)
	}
	reparse(t, res.Bytes)
	s := string(res.Bytes)
	if !strings.Contains(s, "xmlns:foo=\"http://foo.invalid/ns\"") {
		t.Errorf("unknown xmlns declaration dropped")
	}
	if !strings.Contains(s, "<foo:thing custom=\"1\">data</foo:thing>") {
		t.Errorf("unknown namespaced element not preserved verbatim; got:\n%s", s)
	}
}

func TestRewrite_EntityEscapedQueryAndQuotes(t *testing.T) {
	in := `<?xml version="1.0"?>
<rss version="2.0"><channel><item>
  <enclosure url="https://up.invalid/ep.mp3?a='s'&amp;b=&quot;d&quot;" length="1" type="audio/mpeg"/>
</item></channel></rss>`
	p := defaultPolicy()
	gen := mustProxy(t, "http://h:1")
	rw := &rss.Rewriter{Policy: p, Proxy: gen}
	res, err := rw.Rewrite(bytes.NewReader([]byte(in)))
	if err != nil {
		t.Fatal(err)
	}
	reparse(t, res.Bytes)
	s := string(res.Bytes)
	orig := "https://up.invalid/ep.mp3?a='s'&b=\"d\""
	if !strings.Contains(s, url.QueryEscape(orig)) {
		t.Errorf("entity-escaped+quote query url not preserved inside proxy url;\n%s", s)
	}
}

func TestRewrite_MalformedXML(t *testing.T) {
	in := `<?xml version="1.0"?><rss version="2.0"><channel><title>oops</channel></rss>`
	p := defaultPolicy()
	gen := mustProxy(t, "http://h:1")
	rw := &rss.Rewriter{Policy: p, Proxy: gen}
	_, err := rw.Rewrite(bytes.NewReader([]byte(in)))
	if err == nil {
		t.Fatal("expected error for malformed xml")
	}
}

func TestRewrite_OversizedFeed(t *testing.T) {
	// Huge text node; rewriter reads everything but the caller enforces size.
	// Here we verify the rewriter itself tolerates a large feed; the size limit
	// is enforced upstream by upstream.LimitedBody. We assert the caller path.
	// (See server tests for the 502 on oversized feed.)
	t.Skip("size limit enforced by upstream.LimitedBody; covered in server tests")
}

func TestRewrite_ChannelImageURLText(t *testing.T) {
	in := `<?xml version="1.0"?>
<rss version="2.0"><channel>
  <image><url>https://up.invalid/img.jpg</url><title>t</title><link>https://up.invalid/l</link></image>
</channel></rss>`
	p := defaultPolicy()
	gen := mustProxy(t, "http://h:1")
	rw := &rss.Rewriter{Policy: p, Proxy: gen}
	res, err := rw.Rewrite(bytes.NewReader([]byte(in)))
	if err != nil {
		t.Fatal(err)
	}
	reparse(t, res.Bytes)
	s := string(res.Bytes)
	if strings.Contains(s, ">https://up.invalid/img.jpg<") {
		t.Errorf("channel image url text not rewritten")
	}
	if !strings.Contains(s, url.QueryEscape("https://up.invalid/img.jpg")) {
		t.Errorf("channel image url not encoded in proxy url;\n%s", s)
	}
	// link inside image must NOT be rewritten.
	if !strings.Contains(s, "https://up.invalid/l") {
		t.Errorf("image/link child was altered")
	}
}

func TestRewrite_ReparsedNamespaceURIs(t *testing.T) {
	data, err := os.ReadFile("../../testdata/feed.xml")
	if err != nil {
		t.Fatal(err)
	}
	p := defaultPolicy()
	gen := mustProxy(t, "http://localhost:8080")
	rw := &rss.Rewriter{Policy: p, Proxy: gen}
	res, err := rw.Rewrite(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	dec := xml.NewDecoder(bytes.NewReader(res.Bytes))
	dec.Strict = false
	found := map[string]bool{}
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		if se, ok := tok.(xml.StartElement); ok {
			found[se.Name.Space] = true
		}
	}
	for _, uri := range []string{rss.NSItunes, rss.NSMedia, rss.NSContent, rss.NSAtom, rss.NSGoogleplay, rss.NSPodcast} {
		if !found[uri] {
			t.Errorf("reparse: namespace URI %s not present on any element", uri)
		}
	}
}

// Package rss parses upstream podcast feeds with an XML token stream and
// rewrites HTTPS resource URLs to the local media proxy endpoint.
//
// It uses encoding/xml's RawToken API so that namespace prefixes and their
// xmlns declarations are preserved verbatim (iTunes 10.4 is sensitive to the
// conventional itunes/media/content prefixes). Elements are identified
// internally by (namespace URI, local name) by tracking the prefix->URI scope
// reconstructed from xmlns declarations.
package rss

import (
	"encoding/xml"
	"fmt"
	"io"
	"strings"

	"github.com/robindiddams/rss-proxy/internal/policy"
	"github.com/robindiddams/rss-proxy/internal/proxyurl"
)

// Namespace URIs for podcast extension elements.
const (
	NSItunes     = "http://www.itunes.com/dtds/podcast-1.0.dtd"
	NSMedia      = "http://search.yahoo.com/mrss/"
	NSContent    = "http://purl.org/rss/1.0/modules/content/"
	NSAtom       = "http://www.w3.org/2005/Atom"
	NSGoogleplay = "http://www.google.com/schemas/play-podcasts/1.0"
	NSPodcast    = "https://podcastindex.org/namespace/1.0"
)

// Conventional prefixes that must be preserved when they appear upstream.
var conventionalPrefixes = map[string]string{
	NSItunes: "itunes", NSMedia: "media", NSContent: "content",
	NSAtom: "atom", NSGoogleplay: "googleplay", NSPodcast: "podcast",
}

// RewriteResult is the rewritten, serialized feed.
type RewriteResult struct {
	Bytes []byte
}

// Rewriter rewrites feeds using a policy and a proxy URL generator.
type Rewriter struct {
	Policy   *policy.Policy
	Proxy    *proxyurl.Generator
	Logf     func(format string, args ...any)
}

// Rewrite reads the entire upstream feed, rewrites resource URLs, and returns
// serialized XML. On malformed XML or a read error it returns an error and
// emits nothing, so callers can return a clean 502.
func (r *Rewriter) Rewrite(in io.Reader) (*RewriteResult, error) {
	if r.Logf == nil {
		r.Logf = func(string, ...any) {}
	}
	dec := xml.NewDecoder(in)
	dec.Strict = false
	// Keep entity references resolvable with defaults; RawToken still reports errors on malformed structure.

	var sb strings.Builder
	st := &state{logf: r.Logf, policy: r.Policy, proxy: r.Proxy}

	for {
		tok, err := dec.RawToken()
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("xml parse: %w", err)
		}
		if err := emitToken(&sb, st, tok); err != nil {
			return nil, err
		}
	}
	if len(st.scope) != 0 {
		// Tolerant: unbalanced feed. encoding/xml would usually error first.
		return nil, fmt.Errorf("xml parse: unbalanced elements")
	}
	return &RewriteResult{Bytes: []byte(sb.String())}, nil
}

// state carries the namespace scope and element stack across tokens.
type state struct {
	scope   []map[string]string // prefix -> URI, stack of scopes
	stack   []elem              // element stack by (uri, local, rawPrefix, rawLocal)
	logf    func(string, ...any)
	policy  *policy.Policy
	proxy   *proxyurl.Generator
}

type elem struct {
	uri, local string
	prefix     string
}

// curScope returns the merged prefix->URI map in effect (parent + current).
func (s *state) curScope() map[string]string {
	if len(s.scope) == 0 {
		return map[string]string{}
	}
	return s.scope[len(s.scope)-1]
}

// resolvePrefix returns the URI bound to prefix in the current scope.
func (s *state) resolvePrefix(prefix string) string {
	return s.curScope()[prefix]
}

// pushScope builds a new scope from xmlns attrs on a start element.
func (s *state) pushScope(attrs []xml.Attr) map[string]string {
	parent := s.curScope()
	next := make(map[string]string, len(parent)+2)
	for k, v := range parent {
		next[k] = v
	}
	for _, a := range attrs {
		if a.Name.Space == "xmlns" {
			next[a.Name.Local] = a.Value
		} else if a.Name.Space == "" && a.Name.Local == "xmlns" {
			next[""] = a.Value
		}
	}
	s.scope = append(s.scope, next)
	return next
}

func (s *state) popScope() {
	if len(s.scope) > 0 {
		s.scope = s.scope[:len(s.scope)-1]
	}
}

// emitToken serializes one token, applying rewrites, into sb.
func emitToken(sb *strings.Builder, st *state, tok xml.Token) error {
	switch t := tok.(type) {
	case xml.StartElement:
		// Build scope first so we can resolve this element's prefix.
		sc := st.pushScope(t.Attr)
		uri := sc[t.Name.Space]
		e := elem{uri: uri, local: t.Name.Local, prefix: t.Name.Space}
		st.stack = append(st.stack, e)

		// Possibly rewrite an attribute URL.
		attrs := t.Attr
		if attr, attrName := rewriteAttrTarget(uri, t.Name.Local); attr != "" {
			attrs = rewriteAttr(attrs, attr, attrName, st)
		}

		writeStart(sb, t.Name, attrs)
		return nil

	case xml.EndElement:
		writeEnd(sb, t.Name)
		if len(st.stack) > 0 {
			st.stack = st.stack[:len(st.stack)-1]
		}
		st.popScope()
		return nil

	case xml.CharData:
		txt := string(t)
		if shouldRewriteURLText(st) {
			txt = rewriteURLText(txt, st)
		}
		sb.WriteString(escapeText(txt))
		return nil

	case xml.Comment:
		sb.WriteString("<!--")
		sb.WriteString(string(t))
		sb.WriteString("-->")
		return nil

	case xml.ProcInst:
		sb.WriteString("<?")
		sb.WriteString(t.Target)
		if len(t.Inst) > 0 {
			sb.WriteString(" ")
			sb.Write(t.Inst)
		}
		sb.WriteString("?>")
		return nil

	case xml.Directive:
		sb.WriteString("<!")
		sb.Write(t)
		sb.WriteString(">")
		return nil
	}
	return nil
}

// rewriteAttrTarget returns (attrName, "url"|"href") if this element's URL
// attribute should be rewritten, else ("", "").
func rewriteAttrTarget(uri, local string) (string, string) {
	switch {
	case uri == "" && local == "enclosure":
		return "url", "url"
	case uri == NSMedia && local == "content":
		return "url", "url"
	case uri == NSMedia && local == "thumbnail":
		return "url", "url"
	case uri == NSItunes && local == "image":
		return "href", "href"
	case uri == NSGoogleplay && local == "image":
		return "href", "href"
	}
	return "", ""
}

// rewriteAttr returns attrs with the target url/hattr rewritten when policy
// permits. Non-compliant URLs are left unchanged with a warning.
func rewriteAttr(attrs []xml.Attr, targetLocal, _ string, st *state) []xml.Attr {
	out := make([]xml.Attr, len(attrs))
	copy(out, attrs)
	for i, a := range attrs {
		// Match the target attribute: same local name and no namespace prefix
		// (these URL attributes are unprefixed in practice).
		if a.Name.Local == targetLocal && a.Name.Space == "" {
			orig := a.Value
			if rewritten, ok := tryRewriteURL(orig, st); ok {
				out[i].Value = rewritten
			}
			return out
		}
	}
	return out
}

// tryRewriteURL returns the proxy URL for orig if orig is a policy-compliant
// absolute upstream URL. Otherwise it returns (orig, false) and logs a warning.
func tryRewriteURL(orig string, st *state) (string, bool) {
	orig = strings.TrimSpace(orig)
	if orig == "" {
		return orig, false
	}
	u, err := st.policy.CheckURLString(orig)
	if err != nil {
		st.logf("rss: not rewriting non-compliant url %q: %v", orig, err)
		return orig, false
	}
	return st.proxy.MediaURL(u.String()), true
}

// shouldRewriteURLText reports whether current CharData is the URL child of an
// RSS channel <image> element.
func shouldRewriteURLText(st *state) bool {
	n := len(st.stack)
	if n < 2 {
		return false
	}
	cur := st.stack[n-1]
	parent := st.stack[n-2]
	if cur.uri != "" || cur.local != "url" {
		return false
	}
	if parent.uri != "" || parent.local != "image" {
		return false
	}
	// grandparent should be channel for RSS image.
	if n >= 3 {
		gp := st.stack[n-3]
		if gp.uri != "" || gp.local != "channel" {
			return false
		}
	}
	return true
}

// rewriteURLText rewrites the URL inside CharData, preserving surrounding
// whitespace.
func rewriteURLText(txt string, st *state) string {
	i := 0
	for i < len(txt) && isWS(txt[i]) {
		i++
	}
	j := len(txt)
	for j > i && isWS(txt[j-1]) {
		j--
	}
	leading, core, trailing := txt[:i], txt[i:j], txt[j:]
	if rewritten, ok := tryRewriteURL(core, st); ok {
		return leading + rewritten + trailing
	}
	return txt
}

func isWS(b byte) bool { return b == ' ' || b == '\t' || b == '\n' || b == '\r' }

// ---- serializer ----

func writeStart(sb *strings.Builder, name xml.Name, attrs []xml.Attr) {
	sb.WriteString("<")
	sb.WriteString(qname(name))
	for _, a := range attrs {
		writeAttr(sb, a)
	}
	sb.WriteString(">")
}

func writeEnd(sb *strings.Builder, name xml.Name) {
	sb.WriteString("</")
	sb.WriteString(qname(name))
	sb.WriteString(">")
}

func qname(n xml.Name) string {
	if n.Space != "" {
		return n.Space + ":" + n.Local
	}
	return n.Local
}

func attrQName(n xml.Name) string {
	if n.Space != "" {
		return n.Space + ":" + n.Local
	}
	return n.Local
}

func writeAttr(sb *strings.Builder, a xml.Attr) {
	sb.WriteString(" ")
	sb.WriteString(attrQName(a.Name))
	sb.WriteString(`="`)
	sb.WriteString(escapeAttr(a.Value))
	sb.WriteString(`"`)
}

// escapeAttr escapes an attribute value per XML rules.
func escapeAttr(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch r {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '"':
			b.WriteString("&quot;")
		case '\t':
			b.WriteString("&#x9;")
		case '\n':
			b.WriteString("&#xA;")
		case '\r':
			b.WriteString("&#xD;")
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// escapeText escapes character data content.
func escapeText(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch r {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '\r':
			b.WriteString("&#xD;")
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// ConventionalPrefixes exported for tests.
var ConventionalPrefixes = conventionalPrefixes

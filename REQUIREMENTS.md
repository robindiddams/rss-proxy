# Podcast HTTPS-to-HTTP Compatibility Proxy

## Purpose

Build a standalone Go HTTP server that allows legacy podcast clients,
especially iTunes 10.4 on Mac OS X Snow Leopard, to consume modern HTTPS
podcast feeds and media.

The legacy client communicates with this server exclusively over plain HTTP.
The server fetches feeds and media from modern HTTPS upstream servers.

The initial target feed is:

`https://feeds.megaphone.fm/TBIEA9794787572`

The intended deployment is a trusted home network, not the public internet.
The server must still reject private-address SSRF by default and provide a
configurable upstream host policy, but it does not need public multi-tenant
proxy protections.

## Required Endpoints

### Feed proxy

`GET /rss/untls?url=<URL-encoded-upstream-feed-url>`

The endpoint must:

1. Accept an absolute upstream HTTPS feed URL.
2. Validate it against the configured upstream policy.
3. Fetch it over HTTPS, following only policy-compliant redirects.
4. Parse the response as XML/RSS.
5. Rewrite applicable HTTPS resource URLs to this server's media endpoint.
6. Serialize and return valid XML that preserves the feed's semantics.

`HEAD` should return the same status and headers as `GET`, without a body.
Unsupported methods must return `405 Method Not Allowed`.

### Media proxy

`GET /media/untls?url=<URL-encoded-upstream-resource-url>`

The endpoint must:

1. Decode and validate the original absolute upstream URL.
2. Fetch it over HTTPS, following only policy-compliant redirects.
3. Stream the upstream response without buffering the complete media file.
4. Preserve the upstream status and playback/download headers.

`HEAD` must be supported. Unsupported methods must return `405 Method Not
Allowed`.

## Configuration

Configuration must be supplied through flags and/or environment variables. No
deployment hostname, port, scheme, or externally visible URL may be hard-coded.

Required settings:

- HTTP listen address and port.
- Public base URL used for generated links.
- Optional upstream hostname allowlist.
- Feed-fetch timeout.
- Maximum feed size.
- Upstream User-Agent.
- Optional test/development switches for private IPs and HTTP upstreams.

The public base URL is required and **must use the `http` scheme**. Generating
HTTPS links would defeat compatibility with the target client. It must be an
absolute URL without credentials, query, or fragment.

Timeouts and size limits must be strictly positive. Invalid configuration must
fail at startup with a useful error rather than silently falling back to a
default.

HTTPS must be the default and production upstream scheme. Plain HTTP upstreams
and private-address upstreams may be enabled explicitly for local testing.

## XML and RSS Processing

The feed must be processed using an XML parser, not regular expressions over
the complete document.

Lossless byte-for-byte recreation is **not** required. The serializer may
change:

- Whitespace and indentation.
- Attribute order and quoting style.
- Empty-element syntax and entity spelling.

The output must remain well-formed XML and semantically preserve:

- Channel and episode metadata.
- Element and attribute namespace URIs.
- Unknown or unsupported extension elements and attributes.
- Comments, CDATA content, processing instructions, and DOCTYPE information
  where supported by the selected token pipeline.
- Element ordering and hierarchy.

Although XML namespace prefixes are theoretically aliases, this project targets
an old client and must not assume that iTunes 10.4 treats every equivalent
prefix representation identically. Established podcast namespace prefixes and
their bindings must therefore be preserved, including `itunes`, `media`,
`content`, `atom`, `googleplay`, and `podcast` when they occur in the upstream
feed. In particular, iTunes elements must remain in the conventional form
`<itunes:...>` with `itunes` bound to
`http://www.itunes.com/dtds/podcast-1.0.dtd`.

A token-stream implementation using Go's `encoding/xml` is acceptable only if
it explicitly preserves or correctly restores those conventional prefixes and
namespace declarations. Parsed namespace prefixes must not be passed to
`xml.Encoder` as if they were namespace URIs. Elements should still be
identified internally by namespace URI and local name. The emitted document
must be reparsed in tests to prove that it is valid, and its qualified element
names and namespace declarations must be checked for legacy compatibility.

A normalized feed model such as `gofeed` may be used for inspection, but it
must not be the sole representation if serializing it would discard unknown
metadata or extensions.

Malformed upstream XML must produce a controlled `502 Bad Gateway`; the server
must not return partially rewritten XML.

## URL Rewriting

At minimum, rewrite HTTPS URLs used by the legacy client in:

- RSS `<enclosure url="...">` elements.
- Media RSS `<media:content url="...">` elements.
- Media RSS `<media:thumbnail url="...">` elements.
- Podcast artwork elements such as `<itunes:image href="...">` and
  `<googleplay:image href="...">`.
- The URL child of an RSS channel `<image>` element.

Element recognition must use the element's namespace URI and local name rather
than depend on a particular prefix such as `itunes` or `media`.

Ordinary website links and unrelated attributes named `url` must not be
rewritten.

Each generated media URL must:

- Use the configured HTTP public base URL.
- Contain enough information to reconstruct the exact original upstream URL.
- Correctly escape query strings and XML attribute/text content.
- Respect any path prefix present in the configured public base URL.

Only policy-compliant upstream URLs may be rewritten. If an HTTPS media URL is
left unchanged because policy blocks it, log a warning with enough context to
diagnose the resulting compatibility failure.

## Upstream HTTP Behavior

All initial URLs and every redirect target must be checked against the same URL
and address policy. Redirect loops and excessive redirect chains must fail
cleanly.

The media proxy must forward request headers needed for playback, downloading,
and validation, including:

- `Range`
- `If-Range`
- `If-Modified-Since`
- `If-None-Match`

It must preserve relevant upstream response semantics, including:

- Status codes such as `200`, `206`, `304`, and `416`.
- `Content-Type`
- `Content-Length`
- `Content-Range`
- `Accept-Ranges`
- `Content-Disposition`
- `ETag`
- `Last-Modified`
- `Cache-Control`
- `Expires`

The feed endpoint must forward conditional request headers and preserve a valid
upstream `304 Not Modified` response rather than converting it to `502`.
Successful feed responses should preserve relevant cache validators and cache
headers after recalculating the rewritten body's `Content-Length`.

Media streaming must stop promptly when the downstream client disconnects.
The server must not require the full media object to be downloaded before
returning bytes.

## Upstream Security Policy

The server must not permit requests to loopback, unspecified, private,
link-local, multicast, or carrier-grade NAT addresses by default. This policy
must be enforced at connection time so DNS rebinding cannot bypass it.

Embedded URL credentials and unsupported schemes must be rejected. The same
rules must apply after redirects.

An optional hostname allowlist must support exact hosts and properly bounded
subdomains. An empty allowlist may permit any policy-compliant public host,
which is acceptable for this trusted home-network deployment and must be
documented clearly.

## Error Handling and Observability

Client input errors must use appropriate `4xx` responses. Upstream connection,
HTTP, size, timeout, and XML failures must return controlled `502` responses
without leaking sensitive internal details.

Logs should distinguish invalid client input, policy rejection, redirect
rejection, upstream failure, XML failure, and interrupted media streaming.
URLs containing credentials must never be logged unredacted.

A lightweight health endpoint should be provided.

## Project Structure

Keep separate responsibilities for:

- Configuration loading and validation.
- HTTP routing and server lifecycle.
- Upstream URL/address policy.
- Upstream HTTP client and redirect enforcement.
- RSS/XML parsing and URL rewriting.
- Proxy URL generation.
- Media streaming.

## Required Tests

Automated tests must cover:

- Configuration validation, especially rejection of an HTTPS public base URL
  and non-positive limits.
- Enclosure, Media RSS, podcast artwork, and RSS image rewriting.
- Input namespace aliases: recognition must use namespace URI and local name,
  not depend solely on a literal prefix.
- Preservation of conventional podcast prefixes and declarations in output,
  especially `itunes`, `media`, and `content`.
- Preservation of unknown namespaced elements and attributes.
- No rewriting of ordinary links or unrelated `url` attributes.
- XML containing comments, CDATA, processing instructions, entity-escaped URL
  query strings, single/double quotes, and a DOCTYPE where applicable.
- Re-parsing serialized output to verify well-formed XML, correct namespace
  URIs, and legacy-compatible qualified names and declarations.
- Malformed XML and oversized feeds.
- `GET`, `HEAD`, and rejected methods.
- Media streaming without whole-file buffering.
- Byte ranges, including `206` and `416` behavior.
- Conditional requests and `304` behavior for both feed and media endpoints.
- Allowed redirects, blocked-host redirects, private-address redirects,
  redirect loops, and redirect limits.
- Private, loopback, link-local, multicast, CGNAT, and mixed DNS results.
- Public base URLs containing a path prefix.
- Downstream cancellation during media streaming.

Include a checked-in, sanitized fixture representative of the target Megaphone
feed. Tests using that fixture must verify that the rewritten document parses,
retains important channel/episode metadata and extensions, contains HTTP proxy
URLs for enclosures, and preserves the original upstream URLs inside those
proxy URLs. Tests must not require live internet access.

## Acceptance Criteria

The implementation is complete when:

1. The target Megaphone feed can be subscribed to from iTunes 10.4 while the
   client communicates exclusively over HTTP.
2. Episode artwork and metadata remain usable.
3. Episode download and playback work, including seeking/range requests.
4. Upstream redirects and cache validation behave correctly.
5. The generated feed is valid XML with semantically correct namespaces and
   conventional podcast prefixes suitable for iTunes 10.4.
6. Default upstream policy prevents access to inappropriate local/private
   destinations.
7. The automated test suite and `go vet ./...` pass.

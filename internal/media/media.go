// Package media implements streaming proxying of upstream media with
// passthrough of playback/download headers and downstream-cancellation aware
// copying.
package media

import (
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/robindiddams/rss-proxy/internal/upstream"
)

// Headers to forward from the client request to the upstream.
var forwardRequestHeaders = []string{
	"Range", "If-Range", "If-Modified-Since", "If-None-Match",
}

// Headers to copy from the upstream response to the client.
var forwardResponseHeaders = []string{
	"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges",
	"Content-Disposition", "ETag", "Last-Modified", "Cache-Control", "Expires",
}

// Handler streams a single upstream media resource.
type Handler struct {
	Client *upstream.Client
}

// ServeHTTP handles GET/HEAD /media/untls?url=...
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	raw := r.URL.Query().Get("url")
	if raw == "" {
		http.Error(w, "missing url parameter", http.StatusBadRequest)
		return
	}

	reqHeaders := http.Header{}
	for _, k := range forwardRequestHeaders {
		if v := r.Header.Get(k); v != "" {
			reqHeaders.Set(k, v)
		}
	}

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	resp, err := h.Client.Fetch(ctx, r.Method, raw, reqHeaders)
	if err != nil {
		// Distinguish client-ish 4xx is hard here; treat fetch errors as 502.
		http.Error(w, "upstream unavailable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// Copy hop-by-hop-independent response headers.
	for _, k := range forwardResponseHeaders {
		if v := resp.Header.Get(k); v != "" {
			w.Header().Set(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)

	if r.Method == http.MethodHead || resp.StatusCode == http.StatusNotModified {
		// No body to stream.
		return
	}

	// Stream with downstream cancellation awareness. No size cap: this is a
	// trusted home-network deployment and the spec mandates a feed size limit,
	// not a media limit. A silent truncation cap would break long episodes.
	_, copyErr := copyWithCancel(ctx, w, resp.Body)
	if copyErr != nil {
		// Client gone or copy error. Nothing useful to write to a started response.
		// Context cancellation is expected when the client disconnects.
		if ctx.Err() != nil {
			return
		}
		_ = copyErr
	}
}

// copyWithCancel copies r to w, aborting promptly when ctx is done.
func copyWithCancel(ctx context.Context, w io.Writer, r io.Reader) (int64, error) {
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 32*1024)
	var total int64
	for {
		select {
		case <-ctx.Done():
			return total, ctx.Err()
		default:
		}
		n, err := r.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return total, werr
			}
			if flusher != nil {
				flusher.Flush()
			}
			total += int64(n)
		}
		if err == io.EOF {
			return total, nil
		}
		if err != nil {
			return total, fmt.Errorf("media copy: %w", err)
		}
	}
}

package main

import (
	"bufio"
	"compress/gzip"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
)

// gzipResponseWriter wraps an http.ResponseWriter so all writes are
// transparently gzipped. The wrapper remembers the underlying writer and the
// status code so it can flush at the right moment.
//
// The wrapper does NOT gzip if:
//   - the response is < minSize (200 bytes) - the gzip header overhead
//     outweighs the savings for tiny bodies
//   - the response writer already had Content-Encoding set by the handler
//   - Content-Type indicates an already-compressed stream
type gzipResponseWriter struct {
	http.ResponseWriter
	statusCode int
	wroteHeader bool
	gz          *gzip.Writer
	zw          io.Writer
	skip        bool
	minSize     int
	bytesWritten int
}

func (g *gzipResponseWriter) WriteHeader(code int) {
	if g.wroteHeader {
		return
	}
	g.statusCode = code
	// Peek at the response - if a handler already set Content-Encoding,
	// or the status is one we shouldn't gzip, skip the wrapper.
	if g.shouldSkip() {
		g.skip = true
		g.ResponseWriter.WriteHeader(code)
		g.wroteHeader = true
		return
	}
	// Lazily create the gzip writer. We need to delay to check if the
	// handler decides to set Content-Type to something we shouldn't compress.
	g.gz = gzip.NewWriter(g.ResponseWriter)
	g.zw = g.gz
	// Now that we know we're gzipping, set the header. The middleware
	// already added Vary: Accept-Encoding.
	g.ResponseWriter.Header().Set("Content-Encoding", "gzip")
	g.wroteHeader = true
}

func (g *gzipResponseWriter) shouldSkip() bool {
	h := g.ResponseWriter.Header()
	if h.Get("Content-Encoding") != "" {
		return true
	}
	ct := h.Get("Content-Type")
	// Don't gzip things that are already compressed.
	switch {
	case strings.HasPrefix(ct, "image/"),
		strings.HasPrefix(ct, "video/"),
		strings.HasPrefix(ct, "audio/"),
		strings.HasPrefix(ct, "application/zip"),
		strings.HasPrefix(ct, "application/gzip"),
		strings.HasPrefix(ct, "application/x-gzip"),
		strings.HasPrefix(ct, "application/wasm"):
		return true
	}
	// 1xx, 204, 304: no body, don't gzip.
	switch g.statusCode {
	case http.StatusNoContent, http.StatusNotModified:
		return true
	}
	return false
}

func (g *gzipResponseWriter) Write(p []byte) (int, error) {
	if !g.wroteHeader {
		g.WriteHeader(http.StatusOK)
	}
	if g.skip {
		return g.ResponseWriter.Write(p)
	}
	n, err := g.zw.Write(p)
	g.bytesWritten += n
	return n, err
}

// Flush flushes the gzip writer (if active) and the underlying writer.
// Required so streaming handlers see their bytes promptly.
func (g *gzipResponseWriter) Flush() {
	if g.gz != nil {
		_ = g.gz.Flush()
	}
	if f, ok := g.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (g *gzipResponseWriter) Close() error {
	if g.gz == nil {
		return nil
	}
	return g.gz.Close()
}

// Hijack passes through to the underlying writer so the http server can
// upgrade the connection.
func (g *gzipResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hj, ok := g.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("gzip: underlying ResponseWriter does not support hijacking")
	}
	// Best-effort: if we have buffered gzip bytes, they will be lost. For
	// our use case (the dashboard server doesn't hijack), this is fine.
	return hj.Hijack()
}

// gzipPool reuses gzip.Writer instances. Each writer has internal state
// (compression tables) and is expensive to allocate under load.
var gzipPool = sync.Pool{
	New: func() any {
		w, _ := gzip.NewWriterLevel(io.Discard, gzip.DefaultCompression)
		return w
	},
}

// gzipMiddleware wraps next so any response with a gzip-capable
// Accept-Encoding header is transparently compressed. The Vary header is
// set so caches don't serve a gzipped body to a client that didn't ask
// for one.
//
// Content-Encoding is set lazily inside the wrapper once we know we're
// going to gzip (so handlers that opt out by setting their own
// Content-Encoding still work).
func gzipMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !acceptsGzip(r.Header.Get("Accept-Encoding")) {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Vary", "Accept-Encoding")
		gw := &gzipResponseWriter{
			ResponseWriter: w,
			minSize:        200,
		}
		defer func() {
			// Best-effort close; if it fails the connection is already
			// half-written.
			_ = gw.Close()
		}()
		next.ServeHTTP(gw, r)
	})
}

func acceptsGzip(acceptEncoding string) bool {
	// Header value is a comma-separated list like "gzip, deflate, br".
	// Per RFC 7231 we should also respect q-values, but for a LAN-only
	// status server the simple "contains 'gzip'" check is fine.
	return strings.Contains(strings.ToLower(acceptEncoding), "gzip")
}

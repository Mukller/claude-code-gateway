package server

import (
	"compress/gzip"
	"net/http"
	"strings"
)

// withGzip compresses responses for clients that send Accept-Encoding: gzip.
// Streaming responses (text/event-stream) bypass compression so that
// token-by-token delivery from upstream providers is not buffered.
func (s *Server) withGzip(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !acceptsGzip(r) {
			next.ServeHTTP(w, r)
			return
		}
		gw := &gzipResponseWriter{ResponseWriter: w}
		defer gw.close()
		next.ServeHTTP(gw, r)
	})
}

func acceptsGzip(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept-Encoding"), "gzip")
}

type gzipResponseWriter struct {
	http.ResponseWriter
	gz          *gzip.Writer
	wroteHeader bool
	gzipOn      bool
}

func (g *gzipResponseWriter) WriteHeader(code int) {
	if g.wroteHeader {
		return
	}
	g.wroteHeader = true
	ct := g.Header().Get("Content-Type")
	if strings.Contains(ct, "text/event-stream") {
		g.gzipOn = false
		g.ResponseWriter.WriteHeader(code)
		return
	}
	g.gzipOn = true
	g.Header().Del("Content-Length")
	g.Header().Set("Content-Encoding", "gzip")
	g.gz = gzip.NewWriter(g.ResponseWriter)
	g.ResponseWriter.WriteHeader(code)
}

func (g *gzipResponseWriter) Write(b []byte) (int, error) {
	if !g.wroteHeader {
		g.WriteHeader(http.StatusOK)
	}
	if g.gzipOn && g.gz != nil {
		return g.gz.Write(b)
	}
	return g.ResponseWriter.Write(b)
}

func (g *gzipResponseWriter) Flush() {
	if !g.wroteHeader {
		g.WriteHeader(http.StatusOK)
	}
	if g.gzipOn && g.gz != nil {
		_ = g.gz.Flush()
	}
	if f, ok := g.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (g *gzipResponseWriter) close() {
	if g.gz != nil {
		_ = g.gz.Close()
	}
}

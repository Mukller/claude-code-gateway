package server

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func runGzip(t *testing.T, acceptEncoding string, contentType string) *httptest.ResponseRecorder {
	t.Helper()
	h := (&Server{}).withGzip(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", contentType)
		_, _ = w.Write([]byte("payload-body-for-test"))
	}))
	req := httptest.NewRequest("GET", "/v1/messages", nil)
	if acceptEncoding != "" {
		req.Header.Set("Accept-Encoding", acceptEncoding)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestGzipCompressesWhenAccepted(t *testing.T) {
	rec := runGzip(t, "gzip", "application/json")
	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", got)
	}
	zr, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	defer zr.Close()
	body, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if string(body) != "payload-body-for-test" {
		t.Fatalf("body = %q", body)
	}
}

func TestGzipPassthroughWhenNotAccepted(t *testing.T) {
	rec := runGzip(t, "", "application/json")
	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("Content-Encoding = %q, want empty", got)
	}
	if rec.Body.String() != "payload-body-for-test" {
		t.Fatalf("body = %q", rec.Body.String())
	}
}

func TestGzipSkipsEventStream(t *testing.T) {
	rec := runGzip(t, "gzip", "text/event-stream")
	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("Content-Encoding = %q, want empty for SSE", got)
	}
	if !strings.Contains(rec.Body.String(), "payload-body-for-test") {
		t.Fatalf("SSE body = %q", rec.Body.String())
	}
}

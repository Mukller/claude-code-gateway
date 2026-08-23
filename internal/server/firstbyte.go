package server

import (
	"net/http"
	"time"
)

type firstByteWriter struct {
	http.ResponseWriter
	status int
	first  time.Time
}

func (f *firstByteWriter) WriteHeader(code int) {
	if f.status == 0 {
		f.status = code
	}
	f.ResponseWriter.WriteHeader(code)
}

func (f *firstByteWriter) Write(b []byte) (int, error) {
	if f.first.IsZero() {
		f.first = time.Now()
	}
	if f.status == 0 {
		f.status = http.StatusOK
	}
	return f.ResponseWriter.Write(b)
}

func (f *firstByteWriter) ttft(started time.Time) int64 {
	if f.first.IsZero() {
		return -1
	}
	return f.first.Sub(started).Milliseconds()
}

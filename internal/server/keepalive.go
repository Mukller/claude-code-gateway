package server

import (
	"fmt"
	"net/http"
	"time"
)

func (s *Server) startKeepalive(w http.ResponseWriter, flush func(), fbw *firstByteWriter, started time.Time) chan struct{} {
	stop := make(chan struct{})
	if s.cfg.Server.KeepAliveSeconds <= 0 {
		return stop
	}
	interval := time.Duration(s.cfg.Server.KeepAliveSeconds) * time.Second
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-time.After(30 * time.Minute):
				return
			case <-t.C:
				if !fbw.first.IsZero() {
					return
				}
				fmt.Fprint(w, ": keep-alive\n\n")
				flush()
			}
		}
	}()
	return stop
}

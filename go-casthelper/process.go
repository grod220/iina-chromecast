package main

import (
	"net/http"
	"syscall"
	"time"
)

func (a *app) parentAlive() bool {
	if a.parentPID <= 1 {
		return true
	}
	err := syscall.Kill(a.parentPID, 0)
	return err == nil || err == syscall.EPERM
}
func (a *app) controlHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A newly launched IINA must not start a cast in the previous process's
		// helper while its parent watchdog is about to stop it.
		if !a.parentAlive() {
			http.Error(w, "Chromecast helper is restarting with IINA; retry shortly", http.StatusServiceUnavailable)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Shutdown must release the localhost ports promptly even if the TV stops
// replying while IINA quits. Closing our socket never stops foreign playback.
func (a *app) stopForExit(timeout time.Duration) {
	a.mu.Lock()
	c := a.cast
	a.mu.Unlock()
	done := make(chan struct{})
	go func() { a.stop(); close(done) }()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		if c != nil {
			c.Close()
		}
	}
}

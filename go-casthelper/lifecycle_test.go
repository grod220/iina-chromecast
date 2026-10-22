package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStaleWindowCleanupDoesNotStopReplacementCast(t *testing.T) {
	c, f := fakeConnection(t, "http://host/media/new")
	a := newApp("http://host", t.TempDir())
	a.cast = c
	a.phase = "connected"
	a.requested = true
	a.req = castRequest{OwnerID: "new-window", Path: "new.mp4"}
	json.Unmarshal([]byte(`{"castId":"new-cast"}`), &a.req)
	ctrl, _ := a.handlers()
	request := httptest.NewRequest("POST", "/stop", bytes.NewBufferString(`{"ownerId":"old-window","castId":"old-cast","abandon":true}`))
	response := httptest.NewRecorder()
	ctrl.ServeHTTP(response, request)
	if response.Code != 200 || a.status()["active"] != true {
		t.Fatal("stale cleanup stopped current cast", response.Code, a.status())
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.commands) > 0 {
		t.Fatal("stale cleanup sent TV commands", f.commands)
	}
}
func TestAbandonedCastCannotStartAfterDelayedHTTPRequest(t *testing.T) {
	a := newApp("http://host", t.TempDir())
	ctrl, _ := a.handlers()
	response := httptest.NewRecorder()
	ctrl.ServeHTTP(response, httptest.NewRequest("POST", "/stop", bytes.NewBufferString(`{"ownerId":"window","castId":"abandoned","abandon":true}`)))
	file := filepath.Join(t.TempDir(), "fixture.mp4")
	os.WriteFile(file, []byte("fixture"), 0600)
	body, _ := json.Marshal(map[string]any{"ownerId": "window", "castId": "abandoned", "path": file, "device": "192.0.2.1"})
	response = httptest.NewRecorder()
	ctrl.ServeHTTP(response, httptest.NewRequest("POST", "/cast", bytes.NewReader(body)))
	defer a.stop()
	if response.Code != 409 || a.status()["active"] != false {
		t.Fatal("late abandoned cast was accepted", response.Code, a.status())
	}
}
func TestStaleControlRequestCannotReachNewMedia(t *testing.T) {
	c, f := fakeConnection(t, "http://host/media/new")
	a := newApp("http://host", t.TempDir())
	a.cast = c
	a.requested = true
	a.phase = "connected"
	a.req = castRequest{OwnerID: "window"}
	json.Unmarshal([]byte(`{"castId":"new"}`), &a.req)
	ctrl, _ := a.handlers()
	r := httptest.NewRequest("POST", "/pause", bytes.NewBufferString(`{}`))
	r.Header.Set("X-IINA-Owner", "window")
	r.Header.Set("X-IINA-Cast", "old")
	out := httptest.NewRecorder()
	ctrl.ServeHTTP(out, r)
	if out.Code != 409 {
		t.Fatal("stale pause accepted", out.Code)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.commands) > 0 {
		t.Fatal("controlled new media", f.commands)
	}
}

func TestOrphanHelperRejectsNewControlRequests(t *testing.T) {
	a := newApp("http://host", t.TempDir())
	a.parentPID = 999999
	ctrl, _ := a.handlers()
	out := httptest.NewRecorder()
	a.controlHandler(ctrl).ServeHTTP(out, httptest.NewRequest("GET", "/status", nil))
	if out.Code != 503 {
		t.Fatal("orphan accepted control requests", out.Code)
	}
}

func TestQuitDoesNotWaitForAnUnresponsiveTV(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	go io.Copy(io.Discard, server)
	c := &castConn{conn: client, done: make(chan struct{}), waiters: map[int]chan map[string]any{}, transportID: "transport", expectedURL: "http://host/media/ours", ownsMedia: true, mediaSessionID: 7}
	a := newApp("http://host", t.TempDir())
	a.cast = c
	a.requested = true
	start := time.Now()
	a.stopForExit(30 * time.Millisecond)
	if time.Since(start) > 500*time.Millisecond || c.connected() {
		t.Fatal("quit waited for TV", time.Since(start))
	}
}

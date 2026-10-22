package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// These tests deliberately control a TV and run only with explicit opt-in.
func liveTV(t *testing.T) string {
	t.Helper()
	address := os.Getenv("IINA_CAST_LIVE_TV")
	if address == "" {
		t.Skip("set IINA_CAST_LIVE_TV to opt into live TV controls")
	}
	return address
}
func helperLiveStatus(t *testing.T) map[string]any {
	t.Helper()
	r, e := http.Get("http://127.0.0.1:9092/status")
	if e != nil {
		t.Fatal(e)
	}
	defer r.Body.Close()
	var s map[string]any
	if e = json.NewDecoder(r.Body).Decode(&s); e != nil {
		t.Fatal(e)
	}
	return s
}
func livePost(t *testing.T, path string) int {
	t.Helper()
	r, e := http.Post("http://127.0.0.1:9092/"+path, "application/json", bytes.NewBufferString("{}"))
	if e != nil {
		t.Fatal(e)
	}
	defer r.Body.Close()
	return r.StatusCode
}
func probeLiveMedia(t *testing.T, address string) map[string]any {
	t.Helper()
	c, e := dialCast(address)
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	r, e := c.request(nsReceiver, platformReceiverID, map[string]any{"type": "GET_STATUS"})
	if e != nil {
		t.Fatal(e)
	}
	tid := c.receiverTransport(r)
	if tid == "" {
		t.Fatal("no media receiver", r)
	}
	c.sessMu.Lock()
	c.transportID = tid
	c.sessMu.Unlock()
	if e = c.sendJSON(nsConnection, tid, map[string]any{"type": "CONNECT"}); e != nil {
		t.Fatal(e)
	}
	r, e = c.request(nsMedia, tid, map[string]any{"type": "GET_STATUS"})
	if e != nil {
		t.Fatal(e)
	}
	states, _ := r["status"].([]any)
	if len(states) == 0 {
		t.Fatal("no active media", r)
	}
	return states[0].(map[string]any)
}
func TestLiveReconnectKeepsExistingMediaSession(t *testing.T) {
	address := liveTV(t)
	before := probeLiveMedia(t, address)
	if helperLiveStatus(t)["phase"] != "connected" {
		t.Fatal("start an owned cast first")
	}
	if livePost(t, "reconnect") != 202 {
		t.Fatal("reconnect refused")
	}
	deadline := time.Now().Add(12 * time.Second)
	for helperLiveStatus(t)["phase"] != "connected" {
		if time.Now().After(deadline) {
			t.Fatal(helperLiveStatus(t))
		}
		time.Sleep(100 * time.Millisecond)
	}
	after := probeLiveMedia(t, address)
	if before["mediaSessionId"] != after["mediaSessionId"] || before["playerState"] != after["playerState"] || before["media"].(map[string]any)["contentId"] != after["media"].(map[string]any)["contentId"] {
		t.Fatal("reconnect replaced playback", before, after)
	}
	if before["playerState"] == "PAUSED" && before["currentTime"] != after["currentTime"] {
		t.Fatal("reconnect moved paused position")
	}
	t.Logf("retained media session %v, %v at %v", after["mediaSessionId"], after["playerState"], after["currentTime"])
}
func TestLiveTakeoverAndStopLeaveReplacementAlone(t *testing.T) {
	address := liveTV(t)
	clip := os.Getenv("IINA_CAST_LIVE_CLIP")
	if clip == "" {
		t.Skip("set IINA_CAST_LIVE_CLIP to a disposable paused test clip")
	}
	if !filepath.IsAbs(clip) {
		t.Fatal("absolute clip path required")
	}
	if helperLiveStatus(t)["phase"] != "connected" {
		t.Fatal("start an owned cast first")
	}
	helper := os.Getenv("IINA_CAST_LIVE_HELPER")
	if helper == "" {
		t.Skip("set IINA_CAST_LIVE_HELPER to the installed helper with LAN access")
	}
	freePort := func() string {
		l, e := net.Listen("tcp4", "127.0.0.1:0")
		if e != nil {
			t.Fatal(e)
		}
		_, p, _ := net.SplitHostPort(l.Addr().String())
		l.Close()
		return p
	}
	controlPort, mediaPort := freePort(), freePort()
	control := "http://127.0.0.1:" + controlPort
	cmd := exec.Command(helper, "-parent", fmt.Sprint(os.Getpid()), "-control", "127.0.0.1:"+controlPort, "-media", "0.0.0.0:"+mediaPort, "-log", filepath.Join(t.TempDir(), "replacement.log"))
	if e := cmd.Start(); e != nil {
		t.Fatal(e)
	}
	defer func() { cmd.Process.Signal(os.Interrupt); cmd.Wait() }()
	client := &http.Client{Timeout: 3 * time.Second}
	secondaryStatus := func() map[string]any {
		r, e := client.Get(control + "/status")
		if e != nil {
			return nil
		}
		defer r.Body.Close()
		var s map[string]any
		json.NewDecoder(r.Body).Decode(&s)
		return s
	}
	deadline := time.Now().Add(8 * time.Second)
	for secondaryStatus() == nil {
		if time.Now().After(deadline) {
			t.Fatal("replacement helper did not start")
		}
		time.Sleep(100 * time.Millisecond)
	}
	play := false
	body, _ := json.Marshal(castRequest{Device: address, DeviceName: "Connection test", DeviceModel: "Chromecast", Path: clip, Title: "Connection test", Autoplay: &play})
	r, e := client.Post(control+"/cast", "application/json", bytes.NewReader(body))
	if e != nil {
		t.Fatal(e)
	}
	r.Body.Close()
	if r.StatusCode != 202 {
		t.Fatal("replacement cast refused", r.StatusCode)
	}
	deadline = time.Now().Add(20 * time.Second)
	for {
		s := secondaryStatus()
		if s["phase"] == "connected" && s["playerState"] == "PAUSED" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("replacement did not pause", s)
		}
		time.Sleep(200 * time.Millisecond)
	}
	before := probeLiveMedia(t, address)
	for helperLiveStatus(t)["phase"] != "taken-over" {
		if time.Now().After(deadline) {
			t.Fatal("old sender missed takeover", helperLiveStatus(t))
		}
		time.Sleep(100 * time.Millisecond)
	}
	if livePost(t, "reconnect") != 409 {
		t.Fatal("reconnect could reclaim replacement")
	}
	if livePost(t, "stop") != 200 {
		t.Fatal("old sender stop failed")
	}
	after := probeLiveMedia(t, address)
	if before["mediaSessionId"] != after["mediaSessionId"] || after["playerState"] != "PAUSED" {
		t.Fatal("old sender interrupted replacement", before, after)
	}
	t.Logf("replacement session %v remains paused after old sender Stop", after["mediaSessionId"])
	r, e = client.Post(control+"/stop", "application/json", bytes.NewBufferString("{}"))
	if e != nil {
		t.Fatal(e)
	}
	r.Body.Close()
}

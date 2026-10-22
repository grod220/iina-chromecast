package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type fakeReceiver struct {
	mu         sync.Mutex
	position   float64
	playing    bool
	active     []int
	style      map[string]any
	url        string
	commands   []string
	volume     float64
	idleReason string
	loadError  string
}

func fakeConnection(t *testing.T, url string) (*castConn, *fakeReceiver) {
	t.Helper()
	client, server := net.Pipe()
	c := &castConn{conn: client, waiters: map[int]chan map[string]any{}, done: make(chan struct{}), transportID: "transport", receiverSession: "receiver-session", mediaSessionID: 7, volume: 0.5, expectedURL: url, ownsMedia: true, playbackRate: 1}
	f := &fakeReceiver{position: 20, playing: true, url: url, active: []int{10001}, volume: 0.5}
	go c.readLoop()
	go func() {
		defer server.Close()
		for {
			var hdr [4]byte
			if _, e := io.ReadFull(server, hdr[:]); e != nil {
				return
			}
			buf := make([]byte, binary.BigEndian.Uint32(hdr[:]))
			if _, e := io.ReadFull(server, buf); e != nil {
				return
			}
			m := decodeCastMessage(buf)
			var p map[string]any
			json.Unmarshal([]byte(m.payloadUTF8), &p)
			typ, _ := p["type"].(string)
			if typ == "CONNECT" || typ == "PING" {
				continue
			}
			f.mu.Lock()
			f.commands = append(f.commands, typ)
			switch typ {
			case "PAUSE":
				f.playing = false
			case "PLAY":
				f.playing = true
			case "SEEK":
				f.position = p["currentTime"].(float64)
			case "EDIT_TRACKS_INFO":
				if ids, ok := p["activeTrackIds"].([]any); ok {
					f.active = []int{}
					for _, v := range ids {
						f.active = append(f.active, int(v.(float64)))
					}
				}
				if s, ok := p["textTrackStyle"].(map[string]any); ok {
					f.style = s
				}
			case "STOP":
				f.playing = false
			case "SET_VOLUME":
				v := p["volume"].(map[string]any)
				f.volume = v["level"].(float64)
			}
			state := "PAUSED"
			if f.playing {
				state = "PLAYING"
			}
			if f.idleReason != "" {
				state = "IDLE"
			}
			reply := map[string]any{"type": "MEDIA_STATUS", "requestId": p["requestId"], "status": []any{map[string]any{"mediaSessionId": 7, "playerState": state, "currentTime": f.position, "playbackRate": 1, "activeTrackIds": f.active, "textTrackStyle": f.style, "media": map[string]any{"contentId": f.url, "duration": 120}}}}
			if f.idleReason != "" {
				reply["status"].([]any)[0].(map[string]any)["idleReason"] = f.idleReason
			}
			if typ == "LOAD" && f.loadError != "" {
				reply = map[string]any{"type": f.loadError, "requestId": p["requestId"]}
			}
			if m.namespace == nsReceiver {
				reply = map[string]any{"type": "RECEIVER_STATUS", "requestId": p["requestId"], "status": map[string]any{"volume": map[string]any{"level": f.volume, "muted": false}, "applications": []any{map[string]any{"appId": defaultReceiverAppID, "transportId": "transport", "sessionId": "receiver-session"}}}}
			}
			f.mu.Unlock()
			body, _ := json.Marshal(reply)
			source := "transport"
			if m.namespace == nsReceiver {
				source = platformReceiverID
			}
			out := encodeCastMessage(castMessage{sourceID: source, destID: senderID, namespace: m.namespace, payloadUTF8: string(body)})
			binary.BigEndian.PutUint32(hdr[:], uint32(len(out)))
			if _, e := server.Write(append(hdr[:], out...)); e != nil {
				return
			}
		}
	}()
	t.Cleanup(c.Close)
	return c, f
}
func TestRemoteClock(t *testing.T) {
	c := &castConn{currentTime: 20, duration: 30, playerState: "PLAYING", playbackRate: 1, positionUpdated: time.Now().Add(-2 * time.Second)}
	s := c.snapshot()
	if s.CurrentTime < 22 || s.CurrentTime > 22.2 {
		t.Fatal(s)
	}
	c.playerState = "PAUSED"
	if c.snapshot().CurrentTime != 20 {
		t.Fatal("paused clock advanced")
	}
}
func TestControlAndLiveSubtitleHTTP(t *testing.T) {
	c, f := fakeConnection(t, "http://host/media/test")
	a := newApp("http://host", t.TempDir())
	a.cast = c
	a.phase = "connected"
	a.requested = true
	a.req = castRequest{SelectedSubtitle: 1, SubtitlesEnabled: true}
	a.choices = []subtitleChoice{{ID: 1, Name: "English"}, {ID: 2, Name: "Spanish"}}
	ctrl, _ := a.handlers()
	srv := httptest.NewServer(ctrl)
	defer srv.Close()
	post := func(path string, body any) map[string]any {
		t.Helper()
		raw, _ := json.Marshal(body)
		res, e := http.Post(srv.URL+path, "application/json", bytes.NewReader(raw))
		if e != nil {
			t.Fatal(e)
		}
		defer res.Body.Close()
		if res.StatusCode != 200 {
			b, _ := io.ReadAll(res.Body)
			t.Fatalf("%s returned %d %s", path, res.StatusCode, b)
		}
		var out map[string]any
		json.NewDecoder(res.Body).Decode(&out)
		return out
	}
	if post("/pause", nil)["playerState"] != "PAUSED" {
		t.Fatal("pause not reflected")
	}
	if pos := post("/seek", map[string]any{"time": 37})["currentTime"].(float64); pos != 37 {
		t.Fatal("seek position missing", pos)
	}
	if post("/play", nil)["playerState"] != "PLAYING" {
		t.Fatal("play not reflected")
	}
	off := post("/subtitles", map[string]any{"enabled": false})
	if off["subtitlesEnabled"] != false {
		t.Fatal(off)
	}
	on := post("/subtitles", map[string]any{"enabled": true, "selectedSubtitle": 2, "style": map[string]any{"fontScale": 1.4, "backgroundColor": "#000000C0"}})
	if on["selectedSubtitle"] != float64(2) || on["subtitlesEnabled"] != true {
		t.Fatal(on)
	}
	post("/subtitles", map[string]any{"style": map[string]any{"fontScale": 1.1}})
	post("/volume", map[string]any{"level": 0.3, "muted": false})
	post("/stop", nil)
	if a.status()["phase"] != "idle" {
		t.Fatal("stop did not clear connection")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.active[0] != 10002 || f.style["fontScale"] != 1.1 || f.style["backgroundColor"] != "#000000C0" || f.style["edgeType"] != "NONE" || f.style["fontGenericFamily"] != "SANS_SERIF" {
		t.Fatal("receiver did not get live track controls")
	}
	for _, cmd := range f.commands {
		if cmd == "LOAD" {
			t.Fatal("switching subtitles must not reload playback")
		}
	}
}
func TestReconnectAttachesWithoutRestart(t *testing.T) {
	a := newApp("http://host", t.TempDir())
	a.req = castRequest{Path: "movie", Device: "192.168.1.2", SelectedSubtitle: 1}
	a.requested = true
	a.generation = 2
	a.phase = "reconnecting"
	a.mediaToken = "same"
	a.file = "prepared.mp4"
	a.last = remoteState{CurrentTime: 18, PlayerState: "PLAYING"}
	c, f := fakeConnection(t, "http://host/media/same")
	a.dial = func(string) (*castConn, error) { return c, nil }
	a.recover(2)
	if a.status()["phase"] != "connected" {
		t.Fatal(a.status())
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, cmd := range f.commands {
		if cmd == "LOAD" {
			t.Fatal("reconnect restarted a playing TV")
		}
	}
}

func TestMonitorRecoversClosedConnection(t *testing.T) {
	old, _ := fakeConnection(t, "http://host/media/same")
	next, _ := fakeConnection(t, "http://host/media/same")
	a := newApp("http://host", t.TempDir())
	a.cast = old
	a.phase = "connected"
	a.requested = true
	a.generation = 1
	a.req = castRequest{Path: "movie", Device: "192.168.1.2"}
	a.mediaToken = "same"
	a.file = "prepared.mp4"
	a.dial = func(string) (*castConn, error) { return next, nil }
	old.handle(1, castMessage{sourceID: "transport", destID: senderID, namespace: nsConnection, payloadUTF8: `{"type":"CLOSE"}`})
	if old.snapshot().Ended {
		t.Fatal("virtual channel closure was mistaken for finished media")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.monitor(ctx)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		a.mu.Lock()
		connected := a.cast == next && a.phase == "connected"
		a.mu.Unlock()
		if connected {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("closed connection was not recovered")
}

func TestFinishedClockAndStoppedMedia(t *testing.T) {
	c := &castConn{currentTime: 0, duration: 120, playerState: "IDLE", idleReason: "FINISHED"}
	if c.snapshot().CurrentTime != 120 {
		t.Fatal("finished playback position reset")
	}
	a := newApp("http://host", t.TempDir())
	file := filepath.Join(t.TempDir(), "clip.mp4")
	os.WriteFile(file, []byte("0123456789"), 0600)
	a.file = file
	a.mediaToken = "token"
	a.contentType = "video/mp4"
	r := httptest.NewRequest("GET", "http://host/media/token", nil)
	r.Header.Set("Range", "bytes=2-4")
	w := httptest.NewRecorder()
	a.handleMedia(w, r)
	if w.Code != 206 || w.Body.String() != "234" {
		t.Fatal("range streaming failed", w.Code, w.Body.String())
	}
	a.stop()
	w = httptest.NewRecorder()
	a.handleMedia(w, httptest.NewRequest("GET", "http://host/media/token", nil))
	if w.Code != 404 {
		t.Fatal("stopped movie is still being served")
	}
}

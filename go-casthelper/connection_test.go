package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

func TestFailureClosesConnectionButRetainsPreparedSessionForReconnect(t *testing.T) {
	c, f := fakeConnection(t, "http://host/media/ours")
	a := newApp("http://host", t.TempDir())
	a.cast, a.file, a.mediaToken = c, "prepared.mp4", "ours"
	a.session = c.identity()
	a.requested, a.generation = true, 1
	a.fail(1, errors.New("failure"))
	if c.connected() || a.cast != nil || a.file == "" || a.mediaToken == "" || a.session.Media != 7 {
		t.Fatal("failed connection leaked or lost prepared identity")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.commands) != 0 {
		t.Fatal("failure sent media controls", f.commands)
	}
}

func TestNewMovieCannotReusePreviousPreparedSession(t *testing.T) {
	c, _ := fakeConnection(t, "http://host/media/old")
	a := newApp("http://host", t.TempDir())
	a.req.Path = "old.mkv"
	a.cast, a.file, a.mediaToken = c, "old.mp4", "old"
	a.session = c.identity()
	a.begin(castRequest{Path: "/missing/movie.mkv"})
	a.mu.Lock()
	if a.cast != nil || a.file != "" || a.mediaToken != "" || a.session != (sessionIdentity{}) || c.connected() {
		t.Error("new movie retained old session")
	}
	a.cancel()
	a.mu.Unlock()
}

func TestCancellingRecoveryClosesPendingConnection(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	c := &castConn{conn: client, done: make(chan struct{}), waiters: map[int]chan map[string]any{}}
	a := newApp("http://host", t.TempDir())
	a.ctx, a.cancel = context.WithCancel(context.Background())
	a.requested, a.generation = true, 1
	a.req = castRequest{Path: "movie", Device: "TV"}
	dialled := make(chan struct{})
	a.dial = func(string) (*castConn, error) { close(dialled); return c, nil }
	finished := make(chan struct{})
	go func() { a.recover(1); close(finished) }()
	<-dialled
	a.stop()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("cancelled recovery remained blocked in a socket write")
	}
	if c.connected() {
		t.Fatal("pending connection stayed open")
	}
}

func statusMessage(source string, session int, url, state string) castMessage {
	body, _ := json.Marshal(map[string]any{"type": "MEDIA_STATUS", "status": []any{map[string]any{"mediaSessionId": session, "playerState": state, "currentTime": 99, "media": map[string]any{"contentId": url}}}})
	return castMessage{sourceID: source, destID: senderID, namespace: nsMedia, payloadUTF8: string(body)}
}

func TestOverlappingConnectionsHaveDistinctSenderIdentities(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		c := &castConn{senderID: newSenderID()}
		if seen[c.sender()] || c.sender() == senderID {
			t.Fatal("connection reused a sender identity")
		}
		seen[c.sender()] = true
	}
}
func TestRouteUsesActualTVSocketAndKeepsManualOverride(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	client, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	server, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	c := &castConn{conn: client}
	a := newApp("http://manual:8010", t.TempDir())
	a.mediaPort = "8010"
	if got, _ := a.baseFor(c); got != "http://manual:8010" {
		t.Fatal(got)
	}
	a.autoAddress = true
	if got, err := a.baseFor(c); err != nil || got != "http://127.0.0.1:8010" {
		t.Fatal(got, err)
	}
}
func TestRequestDeadlineAlsoBoundsBlockedSocketWrite(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	c := &castConn{conn: client, done: make(chan struct{}), waiters: map[int]chan map[string]any{}}
	start := time.Now()
	_, err := c.requestTimeout(nsMedia, "transport", map[string]any{"type": "GET_STATUS"}, 40*time.Millisecond)
	if err == nil || time.Since(start) > 500*time.Millisecond {
		t.Fatal("write outlived request deadline", err, time.Since(start))
	}
	if c.connected() {
		t.Fatal("failed write did not close transport")
	}
	if len(c.waiters) != 0 {
		t.Fatal("request waiter leaked")
	}
}
func TestLivenessClosesSilentConnectionButKeepsHealthyPausedPeer(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	c := &castConn{conn: client, done: make(chan struct{})}
	c.lastInbound.Store(time.Now().UnixNano())
	if !c.liveness(time.Now()) {
		t.Fatal("healthy quiet peer closed")
	}
	c.lastInbound.Store(time.Now().Add(-receiveTimeout - time.Second).UnixNano())
	if c.liveness(time.Now()) {
		t.Fatal("silent peer stayed connected")
	}
}
func TestForeignTransportCannotChangeOwnedMedia(t *testing.T) {
	c, _ := fakeConnection(t, "http://host/ours")
	c.handle(1, statusMessage("unrelated", 8, "http://host/foreign", "PLAYING"))
	if c.snapshot().TakenOver || c.identity().Media != 7 {
		t.Fatal("unrelated transport changed ownership")
	}
}
func TestMessageForAnotherSenderCannotChangeOurSession(t *testing.T) {
	c, _ := fakeConnection(t, "http://host/ours")
	m := statusMessage("transport", 8, "http://host/foreign", "PLAYING")
	m.destID = "sender-other"
	c.handle(1, m)
	if c.snapshot().TakenOver || c.identity().Media != 7 {
		t.Fatal("message for another sender changed our session")
	}
}
func TestBackdropIsIdleAndPartialReceiverStatusDoesNotEndMedia(t *testing.T) {
	if hasPlaybackApplication([]any{map[string]any{"appId": backdropReceiverAppID}}) {
		t.Fatal("idle Backdrop was classified as another sender")
	}
	if !hasPlaybackApplication([]any{map[string]any{"appId": "YouTube"}}) {
		t.Fatal("another receiver application was classified as idle")
	}
	c, _ := fakeConnection(t, "http://host/ours")
	c.logReceiverStatus(1, map[string]any{"status": map[string]any{"volume": map[string]any{"level": .2}}})
	if c.snapshot().Ended || c.snapshot().TakenOver {
		t.Fatal("partial status changed media ownership")
	}
	c.logReceiverStatus(2, map[string]any{"status": map[string]any{"applications": []any{map[string]any{"appId": backdropReceiverAppID}}}})
	if !c.snapshot().Ended || c.snapshot().TakenOver {
		t.Fatal("idle Backdrop was treated as a takeover")
	}
}
func TestForeignMediaSessionCannotBecomeOurCommandTarget(t *testing.T) {
	c, f := fakeConnection(t, "http://host/ours")
	c.handle(1, statusMessage("transport", 8, "http://host/foreign", "PLAYING"))
	if !c.snapshot().TakenOver || c.identity().Media != 7 {
		t.Fatal("foreign playback was adopted")
	}
	if !errors.Is(c.pause(), errTakenOver) {
		t.Fatal("pause accepted after takeover")
	}
	if !errors.Is(c.stop(), errTakenOver) {
		t.Fatal("stop accepted after takeover")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, cmd := range f.commands {
		if cmd != "GET_STATUS" {
			t.Fatal("controlled replacement playback", f.commands)
		}
	}
}
func TestStopAndDeviceVolumeRecheckUnobservedReplacement(t *testing.T) {
	for _, control := range []string{"stop", "volume"} {
		t.Run(control, func(t *testing.T) {
			c, f := fakeConnection(t, "http://host/ours")
			f.mu.Lock()
			f.url = "http://host/replacement"
			f.mu.Unlock()
			var err error
			if control == "stop" {
				err = c.stop()
			} else {
				err = c.setVolume(.2, false)
			}
			if !errors.Is(err, errTakenOver) {
				t.Fatal("replacement not detected", err)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			for _, cmd := range f.commands {
				if cmd == "STOP" || cmd == "SET_VOLUME" {
					t.Fatal("modified replacement cast", f.commands)
				}
			}
		})
	}
}
func TestAutomaticRecoveryDoesNotReloadAReplacementCast(t *testing.T) {
	c, f := fakeConnection(t, "http://host/media/replacement")
	a := newApp("http://host", t.TempDir())
	a.req = castRequest{Path: "movie", Device: "TV"}
	a.requested = true
	a.generation = 1
	a.phase = "reconnecting"
	a.mediaToken = "ours"
	a.file = "prepared.mp4"
	a.dial = func(string) (*castConn, error) { return c, nil }
	a.recover(1)
	if a.status()["phase"] != "taken-over" || a.status()["active"] != false {
		t.Fatal(a.status())
	}
	a.stop()
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, cmd := range f.commands {
		if cmd == "LOAD" || cmd == "STOP" || cmd == "SET_VOLUME" {
			t.Fatal("reclaimed another cast", f.commands)
		}
	}
}
func TestTakeoverIsNotOverwrittenByCancelledPreparation(t *testing.T) {
	a := newApp("http://host", t.TempDir())
	a.generation = 1
	a.requested = true
	a.takeover(1)
	a.fail(1, errors.New("cancelled"))
	if a.status()["phase"] != "taken-over" {
		t.Fatal(a.status())
	}
}
func TestExplicitZeroRateAndSupportedCommands(t *testing.T) {
	c, _ := fakeConnection(t, "http://host/ours")
	c.sessMu.Lock()
	c.playerState = "PLAYING"
	c.currentTime = 20
	c.positionUpdated = time.Now().Add(-2 * time.Second)
	c.playbackRate = 0
	bits := uint64(2)
	c.supportedCommands = &bits
	c.sessMu.Unlock()
	if c.snapshot().CurrentTime != 20 {
		t.Fatal("explicit zero playback rate advanced clock")
	}
	if err := c.pause(); err == nil || !strings.Contains(err.Error(), "does not support") {
		t.Fatal("unsupported pause was sent", err)
	}
	if err := c.seek(12); err != nil {
		t.Fatal(err)
	}
}

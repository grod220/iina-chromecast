package main

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDeliveryFailureNeverTriggersVideoConversion(t *testing.T) {
	info := fixtureInfo("matroska", "hevc", "aac")
	plan, _ := planMedia(info, "SWTV", nil, false)
	req := castRequest{DeviceModel: "SWTV"}
	last := remoteState{CurrentTime: 42, PlayerState: "PAUSED"}
	first, err := recoveryRequest(req, info, plan, &mediaDelivery{}, last)
	if err != nil || first.recovery.Fallback.Video || first.recovery.Fallback.Audio {
		t.Fatal(first, err)
	}
	if first.StartTime != 42 || first.Autoplay == nil || *first.Autoplay {
		t.Fatal("retry changed clock/pause")
	}
	if _, err = recoveryRequest(first, info, plan, &mediaDelivery{}, last); err == nil || !strings.Contains(err.Error(), "download") {
		t.Fatal("unreachable media caused format fallback", err)
	}
}
func TestDeliveredMediaFallbackPreservesIndependentStreamsAndIsBounded(t *testing.T) {
	info := fixtureInfo("matroska", "hevc", "aac")
	req := castRequest{DeviceModel: "SWTV"}
	current, _ := planMedia(info, "SWTV", nil, false)
	delivery := &mediaDelivery{}
	delivery.Bytes.Store(100000)
	last := remoteState{CurrentTime: 17, PlayerState: "PLAYING"}
	var err error
	req, err = recoveryRequest(req, info, current, delivery, last)
	if err != nil {
		t.Fatal(err)
	} // one unchanged delivery retry
	for _, want := range []conversionFallback{{Audio: true}, {Video: true}, {Audio: true, Video: true}} {
		req, err = recoveryRequest(req, info, current, delivery, last)
		if err != nil {
			t.Fatal(err)
		}
		if req.recovery.Fallback != want {
			t.Fatal("unexpected fallback", req.recovery.Fallback, want)
		}
		current, err = planMediaWithFallback(info, "SWTV", nil, req.recovery.Fallback)
		if err != nil {
			t.Fatal(err)
		}
		if current.CopyAudio == want.Audio || current.CopyVideo == want.Video {
			t.Fatal("converted compatible stream", current, want)
		}
	}
	if _, err = recoveryRequest(req, info, current, delivery, last); err == nil {
		t.Fatal("fallback kept looping")
	}
}
func TestExistingAudioConversionIsNotRepeatedAsAFallback(t *testing.T) {
	info := fixtureInfo("matroska", "h264", "dts")
	plan, _ := planMedia(info, "SWTV", nil, false)
	req := castRequest{DeviceModel: "SWTV"}
	next, ok := nextCompatibility(req, info, plan)
	if !ok {
		t.Fatal("no remaining fallback")
	}
	p, _ := planMediaWithFallback(info, "SWTV", nil, next.Fallback)
	if p.CopyVideo || p.CopyAudio {
		t.Fatal("retry repeated the same audio-only plan", p)
	}
}
func TestMediaDeliveryTracksSuccessfulBytesAndHTTPFailures(t *testing.T) {
	a := newApp("http://host", t.TempDir())
	a.file = "missing"
	a.mediaToken = "test"
	a.delivery = &mediaDelivery{}
	out := httptest.NewRecorder()
	a.handleMedia(out, httptest.NewRequest("GET", "http://host/media/test", nil))
	if a.delivery.Bytes.Load() != 0 || a.delivery.Errors.Load() != 1 {
		t.Fatal("failed HTTP fetch looked like decoded media")
	}
}

func TestRejectedReconnectLoadUsesDeliveryRecoveryInsteadOfRepeatingLoads(t *testing.T) {
	c, f := fakeConnection(t, "http://host/media/same")
	f.mu.Lock()
	f.idleReason, f.loadError = "ERROR", "LOAD_FAILED"
	f.mu.Unlock()
	a := newApp("http://host", t.TempDir())
	a.requested, a.generation = true, 1
	a.req = castRequest{Path: "movie", Device: "TV", recovery: compatibilityRecovery{DeliveryRetries: 1}}
	a.file, a.mediaToken = "prepared.mp4", "same"
	a.last = remoteState{CurrentTime: 42, PlayerState: "PAUSED"}
	a.dial = func(string) (*castConn, error) { return c, nil }
	a.recover(1)
	s := a.status()
	if s["phase"] != "error" || !strings.Contains(s["error"].(string), "download") {
		t.Fatal(s)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	loads := 0
	for _, cmd := range f.commands {
		if cmd == "LOAD" {
			loads++
		}
	}
	if loads != 1 {
		t.Fatal("failed reconnect kept issuing LOAD", f.commands)
	}
}

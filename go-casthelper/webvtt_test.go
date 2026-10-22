package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWebVTTDelayKeepsLayoutAndMetadata(t *testing.T) {
	source := []byte("\ufeffWEBVTT\r\n\r\nNOTE timing example\r\n00:00.000 --> 00:01.000\r\n\r\nSTYLE\r\n::cue { color: white; }\r\n\r\nREGION\r\nid:top\r\nwidth:60%\r\n\r\ncue-one\r\n00:01.000 --> 00:04.000 line:20% position:50% size:60% align:center\r\n<i>Hello <00:02.000>world</i>\r\n")
	unchanged, err := shiftWebVTT(source, 0)
	if err != nil || !bytes.Equal(source, unchanged) {
		t.Fatal("zero offset changed authored bytes", err)
	}
	body, err := shiftWebVTT(source, 0.125)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"NOTE timing example\n00:00.000 --> 00:01.000", "STYLE\n::cue { color: white; }", "REGION\nid:top\nwidth:60%", "cue-one\n00:00:01.125 --> 00:00:04.125 line:20% position:50% size:60% align:center", "<i>Hello <00:00:02.125>world</i>"} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("lost subtitle layout/metadata %q:\n%s", want, body)
		}
	}
}

func TestNegativeSubtitleDelayDropsExpiredCuesAndClampsPartialCue(t *testing.T) {
	body, err := shiftWebVTT([]byte("WEBVTT\n\nold\n00:00.000 --> 00:01.000\nExpired\n\nvisible\n00:01.000 --> 00:03.000 line:20%\nVisible\n"), -2)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "Expired") || !strings.Contains(string(body), "visible\n00:00:00.000 --> 00:00:01.000 line:20%") {
		t.Fatalf("invalid shifted cue:\n%s", body)
	}
}

func TestSubtitlePreviewIsReadOnlyAndShowsLiteralSamples(t *testing.T) {
	dir := t.TempDir()
	media := filepath.Join(dir, "movie.mp4")
	os.WriteFile(media, []byte("fixture"), 0600)
	sub := filepath.Join(dir, "English.vtt")
	os.WriteFile(sub, []byte("WEBVTT\n\n00:01.000 --> 00:04.000 line:20%\n<i>[Birds chirping]</i>\n\n00:19.000 --> 00:23.000\nHello &amp; welcome\nSecond line\n"), 0600)
	c, fake := fakeConnection(t, "http://host/media/test")
	a := newApp("http://host", dir)
	a.cast = c
	a.phase = "connected"
	a.requested = true
	ctrl, _ := a.handlers()
	srv := httptest.NewServer(ctrl)
	defer srv.Close()
	body, _ := json.Marshal(map[string]any{"path": media, "time": 20, "subtitle": subtitleSpec{ID: 2, ExternalPath: sub, Codec: "webvtt", Language: "eng", SDH: true, Forced: true}})
	res, err := http.Post(srv.URL+"/subtitle-preview", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var preview struct {
		Track   subtitleChoice   `json:"track"`
		Samples []subtitleSample `json:"samples"`
	}
	if res.StatusCode != 200 {
		t.Fatal(res.Status)
	}
	if err = json.NewDecoder(res.Body).Decode(&preview); err != nil {
		t.Fatal(err)
	}
	if !preview.Track.SDH || !preview.Track.Forced || !preview.Track.External || !strings.Contains(preview.Track.Name, "SDH") || len(preview.Samples) != 2 || preview.Samples[0].Text != "Hello & welcome\nSecond line" || !preview.Samples[0].Current {
		t.Fatalf("bad preview: %+v", preview)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.commands) != 0 || a.phase != "connected" || !a.requested {
		t.Fatal("preview changed the TV playback/session", fake.commands)
	}
}

func TestCatalogIncludesSourceFlagsWithoutGuessingSDH(t *testing.T) {
	info := mediaInfo{}
	info.Streams = []mediaStream{{Index: 2, Type: "subtitle", Codec: "subrip"}, {Index: 3, Type: "subtitle", Codec: "subrip"}}
	info.Streams[0].Tags.Language = "eng"
	info.Streams[0].Disposition.HearingImpaired = 1
	info.Streams[1].Tags.Language = "eng"
	info.Streams[1].Disposition.Forced = 1
	choices, _ := subtitleCatalog("/movie.mkv", nil, info)
	if len(choices) != 2 || !choices[0].SDH || choices[0].Forced || choices[1].SDH || !choices[1].Forced || choices[0].Name == choices[1].Name {
		t.Fatalf("lost source flags: %+v", choices)
	}
}

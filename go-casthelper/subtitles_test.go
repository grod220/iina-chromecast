package main

import (
	"bytes"
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestPreparedWebVTTPreservesAuthoredLayout(t *testing.T) {
	source := []byte("WEBVTT\n\nSTYLE\n::cue { color: white; }\n\nREGION\nid:top\nwidth:60%\n\ncue1\n00:01.000 --> 00:04.000 line:20% position:50% size:60% align:center\n<i>Positioned text</i>\n")
	p := filepath.Join(t.TempDir(), "layout.vtt")
	os.WriteFile(p, source, 0600)
	prepared, err := newSubtitlePreparer(t.TempDir()).prepare(context.Background(), "", []subtitleSpec{{Enabled: true, ExternalPath: p, Codec: "webvtt"}})
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(prepared[0].file)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body, source) {
		t.Fatalf("authored WebVTT changed:\n%s", body)
	}
}

func TestSubtitleLoadContract(t *testing.T) {
	sub := &castSubtitle{URL: "http://192.168.1.2:8010/subtitles/test.vtt", Name: "English", Language: "en"}
	load := buildMediaLoad("http://192.168.1.2:8010/media", "video/mp4", "Movie", sub, 280.5, false)
	media := load["media"].(map[string]any)
	tracks := media["tracks"].([]map[string]any)
	if len(tracks) != 1 || tracks[0]["type"] != "TEXT" || tracks[0]["trackContentType"] != "text/vtt" || tracks[0]["language"] != "en" {
		t.Fatalf("invalid subtitle track: %#v", tracks)
	}
	if tracks[0]["trackContentId"] != sub.URL {
		t.Fatal("subtitle URL missing")
	}
	active := load["activeTrackIds"].([]int)
	if len(active) != 1 || active[0] != tracks[0]["trackId"] {
		t.Fatal("subtitle track is not active")
	}
	if load["currentTime"] != 280.5 || load["autoplay"] != false {
		t.Fatal("playback state was not preserved")
	}
	plain := buildMediaLoad("media", "video/mp4", "Movie", nil, 0, true)
	if _, ok := plain["activeTrackIds"]; ok {
		t.Fatal("captions should be absent when disabled")
	}
}
func TestSubtitleHTTPContract(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "subtitle.vtt")
	if err := os.WriteFile(file, []byte("WEBVTT\n\n00:00:01.000 --> 00:00:02.000\nCaption\n"), 0600); err != nil {
		t.Fatal(err)
	}
	a := &app{subtitleFile: file, subtitleToken: "token"}
	r := httptest.NewRequest("GET", "http://host/subtitles/token.vtt", nil)
	w := httptest.NewRecorder()
	a.handleSubtitles(w, r)
	if w.Code != 200 || w.Header().Get("Content-Type") != "text/vtt; charset=utf-8" || w.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("receiver cannot fetch subtitles: %d %#v", w.Code, w.Header())
	}
	w = httptest.NewRecorder()
	a.handleSubtitles(w, httptest.NewRequest("GET", "http://host/subtitles/other.vtt", nil))
	if w.Code != 404 {
		t.Fatal("unknown subtitle URL should not serve a file")
	}
}

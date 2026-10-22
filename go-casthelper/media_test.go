package main

import (
	"strings"
	"testing"
)

func fixtureInfo(container, video, audio string) mediaInfo {
	var m mediaInfo
	m.Format.Name = container
	m.Format.Duration = "60"
	m.Streams = []mediaStream{{Index: 0, Type: "video", Codec: video, Level: 41}, {Index: 1, Type: "audio", Codec: audio}}
	return m
}
func TestCompatibilityPlans(t *testing.T) {
	for _, c := range []struct{ container, video, audio, model, mode string }{{"mov,mp4", "h264", "aac", "Chromecast", "direct"}, {"matroska,webm", "hevc", "aac", "SWTV-20TW", "remux"}, {"matroska,webm", "h264", "dts", "SWTV-20TW", "convert-audio"}, {"avi", "mpeg4", "pcm_s16le", "Chromecast", "transcode"}, {"mov,mp4", "hevc", "aac", "Chromecast", "transcode"}, {"matroska,webm", "vp9", "opus", "SWTV-20TW", "direct"}} {
		p, e := planMedia(fixtureInfo(c.container, c.video, c.audio), c.model, nil, false)
		if e != nil || p.Mode != c.mode {
			t.Errorf("%+v -> %+v %v", c, p, e)
		}
	}
	p, _ := planMedia(fixtureInfo("matroska", "hevc", "aac"), "SWTV-20TW", nil, false)
	args := strings.Join(mediaArgs("input", "output", p), " ")
	if !strings.Contains(args, "-c:v copy") || !strings.Contains(args, "-c:a copy") || !strings.Contains(args, "-tag:v hvc1") {
		t.Fatal("remux must preserve codecs", args)
	}
}

func TestSelectedAudioRequiresRemux(t *testing.T) {
	info := fixtureInfo("mov,mp4", "h264", "aac")
	info.Streams = append(info.Streams, mediaStream{Index: 2, Type: "audio", Codec: "aac"})
	selected := 2
	p, e := planMedia(info, "SWTV-20TW", &selected, false)
	if e != nil || p.Mode != "remux" || p.AudioIndex != 2 {
		t.Fatal(p, e)
	}
}

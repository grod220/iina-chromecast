package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func subtitleFixture(t *testing.T) (string, []subtitleSpec) {
	t.Helper()
	source := filepath.Join(t.TempDir(), "movie.mkv")
	if err := os.WriteFile(source, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	return source, []subtitleSpec{{ID: 1, StreamIndex: 2, Codec: "subrip", Enabled: true}, {ID: 2, StreamIndex: 3, Codec: "subrip", Enabled: true}}
}
func publishTestSubtitles(tasks []subtitleTask) []error {
	result := make([]error, len(tasks))
	for i, task := range tasks {
		result[i] = os.WriteFile(task.target, []byte("WEBVTT\n\n00:01.000 --> 00:03.000 line:20%\nCaption\n"), 0600)
	}
	return result
}
func waitTest(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatal("preparation stalled")
	}
}
func TestSubtitleBatchSharesWorkAndKeepsRemainingConsumerAlive(t *testing.T) {
	source, specs := subtitleFixture(t)
	p := newSubtitlePreparer(t.TempDir())
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	p.extract = func(ctx context.Context, tasks []subtitleTask) []error {
		calls.Add(1)
		if len(tasks) != 2 {
			t.Errorf("wanted one two-track batch, got %d", len(tasks))
		}
		close(started)
		select {
		case <-release:
			return publishTestSubtitles(tasks)
		case <-ctx.Done():
			t.Error("cancelled work still needed by another consumer")
			return []error{ctx.Err(), ctx.Err()}
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := make(chan error, 1)
	go func() { _, err := p.prepare(ctx, source, specs); first <- err }()
	waitTest(t, started)
	second := make(chan error, 1)
	go func() { _, err := p.prepare(context.Background(), source, specs[1:]); second <- err }()
	// Wait until the second consumer has joined the same preparation batch.
	deadline := time.Now().Add(3 * time.Second)
	for {
		p.mu.Lock()
		refs := 0
		for _, job := range p.jobs {
			refs = job.batch.refs
			break
		}
		p.mu.Unlock()
		if refs == 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("second consumer did not join")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	close(release)
	if err := <-second; err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("duplicate scans: %d", calls.Load())
	}
	if _, err := p.prepare(context.Background(), source, specs); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal("cached tracks were extracted again")
	}
}
func TestSubtitlePreparationCancelsAbandonedWorkAndCanRetry(t *testing.T) {
	source, specs := subtitleFixture(t)
	p := newSubtitlePreparer(t.TempDir())
	started, cancelled := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	p.extract = func(ctx context.Context, tasks []subtitleTask) []error {
		if calls.Add(1) == 1 {
			close(started)
			<-ctx.Done()
			close(cancelled)
			return []error{ctx.Err()}
		}
		return publishTestSubtitles(tasks)
	}
	ctx, cancel := context.WithCancel(context.Background())
	first := make(chan error, 1)
	go func() { _, err := p.prepare(ctx, source, specs[:1]); first <- err }()
	waitTest(t, started)
	cancel()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	waitTest(t, cancelled)
	if _, err := p.prepare(context.Background(), source, specs[:1]); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatal("retry reused a cancelled job")
	}
}
func TestRealBatchConvertsTwoEmbeddedTracksInOnePass(t *testing.T) {
	ffmpeg := mediaTool("ffmpeg")
	if _, err := exec.LookPath(ffmpeg); err != nil {
		t.Skip("ffmpeg unavailable")
	}
	dir := t.TempDir()
	first, second := filepath.Join(dir, "one.srt"), filepath.Join(dir, "two.srt")
	os.WriteFile(first, []byte("1\n00:00:00,100 --> 00:00:00,800\nFirst track\n"), 0600)
	os.WriteFile(second, []byte("1\n00:00:00,200 --> 00:00:00,900\nSecond track\n"), 0600)
	source := filepath.Join(dir, "movie.mkv")
	cmd := exec.Command(ffmpeg, "-v", "error", "-y", "-f", "lavfi", "-i", "color=size=16x16:rate=1:duration=1", "-i", first, "-i", second, "-map", "0:v", "-map", "1:s", "-map", "2:s", "-c:v", "libx264", "-c:s", "srt", source)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v %s", err, out)
	}
	p := newSubtitlePreparer(t.TempDir())
	var calls int
	p.extract = func(ctx context.Context, tasks []subtitleTask) []error {
		calls++
		return extractSubtitleBatch(ctx, tasks)
	}
	result, err := p.prepare(context.Background(), source, []subtitleSpec{{StreamIndex: 1, Codec: "subrip", Delay: .5}, {StreamIndex: 2, Codec: "subrip"}})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("multiple extraction jobs")
	}
	for i, want := range []string{"First track", "Second track"} {
		body, _ := os.ReadFile(result[i].file)
		if !strings.Contains(string(body), want) {
			t.Fatalf("lost %s: %s", want, body)
		}
	}
	body, _ := os.ReadFile(result[0].file)
	if !strings.Contains(string(body), "00:00:00.600") {
		t.Fatalf("delay lost: %s", body)
	}
}

func TestRealBatchPreservesEmbeddedWebVTTSettings(t *testing.T) {
	ffmpeg := mediaTool("ffmpeg")
	if _, err := exec.LookPath(ffmpeg); err != nil {
		t.Skip("ffmpeg unavailable")
	}
	dir := t.TempDir()
	first, second := filepath.Join(dir, "one.vtt"), filepath.Join(dir, "two.vtt")
	body := []byte("WEBVTT\n\ncue-one\n00:00.100 --> 00:00.800 line:20% position:50% size:60% align:center\n<i>Positioned text</i>\n")
	os.WriteFile(first, body, 0600)
	os.WriteFile(second, body, 0600)
	source := filepath.Join(dir, "movie.webm")
	cmd := exec.Command(ffmpeg, "-v", "error", "-y", "-f", "lavfi", "-i", "color=size=16x16:rate=1:duration=1", "-i", first, "-i", second, "-map", "0:v", "-map", "1:s", "-map", "2:s", "-c:v", "libvpx", "-c:s", "copy", source)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v %s", err, out)
	}
	result, err := newSubtitlePreparer(t.TempDir()).prepare(context.Background(), source, []subtitleSpec{{StreamIndex: 1, Codec: "webvtt"}, {StreamIndex: 2, Codec: "webvtt"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range result {
		out, _ := os.ReadFile(item.file)
		for _, want := range []string{"cue-one", "line:20% position:50% size:60% align:center", "<i>Positioned text</i>"} {
			if !strings.Contains(string(out), want) {
				t.Fatalf("lost %q: %s", want, out)
			}
		}
	}
}

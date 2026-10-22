package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type mediaStream struct {
	Index  int    `json:"index"`
	Codec  string `json:"codec_name"`
	Type   string `json:"codec_type"`
	Level  int    `json:"level"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	Tags   struct {
		Language string `json:"language"`
		Title    string `json:"title"`
	} `json:"tags"`
	Disposition struct {
		AttachedPic     int `json:"attached_pic"`
		HearingImpaired int `json:"hearing_impaired"`
		Forced          int `json:"forced"`
	} `json:"disposition"`
}
type mediaInfo struct {
	Streams []mediaStream `json:"streams"`
	Format  struct {
		Name     string `json:"format_name"`
		Duration string `json:"duration"`
	} `json:"format"`
}
type mediaPlan struct {
	Mode        string  `json:"mode"`
	Description string  `json:"description"`
	ContentType string  `json:"contentType"`
	VideoIndex  int     `json:"videoIndex"`
	AudioIndex  int     `json:"audioIndex"`
	CopyVideo   bool    `json:"copyVideo"`
	CopyAudio   bool    `json:"copyAudio"`
	HEVC        bool    `json:"hevc"`
	Duration    float64 `json:"duration"`
}

func mediaTool(name string) string {
	for _, p := range []string{"/opt/homebrew/opt/ffmpeg@8/bin/" + name, "/usr/local/opt/ffmpeg@8/bin/" + name, "/opt/homebrew/bin/" + name, "/usr/local/bin/" + name} {
		if st, e := os.Stat(p); e == nil && st.Mode().IsRegular() {
			return p
		}
	}
	return name
}
func probeMedia(ctx context.Context, path string) (mediaInfo, error) {
	var result mediaInfo
	out, err := exec.CommandContext(ctx, mediaTool("ffprobe"), "-v", "error", "-show_streams", "-show_format", "-of", "json", path).Output()
	if err != nil {
		return result, fmt.Errorf("cannot inspect media: %w", err)
	}
	err = json.Unmarshal(out, &result)
	return result, err
}
func planMedia(info mediaInfo, model string, audioIndex *int, force bool) (mediaPlan, error) {
	return planMediaWithFallback(info, model, audioIndex, conversionFallback{Video: force, Audio: force})
}
func planMediaWithFallback(info mediaInfo, model string, audioIndex *int, fallback conversionFallback) (mediaPlan, error) {
	p := mediaPlan{Mode: "direct", Description: "Playing original file", ContentType: "video/mp4", VideoIndex: -1, AudioIndex: -1, CopyVideo: true, CopyAudio: true}
	p.Duration, _ = strconv.ParseFloat(info.Format.Duration, 64)
	var video, audio *mediaStream
	for i := range info.Streams {
		s := &info.Streams[i]
		if s.Type == "video" && s.Disposition.AttachedPic == 0 && video == nil {
			video = s
		}
		if s.Type == "audio" && (audio == nil || audioIndex != nil && s.Index == *audioIndex) {
			audio = s
		}
	}
	if video == nil {
		return p, fmt.Errorf("the file has no playable video track")
	}
	p.VideoIndex = video.Index
	if audio != nil {
		p.AudioIndex = audio.Index
	}
	modern := false
	model = strings.ToLower(model)
	for _, label := range []string{"swtv", "ultra", "google tv", "streamer", "android", "shield"} {
		if strings.Contains(model, label) {
			modern = true
		}
	}
	p.HEVC = video.Codec == "hevc"
	p.CopyVideo = !fallback.Video && (video.Codec == "h264" && (modern || video.Level <= 42) || video.Codec == "hevc" && modern || video.Codec == "vp8" || video.Codec == "vp9" && modern)
	p.CopyAudio = audio == nil || !fallback.Audio && (audio.Codec == "aac" || audio.Codec == "mp3")
	mp4 := strings.Contains(info.Format.Name, "mp4") || strings.Contains(info.Format.Name, "mov")
	webm := strings.Contains(info.Format.Name, "webm")
	if !fallback.Video && !fallback.Audio && webm && (video.Codec == "vp8" || video.Codec == "vp9" && modern) && (audio == nil || audio.Codec == "opus" || audio.Codec == "vorbis") {
		p.ContentType = "video/webm"
		p.CopyVideo = true
		p.CopyAudio = true
		return p, nil
	}
	if video.Codec == "vp8" || video.Codec == "vp9" {
		p.CopyVideo = false
	}
	if !p.CopyVideo {
		p.Mode = "transcode"
		p.Description = "Converting video for this TV"
	} else if !p.CopyAudio {
		p.Mode = "convert-audio"
		p.Description = "Converting audio; keeping original video"
	} else if !mp4 || audioIndex != nil && firstAudioIndex(info) != *audioIndex {
		p.Mode = "remux"
		p.Description = "Repackaging without re-encoding"
	}
	return p, nil
}
func mediaArgs(path, out string, p mediaPlan) []string {
	args := []string{"-nostdin", "-hide_banner", "-v", "error", "-y", "-i", path, "-map", fmt.Sprintf("0:%d", p.VideoIndex)}
	if p.AudioIndex >= 0 {
		args = append(args, "-map", fmt.Sprintf("0:%d", p.AudioIndex))
	} else {
		args = append(args, "-an")
	}
	args = append(args, "-sn", "-dn")
	if p.CopyVideo {
		args = append(args, "-c:v", "copy")
		if p.HEVC {
			args = append(args, "-tag:v", "hvc1")
		}
	} else {
		args = append(args, "-c:v", "libx264", "-preset", "veryfast", "-crf", "20", "-pix_fmt", "yuv420p", "-profile:v", "high", "-level:v", "4.1", "-vf", "scale=w='min(1920,iw)':h='min(1080,ih)':force_original_aspect_ratio=decrease:force_divisible_by=2")
	}
	if p.AudioIndex >= 0 {
		if p.CopyAudio {
			args = append(args, "-c:a", "copy")
		} else {
			args = append(args, "-c:a", "aac", "-b:a", "192k", "-ac", "2")
		}
	}
	return append(args, "-map_metadata", "0", "-movflags", "+faststart", "-progress", "pipe:1", "-f", "mp4", out)
}
func prepareMedia(ctx context.Context, path, cache string, p mediaPlan, progress func(float64)) (string, error) {
	if p.Mode == "direct" {
		return path, nil
	}
	st, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	key := sha256.Sum256([]byte(fmt.Sprintf("v2|%s|%d|%d|%+v", path, st.Size(), st.ModTime().UnixNano(), p)))
	if err = os.MkdirAll(cache, 0700); err != nil {
		return "", err
	}
	target := filepath.Join(cache, fmt.Sprintf("%x.mp4", key[:16]))
	if st, err := os.Stat(target); err == nil && st.Size() > 0 {
		progress(1)
		return target, nil
	}
	temp, err := os.CreateTemp(cache, "preparing-*.mp4")
	if err != nil {
		return "", err
	}
	temp.Close()
	defer os.Remove(temp.Name())
	cmd := exec.CommandContext(ctx, mediaTool("ffmpeg"), mediaArgs(path, temp.Name(), p)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	if err = cmd.Start(); err != nil {
		return "", err
	}
	scan := bufio.NewScanner(pipe)
	for scan.Scan() {
		line := scan.Text()
		if strings.HasPrefix(line, "out_time_us=") {
			n, _ := strconv.ParseFloat(strings.TrimPrefix(line, "out_time_us="), 64)
			if p.Duration > 0 {
				v := n / (p.Duration * 1e6)
				if v > 1 {
					v = 1
				}
				if v < 0 {
					v = 0
				}
				progress(v)
			}
		}
	}
	if err = cmd.Wait(); err != nil {
		return "", fmt.Errorf("media conversion failed: %w: %s", err, truncate(stderr.String(), 600))
	}
	if err = os.Rename(temp.Name(), target); err != nil {
		return "", err
	}
	progress(1)
	return target, nil
}
func cleanupMediaCache(dir, active string) {
	files, _ := os.ReadDir(dir)
	for _, f := range files {
		if f.IsDir() {
			continue
		}
		path := filepath.Join(dir, f.Name())
		st, e := f.Info()
		if e == nil && path != active && time.Since(st.ModTime()) > 7*24*time.Hour {
			_ = os.Remove(path)
		}
	}
}

func firstAudioIndex(info mediaInfo) int {
	for _, s := range info.Streams {
		if s.Type == "audio" {
			return s.Index
		}
	}
	return -1
}

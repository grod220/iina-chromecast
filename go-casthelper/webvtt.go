package main

import (
	"bytes"
	"fmt"
	"html"
	"math"
	"regexp"
	"strconv"
	"strings"
)

var vttTiming = regexp.MustCompile(`^((?:\d{2,}:)?\d{2}:\d{2}\.\d{3})[\t ]+-->[\t ]+((?:\d{2,}:)?\d{2}:\d{2}\.\d{3})(.*)$`)
var vttInlineTime = regexp.MustCompile(`<((?:\d{2,}:)?\d{2}:\d{2}\.\d{3})>`)
var vttMarkup = regexp.MustCompile(`<[^>]*>`)

func isWebVTT(body []byte) bool {
	text := string(bytes.TrimPrefix(body, []byte{0xef, 0xbb, 0xbf}))
	return text == "WEBVTT" || strings.HasPrefix(text, "WEBVTT\n") || strings.HasPrefix(text, "WEBVTT\r") || strings.HasPrefix(text, "WEBVTT ") || strings.HasPrefix(text, "WEBVTT\t")
}

func vttMillis(value string) int64 {
	parts := strings.Split(value, ":")
	var seconds float64
	for _, p := range parts {
		n, _ := strconv.ParseFloat(p, 64)
		seconds = seconds*60 + n
	}
	return int64(math.Round(seconds * 1000))
}

func vttTimestamp(ms int64) string {
	if ms < 0 {
		ms = 0
	}
	return fmt.Sprintf("%02d:%02d:%02d.%03d", ms/3600000, ms/60000%60, ms/1000%60, ms%1000)
}

func vttBlocks(body []byte) []string {
	return regexp.MustCompile(`\n[\t ]*\n+`).Split(strings.ReplaceAll(string(body), "\r\n", "\n"), -1)
}

func cueTiming(lines []string) (int, []string) {
	if len(lines) == 0 {
		return -1, nil
	}
	first := strings.TrimSpace(strings.TrimPrefix(lines[0], "\ufeff"))
	if first == "STYLE" || first == "REGION" || first == "NOTE" || strings.HasPrefix(first, "NOTE ") || strings.HasPrefix(first, "NOTE\t") || strings.HasPrefix(first, "WEBVTT") {
		return -1, nil
	}
	for i := 0; i < len(lines) && i < 2; i++ {
		if match := vttTiming.FindStringSubmatch(lines[i]); match != nil {
			return i, match
		}
	}
	return -1, nil
}

// Adjust only cue timestamps. Cue identifiers/settings, markup, STYLE, REGION
// and NOTE blocks stay intact. A zero offset is a byte-for-byte pass-through.
func shiftWebVTT(body []byte, delay float64) ([]byte, error) {
	if !isWebVTT(body) {
		return nil, fmt.Errorf("subtitle conversion did not produce WebVTT")
	}
	if math.IsNaN(delay) || math.IsInf(delay, 0) {
		return nil, fmt.Errorf("invalid subtitle delay")
	}
	offset := int64(math.Round(delay * 1000))
	if offset == 0 {
		return body, nil
	}
	blocks := vttBlocks(body)
	result := make([]string, 0, len(blocks))
	for _, block := range blocks {
		lines := strings.Split(block, "\n")
		i, match := cueTiming(lines)
		if i >= 0 {
			start, end := vttMillis(match[1])+offset, vttMillis(match[2])+offset
			if end <= 0 {
				continue
			}
			lines[i] = vttTimestamp(start) + " --> " + vttTimestamp(end) + match[3]
			for n := i + 1; n < len(lines); n++ {
				lines[n] = vttInlineTime.ReplaceAllStringFunc(lines[n], func(tag string) string { return "<" + vttTimestamp(vttMillis(tag[1:len(tag)-1])+offset) + ">" })
			}
		}
		result = append(result, strings.Join(lines, "\n"))
	}
	return []byte(strings.Join(result, "\n\n")), nil
}

type subtitleSample struct {
	Start   float64 `json:"start"`
	End     float64 `json:"end"`
	Text    string  `json:"text"`
	Current bool    `json:"current"`
}

func previewSamples(body []byte, position float64) []subtitleSample {
	cues := []subtitleSample{}
	for _, block := range vttBlocks(body) {
		lines := strings.Split(block, "\n")
		i, match := cueTiming(lines)
		if i < 0 {
			continue
		}
		text := html.UnescapeString(vttMarkup.ReplaceAllString(strings.Join(lines[i+1:], "\n"), ""))
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		runes := []rune(text)
		if len(runes) > 400 {
			text = string(runes[:400]) + "…"
		}
		start, end := float64(vttMillis(match[1]))/1000, float64(vttMillis(match[2]))/1000
		cues = append(cues, subtitleSample{start, end, text, start <= position && position < end})
	}
	if len(cues) == 0 {
		return []subtitleSample{}
	}
	best := len(cues) - 1
	for i, cue := range cues {
		if cue.Current || cue.Start >= position {
			best = i
			break
		}
	}
	samples := []subtitleSample{cues[best]}
	if best != 0 && cues[0].Text != cues[best].Text {
		samples = append(samples, cues[0])
	} else if best+1 < len(cues) {
		samples = append(samples, cues[best+1])
	}
	return samples
}

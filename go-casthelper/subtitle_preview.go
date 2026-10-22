package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
)

func subtitleDetails(s subtitleSpec) subtitleChoice {
	lang := subtitleLanguage(s.Language)
	base := map[string]string{"en": "English", "es": "Spanish", "ar": "Arabic", "fr": "French", "pt": "Portuguese", "ja": "Japanese", "zh": "Chinese", "de": "German", "it": "Italian", "ko": "Korean", "ru": "Russian", "nl": "Dutch", "und": "Unknown language"}[lang]
	if base == "" {
		base = lang
	}
	title := strings.TrimSpace(s.Name)
	name := base
	if title != "" && !strings.EqualFold(title, base) && !strings.EqualFold(title, s.Language) && title != "Subtitles" {
		name += " — " + title
	}
	if s.SDH && !strings.Contains(strings.ToUpper(title), "SDH") {
		name += " · SDH"
	}
	if s.Forced && !strings.Contains(strings.ToLower(title), "forced") {
		name += " · Forced"
	}
	if s.ExternalPath != "" {
		name += " · External"
	}
	name += fmt.Sprintf(" · Track %d", s.ID)
	return subtitleChoice{ID: s.ID, Name: name, Language: lang, Title: title, SDH: s.SDH, Forced: s.Forced, External: s.ExternalPath != "", Codec: s.Codec}
}

type subtitlePreviewRequest struct{ cancel context.CancelFunc }

func (a *app) handleSubtitlePreview(w http.ResponseWriter, r *http.Request) {
	var source string
	var spec subtitleSpec
	var position float64
	ctx := r.Context()
	if r.Method == "POST" {
		var body struct {
			ClientID string       `json:"clientId"`
			Path     string       `json:"path"`
			Subtitle subtitleSpec `json:"subtitle"`
			Time     float64      `json:"time"`
		}
		if err := decodeBody(r, &body); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if err := validateRequest(castRequest{Path: body.Path, Device: "preview", StartTime: body.Time}); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		source, spec, position = body.Path, body.Subtitle, body.Time
		if body.ClientID != "" {
			previewCtx, cancel := context.WithCancel(ctx)
			ctx = previewCtx
			request := &subtitlePreviewRequest{cancel}
			a.mu.Lock()
			if a.previews == nil {
				a.previews = map[string]*subtitlePreviewRequest{}
			}
			if old := a.previews[body.ClientID]; old != nil {
				old.cancel()
			}
			a.previews[body.ClientID] = request
			a.mu.Unlock()
			defer func() {
				cancel()
				a.mu.Lock()
				if a.previews[body.ClientID] == request {
					delete(a.previews, body.ClientID)
				}
				a.mu.Unlock()
			}()
		}
	} else if r.Method == "GET" {
		id, _ := strconv.Atoi(r.URL.Query().Get("id"))
		position, _ = strconv.ParseFloat(r.URL.Query().Get("time"), 64)
		a.mu.Lock()
		var asset *subtitleAsset
		for _, choice := range a.choices {
			if choice.ID == id {
				asset = a.assets[choice.Token]
				break
			}
		}
		a.mu.Unlock()
		if asset == nil {
			http.Error(w, "This subtitle track is no longer available", 404)
			return
		}
		source, spec = asset.source, asset.spec
	} else {
		http.Error(w, "GET or POST required", 405)
		return
	}
	if !textSubtitle(spec.Codec) {
		http.Error(w, "This image-based subtitle track cannot be previewed or cast as text", 422)
		return
	}
	spec.Enabled = true
	prepared, err := a.subtitles.prepare(ctx, source, []subtitleSpec{spec})
	if err != nil {
		http.Error(w, err.Error(), 422)
		return
	}
	body, err := os.ReadFile(prepared[0].file)
	if err != nil {
		http.Error(w, err.Error(), 422)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, map[string]any{"track": subtitleDetails(spec), "samples": previewSamples(body, position)})
}

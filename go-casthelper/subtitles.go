package main

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"strings"
)

type subtitleSpec struct {
	ID           int     `json:"id"`
	Codec        string  `json:"codec"`
	Enabled      bool    `json:"enabled"`
	StreamIndex  int     `json:"streamIndex"`
	ExternalPath string  `json:"externalPath"`
	Name         string  `json:"name"`
	Language     string  `json:"language"`
	Delay        float64 `json:"delay"`
	SDH          bool    `json:"sdh"`
	Forced       bool    `json:"forced"`
}
type castSubtitle struct{ URL, Name, Language string }

const subtitleTrackID = 10001

func buildMediaLoad(url, contentType, title string, subtitle *castSubtitle, startTime float64, autoplay bool) map[string]any {
	media := map[string]any{"contentId": url, "streamType": "BUFFERED", "contentType": contentType, "metadata": map[string]any{"metadataType": 0, "title": title}}
	load := map[string]any{"type": "LOAD", "autoplay": autoplay, "currentTime": startTime, "media": media}
	if subtitle != nil {
		lang := subtitle.Language
		if normalized := map[string]string{"eng": "en", "ara": "ar", "spa": "es", "fre": "fr", "fra": "fr", "por": "pt", "ger": "de", "deu": "de", "zho": "zh", "chi": "zh", "jpn": "ja", "kor": "ko"}[lang]; normalized != "" {
			lang = normalized
		}
		if lang == "" {
			lang = "und"
		}
		name := subtitle.Name
		if name == "" {
			name = "Subtitles"
		}
		media["tracks"] = []map[string]any{{"trackId": subtitleTrackID, "type": "TEXT", "subtype": "SUBTITLES", "trackContentId": subtitle.URL, "trackContentType": "text/vtt", "name": name, "language": lang}}
		load["activeTrackIds"] = []int{subtitleTrackID}
	}
	return load
}

type subtitleAsset struct {
	spec   subtitleSpec
	source string
}
type subtitleChoice struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	Language string `json:"language"`
	Token    string `json:"-"`
	Title    string `json:"title,omitempty"`
	SDH      bool   `json:"sdh"`
	Forced   bool   `json:"forced"`
	External bool   `json:"external"`
	Codec    string `json:"codec"`
}

func subtitleLanguage(lang string) string {
	if v := map[string]string{"eng": "en", "ara": "ar", "spa": "es", "fre": "fr", "fra": "fr", "por": "pt", "ger": "de", "deu": "de", "zho": "zh", "chi": "zh", "jpn": "ja", "kor": "ko"}[lang]; v != "" {
		return v
	}
	if lang == "" {
		return "und"
	}
	return lang
}
func textSubtitle(codec string) bool {
	switch codec {
	case "subrip", "srt", "ass", "ssa", "webvtt", "mov_text", "text", "":
		return true
	}
	return false
}
func subtitleCatalog(path string, specs []subtitleSpec, info mediaInfo) ([]subtitleChoice, map[string]*subtitleAsset) {
	if len(specs) == 0 {
		n := 0
		for _, s := range info.Streams {
			if s.Type == "subtitle" && textSubtitle(s.Codec) {
				n++
				specs = append(specs, subtitleSpec{ID: n, Enabled: true, StreamIndex: s.Index, Name: s.Tags.Title, Language: s.Tags.Language, Codec: s.Codec})
			}
		}
	}
	choices := []subtitleChoice{}
	assets := map[string]*subtitleAsset{}
	for n, s := range specs {
		for _, stream := range info.Streams {
			if s.ExternalPath == "" && stream.Type == "subtitle" && stream.Index == s.StreamIndex {
				if s.Codec == "" {
					s.Codec = stream.Codec
				}
				if s.Language == "" {
					s.Language = stream.Tags.Language
				}
				if s.Name == "" {
					s.Name = stream.Tags.Title
				}
				s.SDH = s.SDH || stream.Disposition.HearingImpaired != 0
				s.Forced = s.Forced || stream.Disposition.Forced != 0
			}
		}
		if !textSubtitle(s.Codec) {
			continue
		}
		if s.ID <= 0 {
			s.ID = n + 1
		}
		s.Enabled = true
		key := sha256.Sum256([]byte(fmt.Sprintf("%s|%d|%s|%g", path, s.StreamIndex, s.ExternalPath, s.Delay)))
		token := fmt.Sprintf("%x", key[:16])
		choice := subtitleDetails(s)
		choice.Token = token
		choices = append(choices, choice)
		assets[token] = &subtitleAsset{spec: s, source: path}
	}
	return choices, assets
}
func buildCatalogLoad(url, base, title string, plan mediaPlan, choices []subtitleChoice, selected int, enabled bool, style map[string]any, pos float64, autoplay bool) map[string]any {
	load := buildMediaLoad(url, plan.ContentType, title, nil, pos, autoplay)
	media := load["media"].(map[string]any)
	if plan.Duration > 0 {
		media["duration"] = plan.Duration
	}
	tracks := []map[string]any{}
	active := []int{}
	for _, s := range choices {
		tracks = append(tracks, map[string]any{"trackId": 10000 + s.ID, "type": "TEXT", "subtype": "SUBTITLES", "trackContentId": base + "/subtitles/" + s.Token + ".vtt", "trackContentType": "text/vtt", "name": s.Name, "language": s.Language})
		if enabled && selected == s.ID {
			active = append(active, 10000+s.ID)
		}
	}
	media["tracks"] = tracks
	media["textTrackStyle"] = style
	load["activeTrackIds"] = active
	return load
}
func (a *app) handleSubtitles(w http.ResponseWriter, r *http.Request) {
	cors(w, r)
	if r.Method == http.MethodOptions {
		return
	}
	token := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/subtitles/"), ".vtt")
	a.mu.Lock()
	asset := a.assets[token]
	legacyFile, legacyToken := a.subtitleFile, a.subtitleToken
	a.mu.Unlock()
	var file string
	if asset != nil {
		prepared, err := a.subtitles.prepare(r.Context(), asset.source, []subtitleSpec{asset.spec})
		if err == nil {
			file = prepared[0].file
		}
		if err != nil {
			http.Error(w, err.Error(), 422)
			return
		}
	} else if legacyFile != "" && token == legacyToken {
		file = legacyFile
	} else {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/vtt; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	a.subtitleHits.Add(1)
	http.ServeFile(w, r, file)
}

package main

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const helperVersion = "0.4.3"

//go:embed remote.html
var remoteHTML []byte

//go:embed remote.css
var remoteCSS []byte

//go:embed remote-ui.js
var remoteJS []byte

type castRequest struct {
	recovery         compatibilityRecovery
	Device           string         `json:"device"`
	DeviceName       string         `json:"deviceName"`
	DeviceModel      string         `json:"deviceModel"`
	DeviceHostname   string         `json:"deviceHostname"`
	OwnerID          string         `json:"ownerId"`
	CastID           string         `json:"castId"`
	Path             string         `json:"path"`
	Title            string         `json:"title"`
	StartTime        float64        `json:"startTime"`
	Autoplay         *bool          `json:"autoplay"`
	AudioIndex       *int           `json:"audioIndex"`
	Subtitles        []subtitleSpec `json:"subtitles"`
	Subtitle         *subtitleSpec  `json:"subtitle"`
	SelectedSubtitle int            `json:"selectedSubtitle"`
	SubtitlesEnabled bool           `json:"subtitlesEnabled"`
	TextTrackStyle   map[string]any `json:"textTrackStyle"`
	ForceTranscode   bool           `json:"forceTranscode"`
}
type app struct {
	parentPID                     int
	retired                       map[castScope]time.Time
	autoAddress                   bool
	mediaPort                     string
	session                       sessionIdentity
	info                          mediaInfo
	delivery                      *mediaDelivery
	previews                      map[string]*subtitlePreviewRequest
	subtitles                     *subtitlePreparer
	mu                            sync.Mutex
	req                           castRequest
	cast                          *castConn
	mediaURLBase, dataDir         string
	file, contentType, mediaToken string
	subtitleFile, subtitleToken   string
	assets                        map[string]*subtitleAsset
	choices                       []subtitleChoice
	plan                          mediaPlan
	phase, lastError              string
	progress                      float64
	requested                     bool
	generation                    int
	cancel                        context.CancelFunc
	ctx                           context.Context
	last                          remoteState
	recovering                    bool
	failures                      int
	mediaHits, subtitleHits       atomic.Uint64
	dial                          func(string) (*castConn, error)
}

func newApp(base, dir string) *app {
	return &app{subtitles: newSubtitlePreparer(filepath.Join(dir, "subtitle-cache")), mediaURLBase: base, dataDir: dir, phase: "idle", dial: dialCast, assets: map[string]*subtitleAsset{}, last: remoteState{PlayerState: "IDLE", Volume: 1}}
}
func cors(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "Range, Content-Type")
	w.Header().Set("Access-Control-Expose-Headers", "Content-Length, Content-Range, Accept-Ranges")
	w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, OPTIONS")
	if r.Method == http.MethodOptions {
		w.WriteHeader(204)
	}
}
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
func decodeBody(r *http.Request, v any) error {
	defer r.Body.Close()
	return json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(v)
}
func (a *app) handlers() (*http.ServeMux, *http.ServeMux) {
	c, m := http.NewServeMux(), http.NewServeMux()
	c.HandleFunc("/remote.css", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Write(remoteCSS)
	})
	c.HandleFunc("/remote-ui.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Write(remoteJS)
	})
	c.HandleFunc("/remote", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Write(remoteHTML)
	})
	c.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, a.status()) })
	c.HandleFunc("/subtitle-preview", a.handleSubtitlePreview)
	c.HandleFunc("/cast", func(w http.ResponseWriter, r *http.Request) {
		var req castRequest
		if r.Method != "POST" {
			http.Error(w, "POST required", 405)
			return
		}
		if e := decodeBody(r, &req); e != nil {
			http.Error(w, e.Error(), 400)
			return
		}
		if e := validateRequest(req); e != nil {
			http.Error(w, e.Error(), 400)
			return
		}
		if err := a.begin(req); err != nil {
			http.Error(w, err.Error(), 409)
			return
		}
		w.WriteHeader(202)
		writeJSON(w, map[string]any{"ok": true})
	})
	c.HandleFunc("/reconnect", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			http.Error(w, "POST required", 405)
			return
		}
		if err := a.reconnectFor(scopeFromRequest(r)); err != nil {
			http.Error(w, err.Error(), 409)
			return
		}
		w.WriteHeader(202)
		writeJSON(w, map[string]any{"ok": true})
	})
	c.HandleFunc("/stop", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			http.Error(w, "POST required", 405)
			return
		}
		var body struct {
			castScope
			Abandon bool `json:"abandon"`
		}
		if r.Body != nil && r.ContentLength != 0 {
			if err := decodeBody(r, &body); err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
		}
		if body.specified() && (body.OwnerID == "" || body.CastID == "") {
			http.Error(w, "ownerId and castId are required together", 400)
			return
		}
		a.stopFor(body.castScope, body.Abandon)
		writeJSON(w, a.status())
	})
	for _, name := range []string{"play", "pause", "seek", "volume", "subtitles"} {
		name := name
		c.HandleFunc("/"+name, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != "POST" {
				http.Error(w, "POST required", 405)
				return
			}
			if err := a.command(name, r); err != nil {
				http.Error(w, err.Error(), 409)
				return
			}
			writeJSON(w, a.status())
		})
	}
	m.HandleFunc("/media/", a.handleMedia)
	m.HandleFunc("/subtitles/", a.handleSubtitles)
	return c, m
}
func validateRequest(r castRequest) error {
	if r.Device == "" || r.Path == "" {
		return errors.New("device and file are required")
	}
	if !filepath.IsAbs(r.Path) {
		return errors.New("only local files with absolute paths are supported")
	}
	st, e := os.Stat(r.Path)
	if e != nil {
		return e
	}
	if !st.Mode().IsRegular() {
		return errors.New("media must be a regular file")
	}
	if math.IsNaN(r.StartTime) || math.IsInf(r.StartTime, 0) || r.StartTime < 0 {
		return errors.New("invalid playback position")
	}
	return nil
}
func (a *app) begin(req castRequest) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.retiredLocked(req) {
		return errCastReplaced
	}
	a.beginLocked(req)
	return nil
}
func (a *app) beginLocked(req castRequest) {
	if a.cancel != nil {
		a.cancel()
	}
	if a.req.Path != req.Path {
		// A failed preparation of a new movie must never reconnect to the
		// previous movie under the new request's identity.
		if a.cast != nil {
			a.cast.Close()
		}
		a.cast = nil
		a.file, a.mediaToken = "", ""
		a.assets = map[string]*subtitleAsset{}
		a.choices = nil
		a.session = sessionIdentity{}
		a.delivery = nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.cancel = cancel
	a.ctx = ctx
	a.generation++
	gen := a.generation
	a.req = req
	a.plan = mediaPlan{Description: "Inspecting video"}
	a.last.Duration = 0
	a.phase = "preparing"
	a.lastError = ""
	a.progress = 0
	a.requested = true
	a.recovering = false
	a.last.CurrentTime = req.StartTime
	if req.Autoplay != nil && !*req.Autoplay {
		a.last.PlayerState = "PAUSED"
	} else {
		a.last.PlayerState = "PLAYING"
	}
	go a.prepareAndStart(ctx, gen, req)
}
func (a *app) valid(gen int) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.generation == gen && a.requested
}
func (a *app) fail(gen int, err error) {
	a.mu.Lock()
	if gen != a.generation || !a.requested {
		a.mu.Unlock()
		return
	}
	c := a.cast
	a.cast = nil
	a.phase = "error"
	a.lastError = err.Error()
	a.requested = false
	a.recovering = false
	a.mu.Unlock()
	if c != nil {
		c.Close()
	}
	logError("Casting failed: %v", err)
}
func (a *app) prepareAndStart(ctx context.Context, gen int, req castRequest) {
	probeCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	info, err := probeMedia(probeCtx, req.Path)
	cancel()
	if err != nil {
		a.fail(gen, err)
		return
	}
	fallback := req.recovery.Fallback
	if req.ForceTranscode {
		fallback = conversionFallback{Video: true, Audio: true}
	}
	plan, err := planMediaWithFallback(info, req.DeviceModel, req.AudioIndex, fallback)
	if err != nil {
		a.fail(gen, err)
		return
	}
	a.mu.Lock()
	if gen == a.generation {
		a.plan = plan
		a.info = info
	}
	a.mu.Unlock()
	file, err := prepareMedia(ctx, req.Path, filepath.Join(a.dataDir, "media-cache"), plan, func(v float64) {
		a.mu.Lock()
		if gen == a.generation {
			a.progress = v
		}
		a.mu.Unlock()
	})
	if err != nil {
		a.fail(gen, err)
		return
	}
	specs := req.Subtitles
	if len(specs) == 0 && req.Subtitle != nil {
		s := *req.Subtitle
		if s.ID <= 0 {
			s.ID = 1
		}
		specs = []subtitleSpec{s}
		req.SelectedSubtitle = s.ID
		req.SubtitlesEnabled = s.Enabled
	}
	req.recovery.Tried |= planBit(plan)
	choices, assets := subtitleCatalog(req.Path, specs, info)
	if req.SelectedSubtitle <= 0 && len(choices) > 0 {
		req.SelectedSubtitle = choices[0].ID
	}
	style := subtitleStyle(req.TextTrackStyle)
	req.TextTrackStyle = style
	// The receiver prefetches every advertised track. Prepare uncached embedded
	// tracks together so it never starts one full-file FFmpeg scan per language.
	allSpecs := make([]subtitleSpec, 0, len(choices))
	for _, choice := range choices {
		allSpecs = append(allSpecs, assets[choice.Token].spec)
	}
	if _, e := a.subtitles.prepare(ctx, req.Path, allSpecs); e != nil {
		if ctx.Err() != nil {
			return
		}
		logWarn("Some subtitle tracks could not be prepared: %v", e)
		// An unavailable alternate track must not prevent casting a valid selection.
		if req.SubtitlesEnabled {
			for _, choice := range choices {
				if choice.ID == req.SelectedSubtitle {
					if _, e = a.subtitles.prepare(ctx, req.Path, []subtitleSpec{assets[choice.Token].spec}); e != nil {
						a.fail(gen, e)
						return
					}
				}
			}
		}
	}
	if !a.valid(gen) {
		return
	}
	key := sha256.Sum256([]byte(file))
	token := fmt.Sprintf("%x", key[:16])
	a.mu.Lock()
	if gen != a.generation || !a.requested {
		a.mu.Unlock()
		return
	}
	old := a.cast
	previousURL := a.mediaURLBase + "/media/" + a.mediaToken
	previousIdentity := a.session
	a.cast = nil
	a.req = req
	a.file = file
	a.mediaToken = token
	a.contentType = plan.ContentType
	a.choices = choices
	a.assets = assets
	a.phase = "connecting"
	a.delivery = &mediaDelivery{}
	a.progress = 1
	a.mu.Unlock()
	if old != nil {
		old.Close()
	}
	conn, err := a.dial(resolveDevice(req))
	if err != nil {
		a.fail(gen, err)
		return
	}
	// Stop or a newer request also cancels a pending receiver launch/LOAD,
	// before this connection has been published as the active cast.
	stopWatching := context.AfterFunc(ctx, conn.Close)
	defer stopWatching()
	if !a.valid(gen) {
		conn.Close()
		return
	}
	if req.recovery.Automatic {
		_, probeErr := conn.attachOwned(previousURL, previousIdentity)
		if probeErr != nil {
			conn.Close()
			if errors.Is(probeErr, errTakenOver) {
				a.takeover(gen)
			} else {
				a.fail(gen, probeErr)
			}
			return
		}
	}
	base, e := a.baseFor(conn)
	if e != nil {
		conn.Close()
		a.fail(gen, e)
		return
	}
	a.mu.Lock()
	if gen != a.generation || !a.requested {
		a.mu.Unlock()
		conn.Close()
		return
	}
	a.mediaURLBase = base
	a.mu.Unlock()
	load := buildCatalogLoad(base+"/media/"+token, base, req.Title, plan, choices, req.SelectedSubtitle, req.SubtitlesEnabled, style, req.StartTime, req.Autoplay == nil || *req.Autoplay)
	err = conn.launchAndLoad(load)
	a.mu.Lock()
	if gen == a.generation && a.requested {
		// A rejected LOAD may still have created a new media session. Later
		// recovery must compare against this attempt, not its predecessor.
		a.session = conn.identity()
	}
	a.mu.Unlock()
	if err != nil {
		conn.Close()
		var rejected *receiverRejection
		if errors.As(err, &rejected) && rejected.Type == "LOAD_CANCELLED" {
			a.takeover(gen)
			return
		}
		if errors.As(err, &rejected) && rejected.Type == "LOAD_FAILED" {
			a.retryCompatibility(gen)
			return
		}
		a.fail(gen, err)
		return
	}
	a.mu.Lock()
	if gen != a.generation || !a.requested {
		a.mu.Unlock()
		conn.Close()
		return
	}
	a.cast = conn
	a.session = conn.identity()
	a.phase = "connected"
	a.lastError = ""
	a.failures = 0
	a.last.Duration = plan.Duration
	a.mu.Unlock()
	cleanupMediaCache(filepath.Join(a.dataDir, "media-cache"), file)
}
func resolveDevice(r castRequest) string {
	host, port, e := net.SplitHostPort(r.Device)
	if e != nil {
		host, port = r.Device, "8009"
	}
	if r.DeviceHostname != "" {
		if ips, err := net.LookupIP(strings.TrimSuffix(r.DeviceHostname, ".")); err == nil {
			for _, ip := range ips {
				if ip.To4() != nil {
					host = ip.String()
					break
				}
			}
		}
	}
	return net.JoinHostPort(host, port)
}
func (a *app) status() map[string]any {
	a.mu.Lock()
	defer a.mu.Unlock()
	remote := a.last
	if a.phase == "connected" && a.cast != nil && a.cast.connected() {
		remote = a.cast.snapshot()
		if remote.PlayerState == "IDLE" && remote.IdleReason == "" && !remote.Ended && !remote.TakenOver {
			remote.CurrentTime = a.last.CurrentTime
			remote.PlayerState = "BUFFERING"
		}
		if remote.Duration == 0 {
			remote.Duration = a.plan.Duration
		}
	}
	if remote.Duration == 0 {
		remote.Duration = a.plan.Duration
	}
	selected := a.req.SelectedSubtitle
	enabled := false
	for _, id := range remote.ActiveTrackIDs {
		if id >= 10001 {
			selected = id - 10000
			enabled = true
			break
		}
	}
	if a.phase == "preparing" || a.phase == "connecting" {
		enabled = a.req.SubtitlesEnabled
	}
	style := remote.TextTrackStyle
	if style == nil {
		style = a.req.TextTrackStyle
	}
	if style == nil {
		style = subtitleStyle()
	}
	return map[string]any{"version": helperVersion, "parentPid": a.parentPID, "phase": a.phase, "casting": a.phase == "connected" && a.cast != nil && a.cast.connected(), "active": a.requested, "error": a.lastError, "progress": a.progress, "sourcePath": a.req.Path, "file": a.req.Path, "ownerId": a.req.OwnerID, "castId": a.req.CastID, "title": a.req.Title, "device": a.req.Device, "deviceName": a.req.DeviceName, "currentTime": remote.CurrentTime, "duration": remote.Duration, "playerState": remote.PlayerState, "volume": remote.Volume, "muted": remote.Muted, "activeTrackIds": remote.ActiveTrackIDs, "subtitles": a.choices, "selectedSubtitle": selected, "subtitlesEnabled": enabled, "textTrackStyle": style, "format": a.plan, "mediaHits": a.mediaHits.Load(), "subtitleHits": a.subtitleHits.Load(), "canReconnect": a.req.Path != "" && a.phase != "taken-over", "capabilities": commandCapabilities(remote.SupportedCommands), "mediaURLBase": a.mediaURLBase}
}
func (a *app) command(name string, r *http.Request) error {
	a.mu.Lock()
	if !scopeFromRequest(r).matches(a.req) {
		a.mu.Unlock()
		return errCastReplaced
	}
	c := a.cast
	gen := a.generation
	a.mu.Unlock()
	if c == nil || !c.connected() {
		return errors.New("TV is disconnected; choose Reconnect")
	}
	var err error
	switch name {
	case "play":
		err = c.play()
	case "pause":
		err = c.pause()
	case "seek":
		var b struct {
			Time float64 `json:"time"`
		}
		if e := decodeBody(r, &b); e != nil {
			return e
		}
		if math.IsNaN(b.Time) || math.IsInf(b.Time, 0) || b.Time < 0 {
			return errors.New("invalid seek position")
		}
		err = c.seek(b.Time)
	case "volume":
		var b struct {
			Level float64 `json:"level"`
			Muted bool    `json:"muted"`
		}
		if e := decodeBody(r, &b); e != nil {
			return e
		}
		err = c.setVolume(b.Level, b.Muted)
	case "subtitles":
		var b struct {
			Selected *int           `json:"selectedSubtitle"`
			Enabled  *bool          `json:"enabled"`
			Style    map[string]any `json:"style"`
		}
		if e := decodeBody(r, &b); e != nil {
			return e
		}
		a.mu.Lock()
		selected, enabled, style := a.req.SelectedSubtitle, a.req.SubtitlesEnabled, a.req.TextTrackStyle
		choices := a.choices
		a.mu.Unlock()
		if b.Selected != nil {
			selected = *b.Selected
		}
		if b.Enabled != nil {
			enabled = *b.Enabled
		}
		if b.Style != nil {
			style = subtitleStyle(style, b.Style)
			if v, ok := style["fontScale"].(float64); ok && (v < 0.5 || v > 3) {
				return errors.New("subtitle size must be between 0.5 and 3")
			}
		}
		ids := []int{}
		if enabled {
			found := false
			for _, s := range choices {
				if s.ID == selected {
					ids = append(ids, 10000+s.ID)
					found = true
				}
			}
			if !found {
				return errors.New("subtitle track is not available")
			}
		}
		err = c.editTracks(ids, style)
		if err == nil {
			a.mu.Lock()
			if gen == a.generation && a.cast == c {
				a.req.SelectedSubtitle = selected
				a.req.SubtitlesEnabled = enabled
				a.req.TextTrackStyle = style
			}
			a.mu.Unlock()
		}
	}
	if err == nil {
		err = c.queryStatus()
	}
	if errors.Is(err, errTakenOver) {
		a.takeover(gen)
		return err
	}
	if err == nil {
		a.mu.Lock()
		if gen == a.generation {
			a.last = c.snapshot()
		}
		a.mu.Unlock()
	}
	return err
}
func (a *app) stop() { a.stopFor(castScope{}, false) }
func (a *app) stopFor(scope castScope, abandon bool) {
	a.mu.Lock()
	if abandon {
		if !scope.specified() {
			scope = castScope{a.req.OwnerID, a.req.CastID}
		}
		a.retireLocked(scope)
	}
	if !scope.matches(a.req) {
		a.mu.Unlock()
		return
	}
	if a.cancel != nil {
		a.cancel()
	}
	a.generation++
	c := a.cast
	if c != nil {
		a.last = c.snapshot()
	}
	a.cast = nil
	a.file = ""
	a.mediaToken = ""
	a.assets = map[string]*subtitleAsset{}
	a.requested = false
	a.phase = "idle"
	a.lastError = ""
	a.recovering = false
	a.mu.Unlock()
	if c != nil {
		if c.connected() {
			_ = c.stop()
		}
		c.Close()
	}
}
func (a *app) handleMedia(w http.ResponseWriter, r *http.Request) {
	cors(w, r)
	if r.Method == http.MethodOptions {
		return
	}
	a.mu.Lock()
	file, ct, token, delivery := a.file, a.contentType, a.mediaToken, a.delivery
	a.mu.Unlock()
	if file == "" || r.URL.Path != "/media/"+token {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "no-store")
	a.mediaHits.Add(1)
	if delivery == nil {
		http.ServeFile(w, r, file)
		return
	}
	delivery.Requests.Add(1)
	http.ServeFile(&deliveryWriter{ResponseWriter: w, stats: delivery}, r, file)
}
func (a *app) monitor(ctx context.Context) {
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		a.mu.Lock()
		c, phase, desired, gen := a.cast, a.phase, a.requested, a.generation
		a.mu.Unlock()
		if !desired || phase != "connected" || c == nil {
			continue
		}
		err := error(nil)
		if !c.connected() {
			err = errors.New("connection closed")
		} else {
			err = c.queryStatus()
		}
		a.mu.Lock()
		if gen != a.generation {
			a.mu.Unlock()
			continue
		}
		if err != nil {
			a.failures++
		} else {
			a.failures = 0
		}
		failed := a.failures >= 2 || !c.connected()
		snap := c.snapshot()
		if err == nil && (snap.PlayerState == "PLAYING" || snap.PlayerState == "PAUSED") {
			a.last = snap
		}
		ended := snap.Ended || snap.PlayerState == "IDLE" && (snap.IdleReason == "FINISHED" || snap.IdleReason == "CANCELLED")
		takenOver := snap.TakenOver || snap.IdleReason == "INTERRUPTED" || errors.Is(err, errTakenOver)
		fallback := snap.IdleReason == "ERROR"
		if ended && !takenOver {
			a.requested = false
			a.phase = "ended"
			a.cast = nil
		}
		if failed && !a.recovering && !takenOver && !ended && !fallback {
			a.recovering = true
			a.phase = "reconnecting"
			a.lastError = "Connection lost; reconnecting to TV"
		}

		a.mu.Unlock()
		if takenOver {
			a.takeover(gen)
		} else if ended {
			c.Close()
		} else if fallback {
			a.retryCompatibility(gen)
		} else if failed {
			c.Close()
			go a.recover(gen)
		}
	}
}
func (a *app) baseFor(conn *castConn) (string, error) {
	if a.autoAddress {
		return conn.routeBase(a.mediaPort)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.mediaURLBase, nil
}
func commandCapabilities(bits *uint64) map[string]bool {
	allowed := func(bit uint64) bool { return bits == nil || *bits&bit != 0 }
	return map[string]bool{"pause": allowed(1), "seek": allowed(2), "editTracks": allowed(1 << 12)}
}
func (a *app) takeover(gen int) {
	a.mu.Lock()
	if gen != a.generation {
		a.mu.Unlock()
		return
	}
	c := a.cast
	a.cast = nil
	a.requested = false
	a.recovering = false
	a.phase = "taken-over"
	a.lastError = errTakenOver.Error()
	a.file = ""
	a.mediaToken = ""
	a.assets = map[string]*subtitleAsset{}
	if a.cancel != nil {
		a.cancel()
	}
	a.mu.Unlock()
	if c != nil {
		c.Close()
	}
}
func (a *app) retryCompatibility(gen int) {
	a.mu.Lock()
	if gen != a.generation || !a.requested {
		a.mu.Unlock()
		return
	}
	req, err := recoveryRequest(a.req, a.info, a.plan, a.delivery, a.last)
	a.mu.Unlock()
	if err != nil {
		a.fail(gen, err)
		return
	}
	logInfo("Retrying casting: delivery retry %d, audio conversion %v, video conversion %v", req.recovery.DeliveryRetries, req.recovery.Fallback.Audio, req.recovery.Fallback.Video)
	a.beginIfCurrent(gen, req)
}
func (a *app) beginIfCurrent(gen int, req castRequest) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if gen != a.generation || !a.requested {
		return
	}
	req.recovery.Automatic = true
	a.beginLocked(req)
}
func (a *app) reconnect() error { return a.reconnectFor(castScope{}) }
func (a *app) reconnectFor(scope castScope) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !scope.matches(a.req) {
		return errCastReplaced
	}
	if a.req.Path == "" {
		return errors.New("there is no previous casting session")
	}
	if a.phase == "taken-over" {
		return errTakenOver
	}
	if a.recovering {
		return nil
	}
	if a.cancel != nil {
		a.cancel()
	}
	a.ctx, a.cancel = context.WithCancel(context.Background())
	a.generation++
	gen := a.generation
	a.requested = true
	a.recovering = true
	a.phase = "reconnecting"
	a.lastError = ""
	go a.recover(gen)
	return nil
}
func (a *app) recover(gen int) {
	a.mu.Lock()
	ctx := a.ctx
	a.mu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	stopWatching := func() bool { return false }
	defer func() { stopWatching() }()
	for attempt := 0; attempt < 5; attempt++ {
		if !a.valid(gen) {
			return
		}
		if attempt > 0 {
			timer := time.NewTimer(time.Duration(1<<min(attempt, 3)) * time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			if !a.valid(gen) {
				return
			}
		}
		a.mu.Lock()
		req, plan, choices, last, token, base, identity, file := a.req, a.plan, a.choices, a.last, a.mediaToken, a.mediaURLBase, a.session, a.file
		a.mu.Unlock()
		conn, err := a.dial(resolveDevice(req))
		if err != nil {
			continue
		}
		stopWatching()
		stopWatching = context.AfterFunc(ctx, conn.Close)
		if !a.valid(gen) {
			conn.Close()
			return
		}
		attached, err := conn.attachOwned(base+"/media/"+token, identity)
		if errors.Is(err, errTakenOver) {
			conn.Close()
			a.takeover(gen)
			return
		}
		if errors.Is(err, errSessionEnded) {
			conn.Close()
			a.mu.Lock()
			if gen == a.generation {
				a.phase = "ended"
				a.requested = false
				a.recovering = false
			}
			a.mu.Unlock()
			return
		}
		if err != nil {
			conn.Close()
			continue
		}
		if file == "" && !attached {
			conn.Close()
			req.StartTime = last.CurrentTime
			play := last.PlayerState == "PLAYING"
			req.Autoplay = &play
			a.beginIfCurrent(gen, req)
			return
		}
		nextBase, err := a.baseFor(conn)
		if err != nil {
			conn.Close()
			continue
		}
		if !attached || nextBase != base {
			if attached {
				if err = conn.queryStatus(); err != nil {
					conn.Close()
					if errors.Is(err, errTakenOver) {
						a.takeover(gen)
						return
					}
					continue
				}
				last = conn.snapshot()
			}
			if !a.valid(gen) {
				conn.Close()
				return
			}
			a.mu.Lock()
			if gen == a.generation {
				a.mediaURLBase = nextBase
				a.delivery = &mediaDelivery{}
			}
			a.mu.Unlock()
			load := buildCatalogLoad(nextBase+"/media/"+token, nextBase, req.Title, plan, choices, req.SelectedSubtitle, req.SubtitlesEnabled, req.TextTrackStyle, last.CurrentTime, last.PlayerState == "PLAYING")
			err = conn.launchAndLoad(load)
			a.mu.Lock()
			if gen == a.generation && a.requested {
				a.session = conn.identity()
			}
			a.mu.Unlock()
			if err != nil {
				conn.Close()
				var rejected *receiverRejection
				if errors.As(err, &rejected) && rejected.Type == "LOAD_CANCELLED" {
					a.takeover(gen)
					return
				}
				if errors.As(err, &rejected) && rejected.Type == "LOAD_FAILED" {
					a.retryCompatibility(gen)
					return
				}
				continue
			}
		}
		a.mu.Lock()
		if gen != a.generation || !a.requested {
			a.mu.Unlock()
			conn.Close()
			return
		}
		old := a.cast
		a.cast = conn
		a.session = conn.identity()
		a.phase = "connected"
		a.recovering = false
		a.lastError = ""
		a.failures = 0
		a.mu.Unlock()
		if old != nil && old != conn {
			old.Close()
		}
		return
	}
	a.mu.Lock()
	if gen == a.generation {
		a.phase = "disconnected"
		a.lastError = "TV is unavailable. Turn it on, then choose Reconnect."
		a.requested = false
		a.recovering = false
		old := a.cast
		a.cast = nil
		if old != nil {
			old.Close()
		}
	}
	a.mu.Unlock()
}

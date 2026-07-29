package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// app holds everything the two HTTP servers share.
type app struct {
	mu           sync.Mutex
	file         string    // absolute path of the file currently being served
	contentType  string    // content type advertised to the device in LOAD
	cast         *castConn // active connection, or nil
	mediaURLBase string    // e.g. http://192.168.1.20:8010

	mediaHits atomic.Uint64 // how many times the device has fetched /media
	reqSeq    atomic.Uint64 // request counter, for correlating log lines
}

func main() {
	controlAddr := flag.String("control", "127.0.0.1:9092", "localhost control API address (for the IINA plugin)")
	mediaAddr := flag.String("media", "0.0.0.0:8010", "media server bind address (must be reachable by the Chromecast)")
	advertiseIP := flag.String("ip", "", "LAN IP to advertise to the device (default: autodetect)")
	verbosity := flag.Int("v", lvlDebug, "log verbosity: 0=error 1=warn 2=info 3=debug 4=trace (protobuf wire dumps)")
	logPath := flag.String("log", defaultLogPath(), "append logs to this file as well as stderr (empty to disable)")
	flag.Parse()

	setupLogging(*logPath, *verbosity)

	logInfo("=== casthelper starting: pid=%d args=%v ===", os.Getpid(), os.Args[1:])
	logInfo("flags: control=%s media=%s ip=%q verbosity=%d log=%s", *controlAddr, *mediaAddr, *advertiseIP, *verbosity, *logPath)
	logInterfaces()

	ip := *advertiseIP
	if ip == "" {
		ip = detectLANIP()
		logInfo("autodetected LAN IP: %s", ip)
	} else {
		logInfo("using LAN IP from -ip flag: %s", ip)
	}
	if ip == "127.0.0.1" || ip == "" {
		logWarn("advertising %q to the device — a Chromecast cannot reach this address; "+
			"pass -ip <your LAN IP> if autodetection picked the wrong interface", ip)
	}

	_, mediaPort, err := net.SplitHostPort(*mediaAddr)
	if err != nil {
		logError("cannot parse -media address %q: %v", *mediaAddr, err)
		os.Exit(1)
	}

	a := &app{mediaURLBase: fmt.Sprintf("http://%s:%s", ip, mediaPort)}
	logInfo("media URL advertised to devices: %s/media", a.mediaURLBase)

	// Media server: serves the current file to the Chromecast. http.ServeContent
	// (via ServeFile) honors Range requests automatically, so seeking works.
	mediaMux := http.NewServeMux()
	mediaMux.HandleFunc("/media", a.logged("media", a.handleMedia))
	mediaMux.HandleFunc("/", a.logged("media", func(w http.ResponseWriter, r *http.Request) {
		logWarn("media server: unexpected path %s requested by %s", r.URL.Path, r.RemoteAddr)
		http.NotFound(w, r)
	}))
	mediaLn, err := net.Listen("tcp", *mediaAddr)
	if err != nil {
		logError("media server cannot bind %s: %v", *mediaAddr, err)
		os.Exit(1)
	}
	logInfo("media server listening on %s", mediaLn.Addr())
	go func() {
		if err := http.Serve(mediaLn, mediaMux); err != nil {
			logError("media server stopped: %v", err)
			os.Exit(1)
		}
	}()

	// Control API: driven by the IINA plugin over localhost.
	ctrlMux := http.NewServeMux()
	ctrlMux.HandleFunc("/cast", a.logged("ctrl", a.handleCast))
	ctrlMux.HandleFunc("/play", a.logged("ctrl", a.simpleCmd("PLAY", func(c *castConn) error { return c.play() })))
	ctrlMux.HandleFunc("/pause", a.logged("ctrl", a.simpleCmd("PAUSE", func(c *castConn) error { return c.pause() })))
	ctrlMux.HandleFunc("/stop", a.logged("ctrl", a.handleStop))
	ctrlMux.HandleFunc("/seek", a.logged("ctrl", a.handleSeek))
	ctrlMux.HandleFunc("/status", a.logged("ctrl", a.handleStatus))
	ctrlMux.HandleFunc("/", a.logged("ctrl", func(w http.ResponseWriter, r *http.Request) {
		logWarn("control API: unknown endpoint %s %s", r.Method, r.URL.Path)
		http.NotFound(w, r)
	}))

	ctrlLn, err := net.Listen("tcp", *controlAddr)
	if err != nil {
		logError("control API cannot bind %s: %v (another casthelper still running?)", *controlAddr, err)
		os.Exit(1)
	}
	logInfo("control API listening on %s", ctrlLn.Addr())
	go func() {
		if err := http.Serve(ctrlLn, ctrlMux); err != nil {
			logError("control server stopped: %v", err)
			os.Exit(1)
		}
	}()

	// Signal the plugin (which reads our stdout) that we're up.
	fmt.Println("READY")
	logInfo("wrote READY to stdout; waiting for control requests")

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	s := <-sig
	logInfo("received signal %s, shutting down", s)
	a.mu.Lock()
	if a.cast != nil {
		logInfo("tearing down active cast session")
		a.cast.stopReceiver()
		a.cast.Close()
	}
	a.mu.Unlock()
	logInfo("=== casthelper exiting ===")
}

// respRecorder captures what a handler actually replied so it can be logged.
type respRecorder struct {
	http.ResponseWriter
	status int
	n      int64
	body   bytes.Buffer // only filled for small non-media responses
	record bool
}

func (r *respRecorder) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *respRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	if r.record && r.body.Len() < 2048 {
		r.body.Write(b)
	}
	n, err := r.ResponseWriter.Write(b)
	r.n += int64(n)
	return n, err
}

// logged wraps a handler with request/response logging. Every request gets a
// sequence number so its log lines can be matched up.
func (a *app) logged(kind string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := a.reqSeq.Add(1)
		start := time.Now()

		logInfo("[%s#%d] --> %s %s from %s", kind, id, r.Method, r.URL.RequestURI(), r.RemoteAddr)
		if rng := r.Header.Get("Range"); rng != "" {
			logInfo("[%s#%d]     Range: %s  User-Agent: %s", kind, id, rng, r.Header.Get("User-Agent"))
		}
		logTrace("[%s#%d]     headers: %v", kind, id, r.Header)

		rec := &respRecorder{ResponseWriter: w, record: kind == "ctrl"}
		h(rec, r)

		if rec.status == 0 {
			rec.status = http.StatusOK
		}
		body := ""
		if rec.record && rec.body.Len() > 0 {
			body = " body=" + truncate(rec.body.String(), 400)
		}
		logInfo("[%s#%d] <-- %d (%d bytes) in %s%s", kind, id, rec.status, rec.n, time.Since(start).Round(time.Millisecond), body)
	}
}

func (a *app) handleMedia(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	path, ct := a.file, a.contentType
	a.mu.Unlock()

	hits := a.mediaHits.Add(1)
	logInfo("media: request #%d for %q (content-type %q) from %s", hits, path, ct, r.RemoteAddr)

	if path == "" {
		logError("media: no file loaded yet — the device asked for /media before /cast set a file")
		http.Error(w, "no file loaded", http.StatusNotFound)
		return
	}
	if st, err := os.Stat(path); err != nil {
		logError("media: cannot stat %q: %v — the device will get an error response", path, err)
	} else {
		logDebug("media: serving %q (%d bytes, mode %s)", path, st.Size(), st.Mode())
	}

	if ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	// ServeFile adds Accept-Ranges and answers 206 Partial Content for range
	// requests, which is what lets the device seek.
	http.ServeFile(w, r, path)
}

type castRequest struct {
	Device      string `json:"device"`      // "192.168.1.50" or "192.168.1.50:8009"
	Path        string `json:"path"`        // absolute path of the local file
	ContentType string `json:"contentType"` // e.g. "video/mp4"
	Title       string `json:"title"`
}

func (a *app) handleCast(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		logError("cast: cannot read request body: %v", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	logInfo("cast: raw request body (%d bytes): %s", len(raw), truncate(string(raw), 1000))
	if len(raw) == 0 {
		logError("cast: empty request body — the plugin sent no JSON payload")
		http.Error(w, "empty request body", http.StatusBadRequest)
		return
	}

	var req castRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		logError("cast: cannot decode JSON body: %v (body was: %s)", err, truncate(string(raw), 500))
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	logInfo("cast: parsed device=%q path=%q contentType=%q title=%q", req.Device, req.Path, req.ContentType, req.Title)

	if req.Device == "" || req.Path == "" {
		logError("cast: missing required field(s): device=%q path=%q", req.Device, req.Path)
		http.Error(w, "device and path are required", http.StatusBadRequest)
		return
	}
	if _, _, err := net.SplitHostPort(req.Device); err != nil {
		logDebug("cast: device %q has no port (%v), defaulting to 8009", req.Device, err)
		req.Device = net.JoinHostPort(req.Device, "8009")
	}
	if req.ContentType == "" {
		req.ContentType = "video/mp4"
		logDebug("cast: no contentType given, defaulting to %s", req.ContentType)
	}

	// The device will fetch this file over HTTP; if we cannot read it, nothing
	// downstream can work.
	if !filepath.IsAbs(req.Path) {
		logWarn("cast: path %q is not absolute — it resolves against the helper's cwd, not IINA's", req.Path)
	}
	if st, err := os.Stat(req.Path); err != nil {
		logError("cast: file %q is not readable: %v", req.Path, err)
	} else {
		logInfo("cast: file %q ok (%d bytes, mode %s, ext %q)", req.Path, st.Size(), st.Mode(), filepath.Ext(req.Path))
		if f, err := os.Open(req.Path); err != nil {
			logError("cast: file %q stat'd but cannot be opened: %v", req.Path, err)
		} else {
			f.Close()
		}
	}
	logMediaTypeHint(req.Path, req.ContentType)

	a.mu.Lock()
	a.file = req.Path
	a.contentType = req.ContentType
	if a.cast != nil {
		logInfo("cast: closing previous cast connection")
		a.cast.Close()
		a.cast = nil
	}
	base := a.mediaURLBase
	a.mu.Unlock()

	logInfo("cast: dialing device %s ...", req.Device)
	dialStart := time.Now()
	conn, err := dialCast(req.Device)
	if err != nil {
		logError("cast: dial %s failed after %s: %v", req.Device, time.Since(dialStart).Round(time.Millisecond), err)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	logInfo("cast: connected to %s in %s", req.Device, time.Since(dialStart).Round(time.Millisecond))

	mediaURL := base + "/media"
	logInfo("cast: launching receiver and loading %s", mediaURL)
	loadStart := time.Now()
	if err := conn.launchAndLoad(mediaURL, req.ContentType, req.Title); err != nil {
		logError("cast: launchAndLoad failed after %s: %v", time.Since(loadStart).Round(time.Millisecond), err)
		conn.Close()
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	logInfo("cast: LOAD accepted in %s", time.Since(loadStart).Round(time.Millisecond))

	a.mu.Lock()
	a.cast = conn
	a.mu.Unlock()

	a.watchForMediaFetch(mediaURL)

	writeJSON(w, map[string]any{"ok": true, "mediaUrl": mediaURL})
}

// watchForMediaFetch flags the most common failure mode: the device accepts the
// LOAD but never connects back to our media server (wrong advertised IP,
// firewall, or a different subnet).
func (a *app) watchForMediaFetch(mediaURL string) {
	before := a.mediaHits.Load()
	go func() {
		time.Sleep(15 * time.Second)
		if a.mediaHits.Load() == before {
			logWarn("diagnostic: 15s after LOAD the device has still not requested %s — "+
				"it probably cannot reach us. Check that the advertised IP is on the same "+
				"subnet as the device and that the local firewall allows inbound connections "+
				"to the media port.", mediaURL)
		} else {
			logInfo("diagnostic: device fetched the media URL, byte streaming is underway")
		}
	}()
}

func (a *app) simpleCmd(name string, fn func(*castConn) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		conn := a.cast
		a.mu.Unlock()
		if conn == nil {
			logWarn("%s: no active cast connection (was /cast ever called, or did it fail?)", name)
			http.Error(w, "not casting", http.StatusConflict)
			return
		}
		logInfo("%s: sending command", name)
		if err := fn(conn); err != nil {
			logError("%s: command failed: %v", name, err)
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		logInfo("%s: command acknowledged", name)
		writeJSON(w, map[string]any{"ok": true})
	}
}

func (a *app) handleSeek(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<16))
	if err != nil {
		logError("seek: cannot read body: %v", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	logInfo("seek: raw body: %s", truncate(string(raw), 300))

	var body struct {
		Time float64 `json:"time"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		logError("seek: cannot decode body: %v", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	logInfo("seek: to %.3fs", body.Time)
	a.simpleCmd("SEEK", func(c *castConn) error { return c.seek(body.Time) })(w, r)
}

func (a *app) handleStop(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	conn := a.cast
	a.cast = nil
	a.mu.Unlock()
	if conn != nil {
		logInfo("stop: stopping media, receiver, and closing connection")
		if err := conn.stop(); err != nil {
			logWarn("stop: STOP command failed (continuing teardown): %v", err)
		}
		conn.stopReceiver()
		conn.Close()
	} else {
		logWarn("stop: no active cast connection")
	}
	writeJSON(w, map[string]any{"ok": true})
}

// handleStatus dumps the helper's view of the world — handy when the plugin
// looks connected but nothing is playing.
func (a *app) handleStatus(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	file, ct, conn, base := a.file, a.contentType, a.cast, a.mediaURLBase
	a.mu.Unlock()

	st := map[string]any{
		"file":         file,
		"contentType":  ct,
		"mediaURLBase": base,
		"casting":      conn != nil,
		"mediaHits":    a.mediaHits.Load(),
	}
	if conn != nil {
		conn.sessMu.Lock()
		st["transportId"] = conn.transportID
		st["mediaSessionId"] = conn.mediaSessionID
		st["playerState"] = conn.playerState
		st["idleReason"] = conn.idleReason
		conn.sessMu.Unlock()
	}
	logInfo("status: %+v", st)
	writeJSON(w, st)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		logError("cannot write JSON response: %v", err)
	}
}

// logMediaTypeHint warns about combinations the Default Media Receiver is known
// to reject outright, which looks like "casting silently does nothing".
func logMediaTypeHint(path, contentType string) {
	ext := filepath.Ext(path)
	switch ext {
	case ".mkv", ".avi", ".wmv", ".flv", ".ts", ".m2ts":
		logWarn("media type: %s is not supported by the Default Media Receiver (CC1AD845); "+
			"the device will accept LOAD and then go IDLE with idleReason=ERROR. "+
			"Only MP4/WebM containers with H.264/VP8 video and AAC/Vorbis audio play reliably.", ext)
	}
	if ext == ".mp4" && contentType != "video/mp4" {
		logWarn("media type: %s is advertised as %q; a mismatched content type makes the receiver refuse the stream", ext, contentType)
	}
}

// detectLANIP finds the outbound LAN address without sending any packets
// (UDP "connect" just picks the route).
func detectLANIP() string {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		logError("detectLANIP: cannot determine outbound address: %v (falling back to 127.0.0.1)", err)
		return "127.0.0.1"
	}
	defer conn.Close()
	ip := conn.LocalAddr().(*net.UDPAddr).IP.String()
	logDebug("detectLANIP: outbound route to 8.8.8.8:80 uses local address %s", ip)
	return ip
}

// logInterfaces prints every usable local address, so a wrong -ip guess is
// obvious from the log alone.
func logInterfaces() {
	ifaces, err := net.Interfaces()
	if err != nil {
		logError("cannot enumerate network interfaces: %v", err)
		return
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			logWarn("cannot list addresses for %s: %v", iface.Name, err)
			continue
		}
		for _, addr := range addrs {
			if ipnet, ok := addr.(*net.IPNet); ok && ipnet.IP.To4() != nil {
				logInfo("interface %s: %s", iface.Name, ipnet.String())
			}
		}
	}
}

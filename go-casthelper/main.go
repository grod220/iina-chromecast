package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
)

// app holds everything the two HTTP servers share.
type app struct {
	mu           sync.Mutex
	file         string    // absolute path of the file currently being served
	contentType  string    // content type advertised to the device in LOAD
	cast         *castConn // active connection, or nil
	mediaURLBase string    // e.g. http://192.168.1.20:8010
}

func main() {
	controlAddr := flag.String("control", "127.0.0.1:9092", "localhost control API address (for the IINA plugin)")
	mediaAddr := flag.String("media", "0.0.0.0:8010", "media server bind address (must be reachable by the Chromecast)")
	advertiseIP := flag.String("ip", "", "LAN IP to advertise to the device (default: autodetect)")
	flag.Parse()

	ip := *advertiseIP
	if ip == "" {
		ip = detectLANIP()
	}
	_, mediaPort, _ := net.SplitHostPort(*mediaAddr)

	a := &app{mediaURLBase: fmt.Sprintf("http://%s:%s", ip, mediaPort)}

	// Media server: serves the current file to the Chromecast. http.ServeContent
	// (via ServeFile) honors Range requests automatically, so seeking works.
	mediaMux := http.NewServeMux()
	mediaMux.HandleFunc("/media", a.handleMedia)
	go func() {
		log.Printf("media server on %s (advertising %s/media)", *mediaAddr, a.mediaURLBase)
		if err := http.ListenAndServe(*mediaAddr, mediaMux); err != nil {
			log.Fatalf("media server: %v", err)
		}
	}()

	// Control API: driven by the IINA plugin over localhost.
	ctrlMux := http.NewServeMux()
	ctrlMux.HandleFunc("/cast", a.handleCast)
	ctrlMux.HandleFunc("/play", a.simpleCmd(func(c *castConn) error { return c.play() }))
	ctrlMux.HandleFunc("/pause", a.simpleCmd(func(c *castConn) error { return c.pause() }))
	ctrlMux.HandleFunc("/stop", a.handleStop)
	ctrlMux.HandleFunc("/seek", a.handleSeek)

	go func() {
		log.Printf("control API on %s", *controlAddr)
		if err := http.ListenAndServe(*controlAddr, ctrlMux); err != nil {
			log.Fatalf("control server: %v", err)
		}
	}()

	// Signal the plugin (which reads our stdout) that we're up.
	fmt.Println("READY")

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	a.mu.Lock()
	if a.cast != nil {
		a.cast.stopReceiver()
		a.cast.Close()
	}
	a.mu.Unlock()
}

func (a *app) handleMedia(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	path, ct := a.file, a.contentType
	a.mu.Unlock()
	if path == "" {
		http.Error(w, "no file loaded", http.StatusNotFound)
		return
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
	var req castRequest
	fmt.Println("Casting now...")
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.Device == "" || req.Path == "" {
		http.Error(w, "device and path are required", http.StatusBadRequest)
		return
	}
	if _, _, err := net.SplitHostPort(req.Device); err != nil {
		req.Device = net.JoinHostPort(req.Device, "8009")
	}
	if req.ContentType == "" {
		req.ContentType = "video/mp4"
	}

	a.mu.Lock()
	a.file = req.Path
	a.contentType = req.ContentType
	if a.cast != nil {
		a.cast.Close()
		a.cast = nil
	}
	base := a.mediaURLBase
	a.mu.Unlock()

	conn, err := dialCast(req.Device)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	mediaURL := base + "/media"
	if err := conn.launchAndLoad(mediaURL, req.ContentType, req.Title); err != nil {
		conn.Close()
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	a.mu.Lock()
	a.cast = conn
	a.mu.Unlock()

	writeJSON(w, map[string]any{"ok": true, "mediaUrl": mediaURL})
}

func (a *app) simpleCmd(fn func(*castConn) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		conn := a.cast
		a.mu.Unlock()
		if conn == nil {
			http.Error(w, "not casting", http.StatusConflict)
			return
		}
		if err := fn(conn); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	}
}

func (a *app) handleSeek(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Time float64 `json:"time"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	a.simpleCmd(func(c *castConn) error { return c.seek(body.Time) })(w, r)
}

func (a *app) handleStop(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	conn := a.cast
	a.cast = nil
	a.mu.Unlock()
	if conn != nil {
		_ = conn.stop()
		conn.stopReceiver()
		conn.Close()
	}
	writeJSON(w, map[string]any{"ok": true})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// detectLANIP finds the outbound LAN address without sending any packets
// (UDP "connect" just picks the route).
func detectLANIP() string {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return "127.0.0.1"
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).IP.String()
}

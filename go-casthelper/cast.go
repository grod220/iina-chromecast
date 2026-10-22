package main

import (
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

const (
	nsConnection = "urn:x-cast:com.google.cast.tp.connection"
	nsHeartbeat  = "urn:x-cast:com.google.cast.tp.heartbeat"
	nsReceiver   = "urn:x-cast:com.google.cast.receiver"
	nsMedia      = "urn:x-cast:com.google.cast.media"

	defaultReceiverAppID  = "CC1AD845" // Default Media Receiver
	backdropReceiverAppID = "E8C28D3C" // Platform's idle Backdrop, observed on SWTV-20TW
	senderID              = "sender-0"
	platformReceiverID    = "receiver-0"
)

// castConn is a live connection to one Chromecast.
type castConn struct {
	conn     io.ReadWriteCloser
	addr     string // remote address, for log context
	senderID string // unique across overlapping reconnect/probe connections

	writeOnce   sync.Once
	writeGate   chan struct{} // deadline-aware serialization of heartbeat and control writes
	lastInbound atomic.Int64
	lastPong    atomic.Int64
	reqMu       sync.Mutex
	reqID       int

	waitMu          sync.Mutex
	waiters         map[int]chan map[string]any
	waiterSource    map[int]string
	waiterNamespace map[int]string

	sessMu            sync.Mutex
	transportID       string
	mediaSessionID    int
	playerState       string // last playerState seen in a MEDIA_STATUS
	idleReason        string // last idleReason seen in a MEDIA_STATUS
	currentTime       float64
	duration          float64
	positionUpdated   time.Time
	playbackRate      float64
	tracks            []map[string]any
	activeTrackIDs    []int
	textTrackStyle    map[string]any
	volume            float64
	muted             bool
	receiverSession   string
	sessionEnded      bool
	expectedURL       string
	ownsMedia         bool
	takenOver         bool
	loading           bool
	loadRequestID     int
	loadStarted       time.Time
	supportedCommands *uint64

	sent atomic.Uint64 // frames written
	recv atomic.Uint64 // frames read

	closeOnce sync.Once
	done      chan struct{}
}

// dialCast opens the TLS socket, starts the read loop + heartbeat, and opens the
// virtual connection to the platform receiver (receiver-0).
func dialCast(addr string) (*castConn, error) {
	logDebug("dial: TLS handshake with %s (timeout 8s, cert verification disabled)", addr)
	start := time.Now()
	tlsConn, err := tls.DialWithDialer(&net.Dialer{Timeout: 8 * time.Second}, "tcp", addr, &tls.Config{
		InsecureSkipVerify: true, // devices present a self-signed cert
		MinVersion:         tls.VersionTLS12,
	})
	if err != nil {
		logError("dial: TLS dial %s failed: %v", addr, err)
		return nil, fmt.Errorf("tls dial %s: %w", addr, err)
	}
	cs := tlsConn.ConnectionState()
	logInfo("dial: connected to %s in %s (local %s, TLS %#04x, cipher %#04x, %d peer certs)",
		addr, time.Since(start).Round(time.Millisecond), tlsConn.LocalAddr(),
		cs.Version, cs.CipherSuite, len(cs.PeerCertificates))
	if len(cs.PeerCertificates) > 0 {
		logDebug("dial: peer cert subject=%q issuer=%q", cs.PeerCertificates[0].Subject, cs.PeerCertificates[0].Issuer)
	}

	c := &castConn{
		conn:         tlsConn,
		addr:         addr,
		senderID:     newSenderID(),
		waiters:      make(map[int]chan map[string]any),
		done:         make(chan struct{}),
		playbackRate: 1,
	}
	c.lastInbound.Store(time.Now().UnixNano())
	go c.readLoop()

	// Open the tp.connection virtual channel to the platform receiver.
	logDebug("dial: opening virtual connection to %s", platformReceiverID)
	if err := c.sendJSON(nsConnection, platformReceiverID, map[string]any{"type": "CONNECT"}); err != nil {
		logError("dial: CONNECT to %s failed: %v", platformReceiverID, err)
		c.Close()
		return nil, err
	}
	go c.heartbeatLoop()
	logDebug("dial: setup complete for %s", addr)
	return c, nil
}

func (c *castConn) nextReqID() int {
	c.reqMu.Lock()
	defer c.reqMu.Unlock()
	c.reqID++
	return c.reqID
}

// send frames one CastMessage: 4-byte big-endian length prefix + protobuf bytes.
func (c *castConn) send(m castMessage) error { return c.sendDeadline(m, time.Time{}) }
func (c *castConn) sendDeadline(m castMessage, deadline time.Time) error {
	payload := encodeCastMessage(m)
	frame := make([]byte, 4+len(payload))
	binary.BigEndian.PutUint32(frame, uint32(len(payload)))
	copy(frame[4:], payload)
	c.sent.Add(1)
	return c.writeFrame(frame, deadline)
}

func (c *castConn) sendJSON(ns, dest string, payload map[string]any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		logError("sendJSON: cannot marshal payload for ns=%s dest=%s: %v", shortNS(ns), dest, err)
		return err
	}
	return c.send(castMessage{
		sourceID:    c.sender(),
		destID:      dest,
		namespace:   ns,
		payloadUTF8: string(b),
	})
}

// request sends a JSON message tagged with a requestId and waits for the reply
// carrying the same requestId.
func (c *castConn) request(ns, dest string, payload map[string]any) (map[string]any, error) {
	return c.requestTimeout(ns, dest, payload, 10*time.Second)
}
func (c *castConn) requestTimeout(ns, dest string, payload map[string]any, timeout time.Duration) (map[string]any, error) {
	id := c.nextReqID()
	payload["requestId"] = id
	kind, _ := payload["type"].(string)

	ch := make(chan map[string]any, 1)
	c.waitMu.Lock()
	c.waiters[id] = ch
	if c.waiterSource == nil {
		c.waiterSource = map[int]string{}
		c.waiterNamespace = map[int]string{}
	}
	c.waiterSource[id] = dest
	c.waiterNamespace[id] = ns
	c.waitMu.Unlock()
	defer func() {
		c.waitMu.Lock()
		delete(c.waiters, id)
		delete(c.waiterSource, id)
		delete(c.waiterNamespace, id)
		c.waitMu.Unlock()
	}()

	logDebug("request: %s requestId=%d -> %s (ns=%s)", kind, id, dest, shortNS(ns))
	start := time.Now()
	deadline := start.Add(timeout)
	if kind == "LOAD" {
		c.sessMu.Lock()
		c.loadRequestID = id
		c.sessMu.Unlock()
	}
	body, marshalErr := json.Marshal(payload)
	if marshalErr != nil {
		return nil, marshalErr
	}
	if err := c.sendDeadline(castMessage{sourceID: c.sender(), destID: dest, namespace: ns, payloadUTF8: string(body)}, deadline); err != nil {
		logError("request: %s requestId=%d could not be sent: %v", kind, id, err)
		return nil, err
	}
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case reply := <-ch:
		rt, _ := reply["type"].(string)
		logDebug("request: %s requestId=%d answered with %s in %s", kind, id, rt, time.Since(start).Round(time.Millisecond))
		switch rt {
		case "INVALID_REQUEST", "INVALID_PLAYER_STATE", "ERROR", "LOAD_FAILED", "LOAD_CANCELLED", "LAUNCH_ERROR":
			return reply, &receiverRejection{Command: kind, Type: rt}
		}
		return reply, nil
	case <-timer.C:
		logError("request: %s requestId=%d timed out after %s with no reply from %s", kind, id, timeout, dest)
		return nil, errors.New("timed out waiting for device reply")
	case <-c.done:
		logError("request: %s requestId=%d aborted — connection closed", kind, id)
		return nil, errors.New("connection closed")
	}
}

func (c *castConn) readLoop() {
	defer func() {
		logInfo("readLoop: exiting for %s (read %d frames, sent %d)", c.addr, c.recv.Load(), c.sent.Load())
		c.Close()
	}()
	logDebug("readLoop: started for %s", c.addr)
	hdr := make([]byte, 4)
	for {
		c.readDeadline(receiveTimeout)
		if _, err := io.ReadFull(c.conn, hdr); err != nil {
			if errors.Is(err, io.EOF) {
				logWarn("readLoop: device %s closed the connection", c.addr)
			} else {
				logError("readLoop: reading length prefix failed: %v", err)
			}
			return
		}
		n := binary.BigEndian.Uint32(hdr)
		if n == 0 || n > 1<<22 {
			logError("readLoop: implausible frame length %d — the stream is out of sync, giving up", n)
			return
		}
		buf := make([]byte, n)
		c.readDeadline(5 * time.Second)
		if _, err := io.ReadFull(c.conn, buf); err != nil {
			logError("readLoop: reading %d-byte frame body failed: %v", n, err)
			return
		}
		c.lastInbound.Store(time.Now().UnixNano())
		seq := c.recv.Add(1)
		if traceEnabled() {
			logTrace("recv #%d wire (%d bytes): %s", seq, n, hexdump(buf, 256))
		}
		c.handle(seq, decodeCastMessage(buf))
	}
}

func (c *castConn) handle(seq uint64, m castMessage) {
	logDebug("recv #%d ns=%s src=%s dest=%s payload=%s", seq, shortNS(m.namespace), m.sourceID, m.destID, truncate(m.payloadUTF8, 600))
	if m.destID != "" && m.destID != "*" && m.destID != c.sender() {
		return
	}

	if m.payloadUTF8 == "" {
		logWarn("recv #%d: message has no UTF-8 payload (binary payload or decode failure) — ignoring", seq)
		return
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(m.payloadUTF8), &payload); err != nil {
		logError("recv #%d: payload is not JSON: %v (payload: %s)", seq, err, truncate(m.payloadUTF8, 300))
		return
	}
	msgType, _ := payload["type"].(string)
	if _, ok := payload["requestId"]; !ok {
		if id, ok := payload["launchRequestId"]; ok {
			payload["requestId"] = id
		}
	}

	// Answer heartbeat pings so the device keeps us alive.
	if m.namespace == nsHeartbeat {
		if msgType == "PING" {
			logTrace("recv #%d: PING from %s, replying PONG", seq, m.sourceID)
			if err := c.sendJSON(nsHeartbeat, platformReceiverID, map[string]any{"type": "PONG"}); err != nil {
				logError("recv #%d: sending PONG failed: %v", seq, err)
			}
		} else if msgType == "PONG" {
			c.lastPong.Store(time.Now().UnixNano())
		}
		return
	}

	// Only the selected application transport may update our media state.
	if m.namespace == nsMedia {
		c.sessMu.Lock()
		valid := c.transportID != "" && m.sourceID == c.transportID
		c.sessMu.Unlock()
		if !valid {
			return
		}
	}
	if m.namespace == nsReceiver && m.sourceID != platformReceiverID {
		return
	}
	if id, ok := payload["requestId"].(float64); ok && id > 0 {
		c.waitMu.Lock()
		source, space := c.waiterSource[int(id)], c.waiterNamespace[int(id)]
		c.waitMu.Unlock()
		if source != "" && (source != m.sourceID || space != m.namespace) {
			return
		}
	}
	// The device closes a virtual connection when it drops us; without this the
	// symptom is a silent stall.
	if m.namespace == nsConnection && msgType == "CLOSE" {
		c.sessMu.Lock()
		ours := m.sourceID == c.transportID || m.sourceID == platformReceiverID
		c.sessMu.Unlock()
		logWarn("recv #%d: device closed the virtual connection from %s (reason: %v)", seq, m.sourceID, payload["reason"])
		// CLOSE ends a control channel, not necessarily playback. Reconnect
		// and inspect the receiver; only media/receiver status can establish
		// completion or takeover. In particular, an idle receiver may retire
		// immediately after accepting a new LOAD.
		if ours {
			c.Close()
		}
		return
	}

	switch msgType {
	case "MEDIA_STATUS":
		c.logMediaStatus(seq, payload)
	case "RECEIVER_STATUS":
		c.logReceiverStatus(seq, payload)
	case "LOAD_FAILED", "LOAD_CANCELLED", "INVALID_PLAYER_STATE", "INVALID_REQUEST", "ERROR":
		logError("recv #%d: device reported %s — detail: %s", seq, msgType, truncate(m.payloadUTF8, 600))
	}

	// Route replies that carry a requestId to whoever is waiting.
	if idf, ok := payload["requestId"].(float64); ok {
		id := int(idf)
		if id == 0 {
			logTrace("recv #%d: broadcast (requestId 0), not a reply", seq)
			return
		}
		c.waitMu.Lock()
		ch := c.waiters[id]
		c.waitMu.Unlock()
		if ch == nil {
			logWarn("recv #%d: reply for requestId=%d has no waiter (already timed out?)", seq, id)
			return
		}
		select {
		case ch <- payload:
			logTrace("recv #%d: delivered reply for requestId=%d", seq, id)
		default:
			logWarn("recv #%d: waiter channel for requestId=%d was full, dropping reply", seq, id)
		}
	} else {
		logTrace("recv #%d: message carries no requestId (unsolicited broadcast)", seq)
	}
}

// logMediaStatus records and reports the player state. idleReason=ERROR here is
// the device's way of saying "I accepted your LOAD but could not play the URL".
func (c *castConn) logMediaStatus(seq uint64, payload map[string]any) {
	arr, ok := payload["status"].([]any)
	if !ok || len(arr) == 0 {
		// An empty status array is what the receiver sends when nothing is
		// loaded — i.e. the media session went away.
		c.sessMu.Lock()
		if c.ownsMedia && !c.loading {
			c.sessionEnded = true
			c.ownsMedia = false
			c.playerState = "IDLE"
		}
		c.sessMu.Unlock()
		return
	}
	for _, s := range arr {
		s0, ok := s.(map[string]any)
		if !ok {
			continue
		}
		state, _ := s0["playerState"].(string)
		idle, _ := s0["idleReason"].(string)
		cur, _ := s0["currentTime"].(float64)

		c.sessMu.Lock()
		id, _ := s0["mediaSessionId"].(float64)
		media, _ := s0["media"].(map[string]any)
		url, _ := media["contentId"].(string)
		requestID, _ := payload["requestId"].(float64)
		if c.expectedURL == "" || c.takenOver {
			c.sessMu.Unlock()
			continue
		}
		foreign := (c.mediaSessionID != 0 && id != 0 && c.mediaSessionID != int(id)) || (url != "" && url != c.expectedURL)
		if foreign {
			if (c.ownsMedia && !c.loading) || (requestID > 0 && int(requestID) != c.loadRequestID) {
				c.takenOver = true
				c.ownsMedia = false
			}
			c.sessMu.Unlock()
			continue
		}
		if c.mediaSessionID == 0 && id > 0 {
			if url != c.expectedURL && int(requestID) != c.loadRequestID {
				c.sessMu.Unlock()
				continue
			}
			c.mediaSessionID = int(id)
			c.ownsMedia = true
		}
		if state == "IDLE" && idle == "INTERRUPTED" && !c.loading {
			c.takenOver = true
			c.ownsMedia = false
			c.sessMu.Unlock()
			continue
		}
		if state == "PLAYING" || state == "PAUSED" || state == "BUFFERING" || idle != "" {
			c.loading = false
		}
		if extended, ok := s0["extendedStatus"].(map[string]any); ok && extended["playerState"] == "LOADING" {
			state = "LOADING"
		}
		if commands, ok := s0["supportedMediaCommands"].(float64); ok {
			bits := uint64(commands)
			c.supportedCommands = &bits
		}
		if state != "" {
			c.playerState = state
		}
		c.idleReason = idle
		if _, ok := s0["currentTime"]; ok {
			c.currentTime = cur
			c.positionUpdated = time.Now()
		}
		if rate, ok := s0["playbackRate"].(float64); ok {
			c.playbackRate = rate
		}
		if ids, ok := s0["activeTrackIds"].([]any); ok {
			c.activeTrackIDs = []int{}
			for _, raw := range ids {
				if id, ok := raw.(float64); ok {
					c.activeTrackIDs = append(c.activeTrackIDs, int(id))
				}
			}
		}
		if style, ok := s0["textTrackStyle"].(map[string]any); ok {
			c.textTrackStyle = style
		}
		if media, ok := s0["media"].(map[string]any); ok {
			if style, ok := media["textTrackStyle"].(map[string]any); ok {
				c.textTrackStyle = style
			}
			if duration, ok := media["duration"].(float64); ok {
				c.duration = duration
			}
			if ts, ok := media["tracks"].([]any); ok {
				c.tracks = nil
				for _, t := range ts {
					if track, ok := t.(map[string]any); ok {
						c.tracks = append(c.tracks, track)
					}
				}
			}
		}
		session := c.mediaSessionID
		c.sessMu.Unlock()

		logInfo("recv #%d: MEDIA_STATUS session=%d playerState=%s currentTime=%.1fs idleReason=%q", seq, session, state, cur, idle)
		if media, ok := s0["media"].(map[string]any); ok {
			logDebug("recv #%d:   media contentId=%v contentType=%v duration=%v", seq, media["contentId"], media["contentType"], media["duration"])
		}
		if idle == "ERROR" {
			logError("recv #%d: device went IDLE with idleReason=ERROR — it tried to fetch or play the "+
				"media URL and failed. Usual causes: unsupported container/codec, a media URL the "+
				"device cannot reach, or a content type that does not match the file.", seq)
		}
	}
}

func (c *castConn) logReceiverStatus(seq uint64, payload map[string]any) {
	status, ok := payload["status"].(map[string]any)
	if !ok {
		logWarn("recv #%d: RECEIVER_STATUS without a status object", seq)
		return
	}
	if v, ok := status["volume"].(map[string]any); ok {
		c.sessMu.Lock()
		if level, ok := v["level"].(float64); ok {
			c.volume = level
		}
		if muted, ok := v["muted"].(bool); ok {
			c.muted = muted
		}
		c.sessMu.Unlock()
	}
	apps, hasApps := status["applications"].([]any)
	c.sessMu.Lock()
	if hasApps && c.ownsMedia && !c.loading {
		match := false
		for _, raw := range apps {
			app, _ := raw.(map[string]any)
			if app["transportId"] == c.transportID && app["sessionId"] == c.receiverSession {
				match = true
			}
		}
		if !match {
			if hasPlaybackApplication(apps) {
				c.takenOver = true
			} else {
				c.sessionEnded = true
			}
			c.ownsMedia = false
		}
	}
	c.sessMu.Unlock()
	if len(apps) == 0 {
		return
	}
	for _, a := range apps {
		app, ok := a.(map[string]any)
		if !ok {
			continue
		}
		logInfo("recv #%d: RECEIVER_STATUS app appId=%v displayName=%v statusText=%q transportId=%v sessionId=%v",
			seq, app["appId"], app["displayName"], app["statusText"], app["transportId"], app["sessionId"])
	}
}

func hasPlaybackApplication(apps []any) bool {
	for _, raw := range apps {
		app, _ := raw.(map[string]any)
		if id, _ := app["appId"].(string); id != "" && id != backdropReceiverAppID {
			return true
		}
	}
	return false
}

func (c *castConn) heartbeatLoop() {
	logDebug("heartbeat: loop started (PING every 5s)")
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-c.done:
			logDebug("heartbeat: loop stopped (connection closed)")
			return
		case <-ticker.C:
			if !c.liveness(time.Now()) {
				return
			}
			logTrace("heartbeat: sending PING")
			if err := c.sendJSON(nsHeartbeat, platformReceiverID, map[string]any{"type": "PING"}); err != nil {
				logError("heartbeat: PING failed, stopping loop: %v", err)
				c.Close()
				return
			}
		}
	}
}

func (c *castConn) Close() {
	c.closeOnce.Do(func() {
		logInfo("close: shutting down connection to %s (sent %d frames, received %d)", c.addr, c.sent.Load(), c.recv.Load())
		close(c.done)
		if err := c.conn.Close(); err != nil {
			logDebug("close: socket close returned: %v", err)
		}
	})
}

// launchAndLoad launches the Default Media Receiver, opens a connection to the
// resulting session, and issues LOAD for the given media URL.
func (c *castConn) launchAndLoad(load map[string]any) error {
	transportID, err := c.launchReceiver()
	if err != nil {
		return err
	}
	c.sessMu.Lock()
	c.transportID = transportID
	media, _ := load["media"].(map[string]any)
	c.expectedURL, _ = media["contentId"].(string)
	c.mediaSessionID = 0
	c.ownsMedia = false
	c.loading = true
	c.loadStarted = time.Now()
	c.playerState = "LOADING"
	c.idleReason = ""
	c.takenOver = false
	c.playbackRate = 1
	c.supportedCommands = nil
	c.sessionEnded = false
	c.sessMu.Unlock()
	if err = c.sendJSON(nsConnection, transportID, map[string]any{"type": "CONNECT"}); err != nil {
		return err
	}
	_, err = c.requestTimeout(nsMedia, transportID, load, 30*time.Second)
	return err
}

// launchReceiver LAUNCHes the default receiver and returns its transportId,
// polling GET_STATUS briefly if the app isn't listed in the launch reply yet.
func (c *castConn) launchReceiver() (string, error) {
	st, err := c.requestTimeout(nsReceiver, platformReceiverID, map[string]any{"type": "GET_STATUS"}, 4*time.Second)
	if err == nil {
		if tid := c.receiverTransport(st); tid != "" {
			return tid, nil
		}
	}
	st, err = c.requestTimeout(nsReceiver, platformReceiverID, map[string]any{"type": "LAUNCH", "appId": defaultReceiverAppID}, 12*time.Second)
	if err != nil {
		return "", err
	}
	if tid := c.receiverTransport(st); tid != "" {
		return tid, nil
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		st, err = c.requestTimeout(nsReceiver, platformReceiverID, map[string]any{"type": "GET_STATUS"}, 4*time.Second)
		if err != nil {
			return "", err
		}
		if tid := c.receiverTransport(st); tid != "" {
			return tid, nil
		}
		time.Sleep(300 * time.Millisecond)
	}
	return "", errors.New("TV did not start its media receiver")
}
func (c *castConn) receiverTransport(reply map[string]any) string {
	status, _ := reply["status"].(map[string]any)
	apps, _ := status["applications"].([]any)
	for _, raw := range apps {
		app, _ := raw.(map[string]any)
		if app["appId"] == defaultReceiverAppID {
			tid, _ := app["transportId"].(string)
			sid, _ := app["sessionId"].(string)
			c.sessMu.Lock()
			c.receiverSession = sid
			c.sessMu.Unlock()
			return tid
		}
	}
	return ""
}

func transportIDFrom(reply map[string]any) string {
	status, ok := reply["status"].(map[string]any)
	if !ok {
		logTrace("transportIDFrom: reply has no status object")
		return ""
	}
	apps, ok := status["applications"].([]any)
	if !ok {
		logTrace("transportIDFrom: status has no applications array")
		return ""
	}
	for _, a := range apps {
		app, ok := a.(map[string]any)
		if !ok {
			continue
		}
		if id, _ := app["transportId"].(string); id != "" {
			logTrace("transportIDFrom: appId=%v -> transportId=%s", app["appId"], id)
			return id
		}
	}
	logTrace("transportIDFrom: no application carried a transportId")
	return ""
}

// mediaCommand issues a simple media command (PLAY/PAUSE/STOP/SEEK) against the
// current media session.
func (c *castConn) mediaCommand(cmd string, extra map[string]any) error {
	if err := c.ownershipError(); err != nil {
		return err
	}
	bit := map[string]uint64{"PAUSE": 1, "SEEK": 2, "EDIT_TRACKS_INFO": 1 << 12}[cmd]
	if bit != 0 && !c.supports(bit) {
		return fmt.Errorf("the TV does not support %s for this media", cmd)
	}
	c.sessMu.Lock()
	dest, session := c.transportID, c.mediaSessionID
	c.sessMu.Unlock()
	if dest == "" || session == 0 {
		logError("mediaCommand %s: no active media session (transportId=%q mediaSessionId=%d)", cmd, dest, session)
		return errors.New("no active media session")
	}
	payload := map[string]any{"type": cmd, "mediaSessionId": session}
	for k, v := range extra {
		payload[k] = v
	}
	logInfo("mediaCommand: %s on session %d -> %s (extra=%v)", cmd, session, dest, extra)
	reply, err := c.request(nsMedia, dest, payload)
	if err != nil {
		logError("mediaCommand %s: failed: %v", cmd, err)
		return err
	}
	logDebug("mediaCommand %s: reply type=%v", cmd, reply["type"])
	return nil
}

func (c *castConn) play() error  { return c.mediaCommand("PLAY", nil) }
func (c *castConn) pause() error { return c.mediaCommand("PAUSE", nil) }
func (c *castConn) stop() error {
	if err := c.queryStatus(); err != nil {
		return err
	}
	return c.mediaCommand("STOP", nil)
}
func (c *castConn) seek(sec float64) error {
	return c.mediaCommand("SEEK", map[string]any{"currentTime": sec})
}

// shortNS trims the long urn:x-cast:com.google.cast. prefix for readable logs.
func shortNS(ns string) string {
	const prefix = "urn:x-cast:com.google.cast."
	if len(ns) > len(prefix) && ns[:len(prefix)] == prefix {
		return ns[len(prefix):]
	}
	return ns
}

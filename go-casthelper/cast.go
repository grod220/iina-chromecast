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

	defaultReceiverAppID = "CC1AD845" // Default Media Receiver
	senderID             = "sender-0"
	platformReceiverID   = "receiver-0"
)

// castConn is a live connection to one Chromecast.
type castConn struct {
	conn io.ReadWriteCloser
	addr string // remote address, for log context

	writeMu sync.Mutex // serializes writes (heartbeat + control both write)
	reqMu   sync.Mutex
	reqID   int

	waitMu  sync.Mutex
	waiters map[int]chan map[string]any

	sessMu         sync.Mutex
	transportID    string
	mediaSessionID int
	playerState    string // last playerState seen in a MEDIA_STATUS
	idleReason     string // last idleReason seen in a MEDIA_STATUS

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
		conn:    tlsConn,
		addr:    addr,
		waiters: make(map[int]chan map[string]any),
		done:    make(chan struct{}),
	}
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
func (c *castConn) send(m castMessage) error {
	payload := encodeCastMessage(m)
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(payload)))

	n := c.sent.Add(1)
	logDebug("send #%d ns=%s dest=%s len=%d payload=%s", n, shortNS(m.namespace), m.destID, len(payload), truncate(m.payloadUTF8, 600))
	if traceEnabled() {
		logTrace("send #%d wire: %s", n, hexdump(payload, 256))
	}

	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if _, err := c.conn.Write(hdr[:]); err != nil {
		logError("send #%d: writing length prefix failed: %v", n, err)
		return err
	}
	if _, err := c.conn.Write(payload); err != nil {
		logError("send #%d: writing payload failed: %v", n, err)
		return err
	}
	return nil
}

func (c *castConn) sendJSON(ns, dest string, payload map[string]any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		logError("sendJSON: cannot marshal payload for ns=%s dest=%s: %v", shortNS(ns), dest, err)
		return err
	}
	return c.send(castMessage{
		sourceID:    senderID,
		destID:      dest,
		namespace:   ns,
		payloadUTF8: string(b),
	})
}

// request sends a JSON message tagged with a requestId and waits for the reply
// carrying the same requestId.
func (c *castConn) request(ns, dest string, payload map[string]any) (map[string]any, error) {
	id := c.nextReqID()
	payload["requestId"] = id
	kind, _ := payload["type"].(string)

	ch := make(chan map[string]any, 1)
	c.waitMu.Lock()
	c.waiters[id] = ch
	c.waitMu.Unlock()
	defer func() {
		c.waitMu.Lock()
		delete(c.waiters, id)
		c.waitMu.Unlock()
	}()

	logDebug("request: %s requestId=%d -> %s (ns=%s)", kind, id, dest, shortNS(ns))
	start := time.Now()
	if err := c.sendJSON(ns, dest, payload); err != nil {
		logError("request: %s requestId=%d could not be sent: %v", kind, id, err)
		return nil, err
	}
	select {
	case reply := <-ch:
		rt, _ := reply["type"].(string)
		logDebug("request: %s requestId=%d answered with %s in %s", kind, id, rt, time.Since(start).Round(time.Millisecond))
		return reply, nil
	case <-time.After(10 * time.Second):
		logError("request: %s requestId=%d timed out after 10s with no reply from %s "+
			"(device may have closed the virtual connection, or the namespace/destination is wrong)", kind, id, dest)
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
		if _, err := io.ReadFull(c.conn, buf); err != nil {
			logError("readLoop: reading %d-byte frame body failed: %v", n, err)
			return
		}
		seq := c.recv.Add(1)
		if traceEnabled() {
			logTrace("recv #%d wire (%d bytes): %s", seq, n, hexdump(buf, 256))
		}
		c.handle(seq, decodeCastMessage(buf))
	}
}

func (c *castConn) handle(seq uint64, m castMessage) {
	logDebug("recv #%d ns=%s src=%s dest=%s payload=%s", seq, shortNS(m.namespace), m.sourceID, m.destID, truncate(m.payloadUTF8, 600))

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

	// Answer heartbeat pings so the device keeps us alive.
	if m.namespace == nsHeartbeat {
		if msgType == "PING" {
			logTrace("recv #%d: PING from %s, replying PONG", seq, m.sourceID)
			if err := c.sendJSON(nsHeartbeat, platformReceiverID, map[string]any{"type": "PONG"}); err != nil {
				logError("recv #%d: sending PONG failed: %v", seq, err)
			}
		} else {
			logTrace("recv #%d: heartbeat %s", seq, msgType)
		}
		return
	}

	// The device closes a virtual connection when it drops us; without this the
	// symptom is a silent stall.
	if m.namespace == nsConnection && msgType == "CLOSE" {
		logWarn("recv #%d: device closed the virtual connection from %s (reason: %v)", seq, m.sourceID, payload["reason"])
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
		logWarn("recv #%d: MEDIA_STATUS with empty status array — no media session on the device", seq)
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
		if id, ok := s0["mediaSessionId"].(float64); ok {
			c.mediaSessionID = int(id)
		}
		c.playerState, c.idleReason = state, idle
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
	apps, _ := status["applications"].([]any)
	if len(apps) == 0 {
		logWarn("recv #%d: RECEIVER_STATUS lists no running applications — the receiver app is not (or no longer) running", seq)
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
			logTrace("heartbeat: sending PING")
			if err := c.sendJSON(nsHeartbeat, platformReceiverID, map[string]any{"type": "PING"}); err != nil {
				logError("heartbeat: PING failed, stopping loop: %v", err)
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
func (c *castConn) launchAndLoad(mediaURL, contentType, title string) error {
	logInfo("launchAndLoad: url=%s contentType=%s title=%q", mediaURL, contentType, title)

	transportID, err := c.launchReceiver()
	if err != nil {
		logError("launchAndLoad: launching receiver failed: %v", err)
		return err
	}
	logInfo("launchAndLoad: receiver ready, transportId=%s", transportID)
	c.sessMu.Lock()
	c.transportID = transportID
	c.sessMu.Unlock()

	// Virtual connection to the media session, then LOAD.
	logDebug("launchAndLoad: opening virtual connection to %s", transportID)
	if err := c.sendJSON(nsConnection, transportID, map[string]any{"type": "CONNECT"}); err != nil {
		logError("launchAndLoad: CONNECT to %s failed: %v", transportID, err)
		return err
	}

	load := map[string]any{
		"type":        "LOAD",
		"autoplay":    true,
		"currentTime": 0,
		"media": map[string]any{
			"contentId":   mediaURL,
			"streamType":  "BUFFERED",
			"contentType": contentType,
			"metadata": map[string]any{
				"type":  0, // GENERIC
				"title": title,
			},
		},
	}
	logInfo("launchAndLoad: sending LOAD to %s", transportID)
	reply, err := c.request(nsMedia, transportID, load)
	if err != nil {
		logError("launchAndLoad: LOAD got no usable reply: %v", err)
		return err
	}
	if t, _ := reply["type"].(string); t == "LOAD_FAILED" || t == "LOAD_CANCELLED" {
		logError("launchAndLoad: device rejected the media: type=%s detail=%v", t, reply)
		return fmt.Errorf("device rejected media: %s", t)
	}

	if arr, ok := reply["status"].([]any); ok && len(arr) > 0 {
		if s0, ok := arr[0].(map[string]any); ok {
			if id, ok := s0["mediaSessionId"].(float64); ok {
				c.sessMu.Lock()
				c.mediaSessionID = int(id)
				c.sessMu.Unlock()
				logInfo("launchAndLoad: media session established, mediaSessionId=%d", int(id))
			} else {
				logWarn("launchAndLoad: LOAD reply status has no mediaSessionId — play/pause/seek will not work")
			}
		}
	} else {
		logWarn("launchAndLoad: LOAD reply had no status array (type=%v); the media session id is unknown", reply["type"])
	}
	return nil
}

// launchReceiver LAUNCHes the default receiver and returns its transportId,
// polling GET_STATUS briefly if the app isn't listed in the launch reply yet.
func (c *castConn) launchReceiver() (string, error) {
	logInfo("launchReceiver: LAUNCH appId=%s", defaultReceiverAppID)
	reply, err := c.request(nsReceiver, platformReceiverID, map[string]any{
		"type":  "LAUNCH",
		"appId": defaultReceiverAppID,
	})
	if err != nil {
		logError("launchReceiver: LAUNCH failed: %v", err)
		return "", err
	}
	if t, _ := reply["type"].(string); t == "LAUNCH_ERROR" {
		logError("launchReceiver: device returned LAUNCH_ERROR: %v", reply)
		return "", fmt.Errorf("device refused to launch %s: %v", defaultReceiverAppID, reply["reason"])
	}
	if tid := transportIDFrom(reply); tid != "" {
		logDebug("launchReceiver: transportId %s came straight from the LAUNCH reply", tid)
		return tid, nil
	}

	logDebug("launchReceiver: LAUNCH reply had no transportId, polling GET_STATUS")
	for i := 0; i < 5; i++ {
		time.Sleep(500 * time.Millisecond)
		st, err := c.request(nsReceiver, platformReceiverID, map[string]any{"type": "GET_STATUS"})
		if err != nil {
			logError("launchReceiver: GET_STATUS poll %d/5 failed: %v", i+1, err)
			return "", err
		}
		if tid := transportIDFrom(st); tid != "" {
			logDebug("launchReceiver: transportId %s appeared on poll %d/5", tid, i+1)
			return tid, nil
		}
		logDebug("launchReceiver: poll %d/5 — receiver app still not listed", i+1)
	}
	logError("launchReceiver: gave up after 5 polls; the receiver app never appeared in the device status")
	return "", errors.New("receiver launched but no transportId appeared")
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
func (c *castConn) stop() error  { return c.mediaCommand("STOP", nil) }
func (c *castConn) seek(sec float64) error {
	return c.mediaCommand("SEEK", map[string]any{"currentTime": sec})
}

// stopReceiver tears down the whole session (used on quit).
func (c *castConn) stopReceiver() {
	logInfo("stopReceiver: sending STOP to %s", platformReceiverID)
	if _, err := c.request(nsReceiver, platformReceiverID, map[string]any{"type": "STOP"}); err != nil {
		logWarn("stopReceiver: STOP was not acknowledged: %v", err)
		return
	}
	logInfo("stopReceiver: receiver stopped")
}

// shortNS trims the long urn:x-cast:com.google.cast. prefix for readable logs.
func shortNS(ns string) string {
	const prefix = "urn:x-cast:com.google.cast."
	if len(ns) > len(prefix) && ns[:len(prefix)] == prefix {
		return ns[len(prefix):]
	}
	return ns
}

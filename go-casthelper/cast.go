package main

import (
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"sync"
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

	writeMu sync.Mutex // serializes writes (heartbeat + control both write)
	reqMu   sync.Mutex
	reqID   int

	waitMu  sync.Mutex
	waiters map[int]chan map[string]any

	sessMu         sync.Mutex
	transportID    string
	mediaSessionID int

	closeOnce sync.Once
	done      chan struct{}
}

// dialCast opens the TLS socket, starts the read loop + heartbeat, and opens the
// virtual connection to the platform receiver (receiver-0).
func dialCast(addr string) (*castConn, error) {
	tlsConn, err := tls.DialWithDialer(&net.Dialer{Timeout: 8 * time.Second}, "tcp", addr, &tls.Config{
		InsecureSkipVerify: true, // devices present a self-signed cert
		MinVersion:         tls.VersionTLS12,
	})
	if err != nil {
		return nil, fmt.Errorf("tls dial %s: %w", addr, err)
	}
	c := &castConn{
		conn:    tlsConn,
		waiters: make(map[int]chan map[string]any),
		done:    make(chan struct{}),
	}
	go c.readLoop()

	// Open the tp.connection virtual channel to the platform receiver.
	if err := c.sendJSON(nsConnection, platformReceiverID, map[string]any{"type": "CONNECT"}); err != nil {
		c.Close()
		return nil, err
	}
	go c.heartbeatLoop()
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

	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if _, err := c.conn.Write(hdr[:]); err != nil {
		return err
	}
	_, err := c.conn.Write(payload)
	return err
}

func (c *castConn) sendJSON(ns, dest string, payload map[string]any) error {
	b, err := json.Marshal(payload)
	if err != nil {
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

	ch := make(chan map[string]any, 1)
	c.waitMu.Lock()
	c.waiters[id] = ch
	c.waitMu.Unlock()
	defer func() {
		c.waitMu.Lock()
		delete(c.waiters, id)
		c.waitMu.Unlock()
	}()

	if err := c.sendJSON(ns, dest, payload); err != nil {
		return nil, err
	}
	select {
	case reply := <-ch:
		return reply, nil
	case <-time.After(10 * time.Second):
		return nil, errors.New("timed out waiting for device reply")
	case <-c.done:
		return nil, errors.New("connection closed")
	}
}

func (c *castConn) readLoop() {
	defer c.Close()
	hdr := make([]byte, 4)
	for {
		if _, err := io.ReadFull(c.conn, hdr); err != nil {
			return
		}
		n := binary.BigEndian.Uint32(hdr)
		buf := make([]byte, n)
		if _, err := io.ReadFull(c.conn, buf); err != nil {
			return
		}
		c.handle(decodeCastMessage(buf))
	}
}

func (c *castConn) handle(m castMessage) {
	if m.payloadUTF8 == "" {
		return
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(m.payloadUTF8), &payload); err != nil {
		return
	}

	// Answer heartbeat pings so the device keeps us alive.
	fmt.Println("Answering pong...")
	if m.namespace == nsHeartbeat {
		if t, _ := payload["type"].(string); t == "PING" {
			_ = c.sendJSON(nsHeartbeat, platformReceiverID, map[string]any{"type": "PONG"})
		}
		return
	}

	// Keep the media session id fresh from any MEDIA_STATUS broadcast.
	if t, _ := payload["type"].(string); t == "MEDIA_STATUS" {
		if arr, ok := payload["status"].([]any); ok && len(arr) > 0 {
			if s0, ok := arr[0].(map[string]any); ok {
				if id, ok := s0["mediaSessionId"].(float64); ok {
					c.sessMu.Lock()
					c.mediaSessionID = int(id)
					c.sessMu.Unlock()
				}
			}
		}
	}

	// Route replies that carry a requestId to whoever is waiting.
	if idf, ok := payload["requestId"].(float64); ok {
		id := int(idf)
		c.waitMu.Lock()
		ch := c.waiters[id]
		c.waitMu.Unlock()
		if ch != nil {
			select {
			case ch <- payload:
			default:
			}
		}
	}
}

func (c *castConn) heartbeatLoop() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		fmt.Println("Heartbeat..")
		select {
		case <-c.done:
			return
		case <-ticker.C:
			if err := c.sendJSON(nsHeartbeat, platformReceiverID, map[string]any{"type": "PING"}); err != nil {
				return
			}
		}
	}
}

func (c *castConn) Close() {
	c.closeOnce.Do(func() {
		close(c.done)
		_ = c.conn.Close()
	})
}

// launchAndLoad launches the Default Media Receiver, opens a connection to the
// resulting session, and issues LOAD for the given media URL.
func (c *castConn) launchAndLoad(mediaURL, contentType, title string) error {
	transportID, err := c.launchReceiver()
	if err != nil {
		return err
	}
	c.sessMu.Lock()
	c.transportID = transportID
	c.sessMu.Unlock()

	// Virtual connection to the media session, then LOAD.
	if err := c.sendJSON(nsConnection, transportID, map[string]any{"type": "CONNECT"}); err != nil {
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
	reply, err := c.request(nsMedia, transportID, load)
	if err != nil {
		return err
	}
	if t, _ := reply["type"].(string); t == "LOAD_FAILED" || t == "LOAD_CANCELLED" {
		return fmt.Errorf("device rejected media: %s", t)
	}
	if arr, ok := reply["status"].([]any); ok && len(arr) > 0 {
		if s0, ok := arr[0].(map[string]any); ok {
			if id, ok := s0["mediaSessionId"].(float64); ok {
				c.sessMu.Lock()
				c.mediaSessionID = int(id)
				c.sessMu.Unlock()
			}
		}
	}
	return nil
}

// launchReceiver LAUNCHes the default receiver and returns its transportId,
// polling GET_STATUS briefly if the app isn't listed in the launch reply yet.
func (c *castConn) launchReceiver() (string, error) {
	reply, err := c.request(nsReceiver, platformReceiverID, map[string]any{
		"type":  "LAUNCH",
		"appId": defaultReceiverAppID,
	})
	if err != nil {
		return "", err
	}
	if tid := transportIDFrom(reply); tid != "" {
		return tid, nil
	}
	for i := 0; i < 5; i++ {
		time.Sleep(500 * time.Millisecond)
		st, err := c.request(nsReceiver, platformReceiverID, map[string]any{"type": "GET_STATUS"})
		if err != nil {
			return "", err
		}
		if tid := transportIDFrom(st); tid != "" {
			return tid, nil
		}
	}
	return "", errors.New("receiver launched but no transportId appeared")
}

func transportIDFrom(reply map[string]any) string {
	status, ok := reply["status"].(map[string]any)
	if !ok {
		return ""
	}
	apps, ok := status["applications"].([]any)
	if !ok {
		return ""
	}
	for _, a := range apps {
		app, ok := a.(map[string]any)
		if !ok {
			continue
		}
		if id, _ := app["transportId"].(string); id != "" {
			return id
		}
	}
	return ""
}

// mediaCommand issues a simple media command (PLAY/PAUSE/STOP/SEEK) against the
// current media session.
func (c *castConn) mediaCommand(cmd string, extra map[string]any) error {
	c.sessMu.Lock()
	dest, session := c.transportID, c.mediaSessionID
	c.sessMu.Unlock()
	if dest == "" || session == 0 {
		return errors.New("no active media session")
	}
	payload := map[string]any{"type": cmd, "mediaSessionId": session}
	for k, v := range extra {
		payload[k] = v
	}
	_, err := c.request(nsMedia, dest, payload)
	return err
}

func (c *castConn) play() error  { return c.mediaCommand("PLAY", nil) }
func (c *castConn) pause() error { return c.mediaCommand("PAUSE", nil) }
func (c *castConn) stop() error  { return c.mediaCommand("STOP", nil) }
func (c *castConn) seek(sec float64) error {
	return c.mediaCommand("SEEK", map[string]any{"currentTime": sec})
}

// stopReceiver tears down the whole session (used on quit).
func (c *castConn) stopReceiver() {
	_, _ = c.request(nsReceiver, platformReceiverID, map[string]any{"type": "STOP"})
	log.Printf("receiver stopped")
}

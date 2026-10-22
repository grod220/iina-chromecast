package main

import (
	"errors"
	"fmt"
	"io"
	"net"
	"sync/atomic"
	"time"
)

var errSessionEnded = errors.New("the previous TV playback has ended")

type receiverRejection struct{ Command, Type string }

func (e *receiverRejection) Error() string { return "TV rejected " + e.Command + ": " + e.Type }

var errTakenOver = errors.New("another app is using the TV; choose Cast Video to start a new cast")

const writeTimeout = 3 * time.Second
const receiveTimeout = 15 * time.Second

type sessionIdentity struct {
	Transport, Receiver string
	Media               int
}

var senderSequence atomic.Uint64

func newSenderID() string {
	return fmt.Sprintf("sender-iina-%x-%x", time.Now().UnixNano(), senderSequence.Add(1))
}

func (c *castConn) sender() string {
	if c.senderID != "" {
		return c.senderID
	}
	return senderID // legacy fixtures and explicitly constructed connections
}

func (c *castConn) identity() sessionIdentity {
	c.sessMu.Lock()
	defer c.sessMu.Unlock()
	return sessionIdentity{c.transportID, c.receiverSession, c.mediaSessionID}
}
func (c *castConn) ownershipError() error {
	c.sessMu.Lock()
	defer c.sessMu.Unlock()
	if c.takenOver {
		return errTakenOver
	}
	if !c.ownsMedia || c.sessionEnded {
		return errors.New("the casting session is no longer active")
	}
	return nil
}
func (c *castConn) supports(bit uint64) bool {
	c.sessMu.Lock()
	defer c.sessMu.Unlock()
	return c.supportedCommands == nil || *c.supportedCommands&bit != 0
}
func (c *castConn) routeBase(port string) (string, error) {
	socket, ok := c.conn.(interface{ LocalAddr() net.Addr })
	if !ok {
		return "", errors.New("TV connection does not expose a local address")
	}
	host, _, err := net.SplitHostPort(socket.LocalAddr().String())
	if err != nil {
		return "", fmt.Errorf("cannot determine address used to reach TV: %w", err)
	}
	if ip := net.ParseIP(host); ip == nil || ip.IsUnspecified() {
		return "", errors.New("TV connection has no usable local address")
	}
	return "http://" + net.JoinHostPort(host, port), nil
}
func (c *castConn) writeFrame(payload []byte, deadline time.Time) error {
	c.writeOnce.Do(func() { c.writeGate = make(chan struct{}, 1); c.writeGate <- struct{}{} })
	if deadline.IsZero() || time.Until(deadline) > writeTimeout {
		deadline = time.Now().Add(writeTimeout)
	}
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case <-c.done:
		return errors.New("connection closed")
	case <-timer.C:
		return errors.New("timed out waiting to send Chromecast command")
	case <-c.writeGate:
	}
	defer func() { c.writeGate <- struct{}{} }()
	if time.Now().After(deadline) {
		return errors.New("Chromecast command deadline expired")
	}
	socket, ok := c.conn.(interface{ SetWriteDeadline(time.Time) error })
	if !ok {
		return errors.New("TV connection does not support write deadlines")
	}
	if err := socket.SetWriteDeadline(deadline); err != nil {
		c.Close()
		return err
	}
	defer socket.SetWriteDeadline(time.Time{})
	for len(payload) > 0 {
		n, err := c.conn.Write(payload)
		if n > 0 {
			payload = payload[n:]
		}
		if err != nil {
			c.Close()
			return err
		}
		if n == 0 {
			c.Close()
			return io.ErrShortWrite
		}
	}
	return nil
}
func (c *castConn) liveness(now time.Time) bool {
	last := c.lastInbound.Load()
	if last != 0 && now.Sub(time.Unix(0, last)) >= receiveTimeout {
		c.Close()
		return false
	}
	return c.connected()
}
func (c *castConn) readDeadline(duration time.Duration) {
	if socket, ok := c.conn.(interface{ SetReadDeadline(time.Time) error }); ok {
		_ = socket.SetReadDeadline(time.Now().Add(duration))
	}
}

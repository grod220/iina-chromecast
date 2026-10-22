package main

import (
	"errors"
	"math"
	"time"
)

type remoteState struct {
	CurrentTime       float64        `json:"currentTime"`
	Duration          float64        `json:"duration"`
	PlayerState       string         `json:"playerState"`
	IdleReason        string         `json:"idleReason"`
	ActiveTrackIDs    []int          `json:"activeTrackIds"`
	TextTrackStyle    map[string]any `json:"textTrackStyle"`
	Volume            float64        `json:"volume"`
	Muted             bool           `json:"muted"`
	Ended             bool           `json:"ended"`
	TakenOver         bool           `json:"takenOver"`
	SupportedCommands *uint64        `json:"supportedMediaCommands,omitempty"`
}

func (c *castConn) snapshot() remoteState {
	c.sessMu.Lock()
	defer c.sessMu.Unlock()
	pos := c.currentTime
	if c.playerState == "IDLE" && c.idleReason == "FINISHED" && c.duration > 0 {
		pos = c.duration
	}
	if c.playerState == "PLAYING" && !c.takenOver && !c.positionUpdated.IsZero() {
		pos += time.Since(c.positionUpdated).Seconds() * c.playbackRate
	}
	if c.duration > 0 {
		pos = math.Min(pos, c.duration)
	}
	return remoteState{CurrentTime: math.Max(0, pos), Duration: c.duration, PlayerState: c.playerState, IdleReason: c.idleReason, ActiveTrackIDs: append([]int{}, c.activeTrackIDs...), TextTrackStyle: c.textTrackStyle, Volume: c.volume, Muted: c.muted, Ended: c.sessionEnded, TakenOver: c.takenOver, SupportedCommands: c.supportedCommands}
}
func (c *castConn) connected() bool {
	select {
	case <-c.done:
		return false
	default:
		return true
	}
}
func (c *castConn) queryStatus() error {
	c.sessMu.Lock()
	tid := c.transportID
	c.sessMu.Unlock()
	if tid == "" {
		return errors.New("no active media receiver")
	}
	if _, err := c.requestTimeout(nsMedia, tid, map[string]any{"type": "GET_STATUS"}, 3*time.Second); err != nil {
		return err
	}
	c.sessMu.Lock()
	if c.loading && !c.loadStarted.IsZero() && time.Since(c.loadStarted) > 30*time.Second {
		c.loading = false
		c.playerState = "IDLE"
		c.idleReason = "ERROR"
	}
	takenOver := c.takenOver
	c.sessMu.Unlock()
	if takenOver {
		return errTakenOver
	}
	return nil
}
func (c *castConn) editTracks(ids []int, style map[string]any) error {
	fields := map[string]any{}
	if ids != nil {
		fields["activeTrackIds"] = ids
	}
	if style != nil {
		fields["textTrackStyle"] = style
	}
	return c.mediaCommand("EDIT_TRACKS_INFO", fields)
}
func (c *castConn) setVolume(level float64, muted bool) error {
	if math.IsNaN(level) || math.IsInf(level, 0) || level < 0 || level > 1 {
		return errors.New("volume must be between 0 and 1")
	}
	// Device-wide volume must not act on a replacement cast.
	if err := c.queryStatus(); err != nil {
		return err
	}
	if err := c.ownershipError(); err != nil {
		return err
	}
	_, err := c.request(nsReceiver, platformReceiverID, map[string]any{"type": "SET_VOLUME", "volume": map[string]any{"level": level, "muted": muted}})
	return err
}
func (c *castConn) attach(url string) (bool, error) { return c.attachOwned(url, sessionIdentity{}) }
func (c *castConn) attachOwned(url string, expected sessionIdentity) (bool, error) {
	reply, err := c.requestTimeout(nsReceiver, platformReceiverID, map[string]any{"type": "GET_STATUS"}, 3*time.Second)
	if err != nil {
		return false, err
	}
	status, _ := reply["status"].(map[string]any)
	apps, _ := status["applications"].([]any)
	tid := c.receiverTransport(reply)
	if tid == "" {
		if hasPlaybackApplication(apps) {
			return false, errTakenOver
		}
		return false, nil
	}
	c.sessMu.Lock()
	receiver := c.receiverSession
	c.transportID = tid
	c.sessMu.Unlock()
	if err = c.sendJSON(nsConnection, tid, map[string]any{"type": "CONNECT"}); err != nil {
		return false, err
	}
	reply, err = c.requestTimeout(nsMedia, tid, map[string]any{"type": "GET_STATUS"}, 3*time.Second)
	if err != nil {
		return false, err
	}
	arr, _ := reply["status"].([]any)
	if len(arr) == 0 {
		return false, nil
	}
	state, _ := arr[0].(map[string]any)
	media, _ := state["media"].(map[string]any)
	player, _ := state["playerState"].(string)
	if extended, ok := state["extendedStatus"].(map[string]any); ok && extended["playerState"] == "LOADING" {
		player = "LOADING"
	}
	id, _ := state["mediaSessionId"].(float64)
	contentID, _ := media["contentId"].(string)
	if (contentID != "" && contentID != url) || (expected.Media != 0 && id != 0 && expected.Media != int(id)) || (expected.Receiver != "" && expected.Receiver != receiver) || (expected.Transport != "" && expected.Transport != tid) {
		return false, errTakenOver
	}
	if player == "IDLE" {
		reason, _ := state["idleReason"].(string)
		if reason == "INTERRUPTED" {
			return false, errTakenOver
		}
		if reason == "FINISHED" || reason == "CANCELLED" {
			return false, errSessionEnded
		}
		return false, nil
	}
	if contentID != url {
		return false, errTakenOver
	}
	c.sessMu.Lock()
	c.expectedURL = url
	c.mediaSessionID = int(id)
	c.ownsMedia = true
	c.takenOver = false
	c.sessionEnded = false
	c.loading = false
	c.playbackRate = 1
	c.sessMu.Unlock()
	c.logMediaStatus(0, reply)
	return true, nil
}

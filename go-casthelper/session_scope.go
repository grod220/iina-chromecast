package main

import (
	"errors"
	"net/http"
	"time"
)

type castScope struct {
	OwnerID string `json:"ownerId"`
	CastID  string `json:"castId"`
}

var errCastReplaced = errors.New("this cast has ended or was replaced; open the current Chromecast remote")

func scopeFromRequest(r *http.Request) castScope {
	return castScope{r.Header.Get("X-IINA-Owner"), r.Header.Get("X-IINA-Cast")}
}
func (s castScope) specified() bool { return s.OwnerID != "" || s.CastID != "" }
func (s castScope) matches(req castRequest) bool {
	return !s.specified() || (s.OwnerID == req.OwnerID && s.CastID == req.CastID)
}
func (a *app) retireLocked(s castScope) {
	if s.OwnerID == "" || s.CastID == "" {
		return
	}
	if a.retired == nil {
		a.retired = map[castScope]time.Time{}
	}
	now := time.Now()
	var oldest castScope
	var oldestTime time.Time
	for key, stamp := range a.retired {
		if now.Sub(stamp) > 24*time.Hour {
			delete(a.retired, key)
			continue
		}
		if oldestTime.IsZero() || stamp.Before(oldestTime) {
			oldest, oldestTime = key, stamp
		}
	}
	if len(a.retired) >= 1024 {
		delete(a.retired, oldest)
	}
	a.retired[s] = now
}
func (a *app) retiredLocked(req castRequest) bool {
	stamp, ok := a.retired[castScope{req.OwnerID, req.CastID}]
	return ok && time.Since(stamp) <= 24*time.Hour
}

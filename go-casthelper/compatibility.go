package main

import (
	"errors"
	"io"
	"net/http"
	"sync/atomic"
)

type conversionFallback struct{ Video, Audio bool }
type compatibilityRecovery struct {
	Automatic       bool
	Fallback        conversionFallback
	Tried           uint8
	DeliveryRetries int
}

func planBit(plan mediaPlan) uint8 {
	index := uint8(0)
	if !plan.CopyAudio {
		index |= 1
	}
	if !plan.CopyVideo {
		index |= 2
	}
	return 1 << index
}
func nextCompatibility(req castRequest, info mediaInfo, current mediaPlan) (compatibilityRecovery, bool) {
	recovery := req.recovery
	recovery.Tried |= planBit(current)
	for _, candidate := range []conversionFallback{{Audio: true}, {Video: true}, {Audio: true, Video: true}} {
		plan, err := planMediaWithFallback(info, req.DeviceModel, req.AudioIndex, candidate)
		if err == nil && recovery.Tried&planBit(plan) == 0 {
			recovery.Fallback = candidate
			return recovery, true
		}
	}
	return recovery, false
}

type mediaDelivery struct{ Requests, Bytes, Errors atomic.Uint64 }
type deliveryWriter struct {
	http.ResponseWriter
	stats *mediaDelivery
	code  int
}

func (w *deliveryWriter) WriteHeader(code int) {
	if w.code != 0 {
		return
	}
	w.code = code
	if code >= 400 {
		w.stats.Errors.Add(1)
	}
	w.ResponseWriter.WriteHeader(code)
}
func (w *deliveryWriter) Write(body []byte) (int, error) {
	if w.code == 0 {
		w.WriteHeader(200)
	}
	n, err := w.ResponseWriter.Write(body)
	if w.code >= 200 && w.code < 300 {
		w.stats.Bytes.Add(uint64(n))
	}
	return n, err
}
func (w *deliveryWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *deliveryWriter) ReadFrom(reader io.Reader) (int64, error) {
	if w.code == 0 {
		w.WriteHeader(200)
	}
	if fast, ok := w.ResponseWriter.(io.ReaderFrom); ok {
		n, err := fast.ReadFrom(reader)
		if w.code >= 200 && w.code < 300 {
			w.stats.Bytes.Add(uint64(n))
		}
		return n, err
	}
	return io.Copy(struct{ io.Writer }{w}, reader)
}

func recoveryRequest(req castRequest, info mediaInfo, plan mediaPlan, delivery *mediaDelivery, last remoteState) (castRequest, error) {
	req.StartTime = last.CurrentTime
	play := last.PlayerState == "PLAYING"
	req.Autoplay = &play
	// A failed fetch is not evidence of an incompatible codec. Retry delivery
	// once with a newly resolved route, then report it without encoding video.
	delivered := delivery != nil && delivery.Bytes.Load() > 0 && delivery.Errors.Load() == 0
	if req.recovery.DeliveryRetries == 0 {
		req.recovery.DeliveryRetries++
		return req, nil
	}
	if !delivered {
		return req, errors.New("TV could not download the video. Check its local network connection, then choose Reconnect")
	}
	if req.ForceTranscode {
		return req, errors.New("TV could not play the converted video")
	}
	next, ok := nextCompatibility(req, info, plan)
	if !ok {
		return req, errors.New("TV could not play the delivered video after trying compatible audio and video formats")
	}
	req.recovery = next
	return req, nil
}

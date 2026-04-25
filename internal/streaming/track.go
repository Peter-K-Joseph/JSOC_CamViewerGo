package streaming

import (
	"sync"
	"sync/atomic"
	"time"
)

const subscriberBufSize = 32

// TrackStats holds live performance metrics for a track.
type TrackStats struct {
	Codec       string  `json:"codec"`
	FPS         float64 `json:"fps"`
	BitrateBps  float64 `json:"bitrate_bps"`  // bits per second
	TotalFrames uint64  `json:"total_frames"`
	TotalBytes  uint64  `json:"total_bytes"`
	Keyframes   uint64  `json:"keyframes"`
	Dropped     uint64  `json:"dropped"`
	Subscribers int     `json:"subscribers"`
}

// Track is a single camera stream's fan-out hub.
// The ws_source publishes AccessUnits; RTSP sessions subscribe.
type Track struct {
	Key   string
	Codec string // "h264" or "h265" — set after first SPS/PPS received

	// Parameter sets, updated whenever WS source sees new ones.
	mu  sync.RWMutex
	SPS []byte
	PPS []byte
	VPS []byte // H.265 only

	subsMu sync.Mutex
	subs   []chan AccessUnit

	// Performance counters (lock-free).
	totalFrames atomic.Uint64
	totalBytes  atomic.Uint64
	keyframes   atomic.Uint64
	dropped     atomic.Uint64

	// Rolling window for FPS / bitrate calculation.
	winMu      sync.Mutex
	winFrames  int
	winBytes   int
	winStart   time.Time
	lastFPS    float64
	lastBitrate float64
}

func NewTrack(key string) *Track {
	return &Track{
		Key:      key,
		winStart: time.Now(),
	}
}

// UpdateParams stores the latest codec parameter sets thread-safely.
func (t *Track) UpdateParams(codec string, sps, pps, vps []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.Codec = codec
	if sps != nil {
		t.SPS = clone(sps)
	}
	if pps != nil {
		t.PPS = clone(pps)
	}
	if vps != nil {
		t.VPS = clone(vps)
	}
}

// Params returns codec and parameter sets.
func (t *Track) Params() (codec string, sps, pps, vps []byte) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.Codec, clone(t.SPS), clone(t.PPS), clone(t.VPS)
}

// Subscribe returns a channel that receives AccessUnits.
// The caller must call Unsubscribe when done.
func (t *Track) Subscribe() chan AccessUnit {
	ch := make(chan AccessUnit, subscriberBufSize)
	t.subsMu.Lock()
	t.subs = append(t.subs, ch)
	t.subsMu.Unlock()
	return ch
}

// Unsubscribe removes the subscriber channel.
func (t *Track) Unsubscribe(ch chan AccessUnit) {
	t.subsMu.Lock()
	defer t.subsMu.Unlock()
	for i, s := range t.subs {
		if s == ch {
			t.subs = append(t.subs[:i], t.subs[i+1:]...)
			close(ch)
			return
		}
	}
}

// Publish fans an AccessUnit out to all subscribers.
// Slow subscribers drop frames (non-blocking send).
func (t *Track) Publish(au AccessUnit) {
	byteLen := uint64(len(au.Data))
	t.totalFrames.Add(1)
	t.totalBytes.Add(byteLen)
	if au.Keyframe {
		t.keyframes.Add(1)
	}

	// Update rolling window.
	t.winMu.Lock()
	t.winFrames++
	t.winBytes += len(au.Data)
	elapsed := time.Since(t.winStart).Seconds()
	if elapsed >= 2.0 { // flush window every 2 seconds
		t.lastFPS = float64(t.winFrames) / elapsed
		t.lastBitrate = float64(t.winBytes) * 8.0 / elapsed
		t.winFrames = 0
		t.winBytes = 0
		t.winStart = time.Now()
	}
	t.winMu.Unlock()

	t.subsMu.Lock()
	defer t.subsMu.Unlock()
	for _, ch := range t.subs {
		select {
		case ch <- au:
		default:
			// subscriber too slow — drop frame
			t.dropped.Add(1)
		}
	}
}

// Stats returns live performance metrics.
func (t *Track) Stats() TrackStats {
	t.winMu.Lock()
	fps := t.lastFPS
	bitrate := t.lastBitrate
	t.winMu.Unlock()

	return TrackStats{
		Codec:       t.Codec,
		FPS:         fps,
		BitrateBps:  bitrate,
		TotalFrames: t.totalFrames.Load(),
		TotalBytes:  t.totalBytes.Load(),
		Keyframes:   t.keyframes.Load(),
		Dropped:     t.dropped.Load(),
		Subscribers: t.SubscriberCount(),
	}
}

// SubscriberCount returns the number of active RTSP sessions.
func (t *Track) SubscriberCount() int {
	t.subsMu.Lock()
	defer t.subsMu.Unlock()
	return len(t.subs)
}

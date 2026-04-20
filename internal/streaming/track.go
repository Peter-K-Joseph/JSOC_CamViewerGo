package streaming

import "sync"

const subscriberBufSize = 32

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
}

func NewTrack(key string) *Track {
	return &Track{Key: key}
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
	t.subsMu.Lock()
	defer t.subsMu.Unlock()
	for _, ch := range t.subs {
		select {
		case ch <- au:
		default:
			// subscriber too slow — drop frame
		}
	}
}

// SubscriberCount returns the number of active RTSP sessions.
func (t *Track) SubscriberCount() int {
	t.subsMu.Lock()
	defer t.subsMu.Unlock()
	return len(t.subs)
}

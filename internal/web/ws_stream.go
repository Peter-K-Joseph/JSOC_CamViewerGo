package web

import (
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
	"github.com/jsoc/camviewer/internal/mux"
	"github.com/jsoc/camviewer/internal/streaming"
)

var wsUpgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 256 * 1024,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

// handleWSStream handles GET /ws/stream/{streamKey}
// Protocol:
//  1. Send JSON text: {"codec":"avc1.4D001E","mimeType":"video/mp4; codecs=\"...\""}
//  2. Send binary:   fMP4 init segment (ftyp + moov)
//  3. Send binary:   fMP4 media segments, one per AccessUnit
func (s *Server) handleWSStream(w http.ResponseWriter, r *http.Request) {
	streamKey := chi.URLParam(r, "streamKey")

	track := s.manager.Track(streamKey)
	if track == nil {
		http.Error(w, "stream not found", 404)
		return
	}

	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[ws] upgrade error: %v", err)
		return
	}
	defer conn.Close()

	// Wait for codec params (SPS/PPS) — keyframe may not have arrived yet.
	var codec string
	var sps, pps, vps []byte
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		codec, sps, pps, vps = track.Params()
		if len(sps) > 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if len(sps) == 0 {
		log.Printf("[ws] %s: no SPS after 10s, closing", streamKey)
		return
	}

	// 1. Send codec info as JSON text message.
	info := struct {
		Codec    string `json:"codec"`
		MIMEType string `json:"mimeType"`
	}{
		Codec:    mux.CodecString(codec, sps),
		MIMEType: mux.MIMEType(codec, sps),
	}
	if err := conn.WriteMessage(websocket.TextMessage, mustJSON(info)); err != nil {
		return
	}

	// 2. Send fMP4 init segment.
	initSeg := mux.InitSegment(codec, sps, pps, vps)
	if err := conn.WriteMessage(websocket.BinaryMessage, initSeg); err != nil {
		return
	}

	// 3. Subscribe to the track and stream media segments.
	ch := track.Subscribe()
	defer track.Unsubscribe(ch)

	var seq uint32
	var lastDTS uint64
	waitKeyframe := true

	for au := range ch {
		// Drop frames until first keyframe so MSE starts on a clean boundary.
		if waitKeyframe {
			if !au.Keyframe {
				continue
			}
			waitKeyframe = false
		}

		// Update init segment if codec params changed (e.g. camera renegotiated).
		if au.Keyframe {
			newCodec, newSPS, newPPS, newVPS := track.Params()
			if len(newSPS) > 0 && string(newSPS) != string(sps) {
				sps, pps, vps, codec = newSPS, newPPS, newVPS, newCodec
				newInit := mux.InitSegment(codec, sps, pps, vps)
				if err := conn.WriteMessage(websocket.BinaryMessage, newInit); err != nil {
					return
				}
			}
		}

		avcc := mux.AnnexBtoAVCC(au.Data)
		if len(avcc) == 0 {
			continue
		}

		dts := uint64(au.Timestamp)
		var dur uint32
		if lastDTS > 0 && dts > lastDTS {
			dur = uint32(dts - lastDTS)
		}
		lastDTS = dts
		seq++

		seg := mux.MediaSegment(seq, dts, dur, au.Keyframe, avcc)
		conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if err := conn.WriteMessage(websocket.BinaryMessage, seg); err != nil {
			return
		}
	}
}

// handleWSStreamByID handles GET /ws/camera/{id} — looks up by camera ID.
func (s *Server) handleWSStreamByID(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	cam, ok := s.store.Get(id)
	if !ok {
		http.Error(w, "camera not found", 404)
		return
	}

	track := s.manager.Track(cam.StreamKey)
	if track == nil {
		http.Error(w, "stream not active — login first", 503)
		return
	}

	// Reuse the stream key handler.
	r = r.WithContext(r.Context())
	// Inject streamKey into URL params manually via chi context.
	rctx := chi.RouteContext(r.Context())
	rctx.URLParams.Add("streamKey", cam.StreamKey)
	s.handleWSStream(w, r)
}

// waitForTrack returns a *streaming.Track once it has params, or nil on timeout.
func waitForTrack(track *streaming.Track, timeout time.Duration) *streaming.Track {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		_, sps, _, _ := track.Params()
		if len(sps) > 0 {
			return track
		}
		time.Sleep(50 * time.Millisecond)
	}
	return nil
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

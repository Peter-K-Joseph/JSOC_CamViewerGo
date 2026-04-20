package web

import (
	"bytes"
	"encoding/binary"
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

	// Wait for codec params (SPS/PPS). Some cameras only emit them after the
	// first IDR packet, so keep the websocket open instead of timing out early.
	var codec string
	var sps, pps, vps []byte
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		codec, sps, pps, vps = track.Params()
		if len(sps) > 0 {
			break
		}
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
	}
	if len(sps) == 0 {
		log.Printf("[ws] %s: no SPS available, closing", streamKey)
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

// handleWSAnnexB handles GET /ws/annexb/{streamKey}
// Protocol:
//  1. Send JSON text: {"codec":"avc1.4D001E","format":"annexb-h264-v1"}
//  2. Send binary frames: 1-byte flags + 8-byte pts-us + Annex-B AU bytes
//     flags bit0 = keyframe
func (s *Server) handleWSAnnexB(w http.ResponseWriter, r *http.Request) {
	streamKey := chi.URLParam(r, "streamKey")

	track := s.manager.Track(streamKey)
	if track == nil {
		http.Error(w, "stream not found", 404)
		return
	}

	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[ws-annexb] upgrade error: %v", err)
		return
	}
	defer conn.Close()

	var codec string
	var sps, pps, _ , _ []byte
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		codec, sps, pps, _ = track.Params()
		if len(sps) > 0 {
			break
		}
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
	}

	if codec != "h264" {
		msg := map[string]string{
			"error":  "codec_not_supported",
			"detail": "annexb websocket currently supports only h264",
		}
		_ = conn.WriteMessage(websocket.TextMessage, mustJSON(msg))
		return
	}

	info := struct {
		Codec  string `json:"codec"`
		Format string `json:"format"`
	}{
		Codec:  mux.CodecString(codec, sps),
		Format: "annexb-h264-v1",
	}
	if err := conn.WriteMessage(websocket.TextMessage, mustJSON(info)); err != nil {
		return
	}

	ch := track.Subscribe()
	defer track.Unsubscribe(ch)

	waitKeyframe := true
	for au := range ch {
		if waitKeyframe {
			if !au.Keyframe {
				continue
			}
			waitKeyframe = false
		}

		payload := au.Data
		if au.Keyframe {
			_, curSPS, curPPS, _ := track.Params()
			if len(curSPS) > 0 && !bytes.Contains(payload, curSPS) {
				payload = prependNALUnits(payload, curSPS, curPPS)
			}
		}

		if len(payload) == 0 {
			continue
		}

		ptsUS := uint64(au.Timestamp) * 1000000 / 90000
		frame := make([]byte, 9+len(payload))
		if au.Keyframe {
			frame[0] = 0x01
		}
		binary.BigEndian.PutUint64(frame[1:9], ptsUS)
		copy(frame[9:], payload)

		conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if err := conn.WriteMessage(websocket.BinaryMessage, frame); err != nil {
			return
		}
	}
}

// handleWSAnnexBByID handles GET /ws/camera/{id}/annexb — looks up by camera ID.
func (s *Server) handleWSAnnexBByID(w http.ResponseWriter, r *http.Request) {
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

	r = r.WithContext(r.Context())
	rctx := chi.RouteContext(r.Context())
	rctx.URLParams.Add("streamKey", cam.StreamKey)
	s.handleWSAnnexB(w, r)
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

func prependNALUnits(payload, sps, pps []byte) []byte {
	if len(sps) == 0 {
		return payload
	}
	out := make([]byte, 0, len(payload)+16+len(sps)+len(pps))
	out = append(out, 0x00, 0x00, 0x00, 0x01)
	out = append(out, sps...)
	if len(pps) > 0 {
		out = append(out, 0x00, 0x00, 0x00, 0x01)
		out = append(out, pps...)
	}
	out = append(out, payload...)
	return out
}

'use strict';

/**
 * MSEPlayer — streams fMP4 segments from /ws/stream/{key} into a <video> element.
 *
 * Protocol:
 *   1. First WS message (text): JSON {"codec":"avc1.4D001E","mimeType":"video/mp4; codecs=\"...\""}
 *   2. Subsequent WS messages (binary): fMP4 init segment, then media segments
 */
class MSEPlayer {
  constructor(videoEl, wsUrl) {
    this.video  = videoEl;
    this.wsUrl  = wsUrl;
    this.ws     = null;
    this.ms     = null;
    this.sb     = null;
    this.queue  = [];          // pending ArrayBuffers to append
    this.gotInit = false;
    this.mimeType = null;
    this.destroyed = false;

    this._drain = this._drain.bind(this);
    this._start();
  }

  _start() {
    this.ms = new MediaSource();
    this.video.src = URL.createObjectURL(this.ms);
    this.ms.addEventListener('sourceopen', () => this._onSourceOpen(), { once: true });
  }

  _onSourceOpen() {
    // Wait for the codec info JSON before adding SourceBuffer
    this._connect();
  }

  _connect() {
    if (this.destroyed) return;
    this.ws = new WebSocket(this.wsUrl);
    this.ws.binaryType = 'arraybuffer';

    this.ws.onmessage = (e) => {
      if (typeof e.data === 'string') {
        // JSON codec info
        try {
          const info = JSON.parse(e.data);
          this.mimeType = info.mimeType || `video/mp4; codecs="${info.codec}"`;
          if (!this.sb && this.ms.readyState === 'open') {
            this._addSourceBuffer(this.mimeType);
          }
        } catch (_) {}
        return;
      }
      // Binary: fMP4 segment
      this.queue.push(e.data);
      this._drain();
    };

    this.ws.onerror = () => {};
    this.ws.onclose = () => {
      if (!this.destroyed) {
        // Reconnect after 2s
        setTimeout(() => {
          this.gotInit = false;
          this.queue = [];
          this._connect();
        }, 2000);
      }
    };
  }

  _addSourceBuffer(mimeType) {
    try {
      this.sb = this.ms.addSourceBuffer(mimeType);
      this.sb.mode = 'segments';
      this.sb.addEventListener('updateend', this._drain);
    } catch (e) {
      console.error('[MSEPlayer] addSourceBuffer failed:', mimeType, e);
      // Try a generic H.264 fallback
      if (mimeType !== 'video/mp4; codecs="avc1.42E01E"') {
        this._addSourceBuffer('video/mp4; codecs="avc1.42E01E"');
      }
    }
  }

  _drain() {
    if (!this.sb || this.sb.updating || this.queue.length === 0) return;
    const chunk = this.queue.shift();
    try {
      this.sb.appendBuffer(chunk);
      // Auto-evict old buffered data to prevent memory growth
      if (this.sb.buffered.length > 0) {
        const end = this.sb.buffered.end(this.sb.buffered.length - 1);
        const start = this.sb.buffered.start(0);
        if (end - start > 30) {
          try { this.sb.remove(start, end - 20); } catch (_) {}
        }
      }
    } catch (e) {
      if (e.name === 'QuotaExceededError') {
        // Evict and retry
        if (this.sb.buffered.length > 0) {
          const s = this.sb.buffered.start(0);
          const en = this.sb.buffered.end(0);
          try { this.sb.remove(s, Math.min(s + 10, en)); } catch (_) {}
        }
        this.queue.unshift(chunk);
      }
    }
  }

  destroy() {
    this.destroyed = true;
    if (this.ws) { this.ws.close(); this.ws = null; }
    if (this.ms && this.ms.readyState === 'open') {
      try { this.ms.endOfStream(); } catch (_) {}
    }
    this.video.src = '';
  }
}

window.MSEPlayer = MSEPlayer;

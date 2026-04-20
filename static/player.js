'use strict';

/**
 * MSEPlayer — streams fMP4 segments from /ws/stream/{key} into a <video> element.
 *
 * Protocol:
 *   1. First WS message (text): JSON {"codec":"avc1.4D001E","mimeType":"video/mp4; codecs=\"...\""}
 *   2. Subsequent WS messages (binary): fMP4 init segment, then media segments
 */
class MSEPlayer {
  constructor(videoEl, wsUrl, options = {}) {
    this.video  = videoEl;
    this.wsUrl  = wsUrl;
    this.fallbackUrl = options.fallbackUrl || null;
    this.startupTimeoutMs = options.startupTimeoutMs || 10000;
    this.ws     = null;
    this.ms     = null;
    this.sb     = null;
    this.queue  = [];          // pending ArrayBuffers to append
    this.gotInit = false;
    this.mimeType = null;
    this.destroyed = false;
    this.started = false;
    this.fallbackActive = false;
    this.fallbackImg = null;
    this.startupTimer = null;

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
    this._clearStartupTimer();
    this.ws = new WebSocket(this.wsUrl);
    this.ws.binaryType = 'arraybuffer';

    this.ws.onopen = () => {
      console.log('[MSEPlayer] websocket open', this.wsUrl);
      this._startStartupTimer();
    };

    this.ws.onmessage = (e) => {
      if (typeof e.data === 'string') {
        // JSON codec info
        try {
          const info = JSON.parse(e.data);
          this.mimeType = info.mimeType || `video/mp4; codecs="${info.codec}"`;
          console.log('[MSEPlayer] codec info', this.mimeType);
          if (!this._isSupportedMimeType(this.mimeType)) {
            this._fallback('unsupported MIME type: ' + this.mimeType);
            return;
          }
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

    this.ws.onerror = (e) => {
      console.error('[MSEPlayer] websocket error', e);
    };
    this.ws.onclose = () => {
      if (!this.destroyed) {
        console.warn('[MSEPlayer] websocket closed, reconnecting');
        if (!this.started) {
          this._fallback('websocket closed before playback started');
          return;
        }
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
      if (!this._isSupportedMimeType(mimeType)) {
        this._fallback('unsupported MIME type: ' + mimeType);
        return;
      }
      this.sb = this.ms.addSourceBuffer(mimeType);
      this.sb.mode = 'segments';
      this.sb.addEventListener('updateend', this._drain);
      this.sb.addEventListener('error', (e) => {
        console.error('[MSEPlayer] sourcebuffer error', e);
        this._fallback('sourcebuffer error');
      });
    } catch (e) {
      console.error('[MSEPlayer] addSourceBuffer failed:', mimeType, e);
      this._fallback('addSourceBuffer failed: ' + mimeType);
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
        return;
      }
      this._fallback('appendBuffer failed: ' + (e && e.name ? e.name : 'unknown'));
    }

    this._ensurePlaying();
  }

  _ensurePlaying() {
    if (this.started || !this.video) return;
    if (this.video.readyState >= 1 || this.sb) {
      this.started = true;
      this._clearStartupTimer();
      const playResult = this.video.play();
      if (playResult && typeof playResult.catch === 'function') {
        playResult.catch((e) => {
          console.warn('[MSEPlayer] video.play() blocked or failed', e);
          this._fallback('video.play() failed');
        });
      }
    }
  }

  _isSupportedMimeType(mimeType) {
    if (!('MediaSource' in window) || typeof MediaSource.isTypeSupported !== 'function') {
      return false;
    }
    try {
      return MediaSource.isTypeSupported(mimeType);
    } catch (_) {
      return false;
    }
  }

  _startStartupTimer() {
    if (!this.fallbackUrl || this.startupTimer || this.started || this.fallbackActive) return;
    this.startupTimer = setTimeout(() => {
      this.startupTimer = null;
      if (!this.started) {
        this._fallback('startup timeout');
      }
    }, this.startupTimeoutMs);
  }

  _clearStartupTimer() {
    if (this.startupTimer) {
      clearTimeout(this.startupTimer);
      this.startupTimer = null;
    }
  }

  _fallback(reason) {
    if (!this.fallbackUrl || this.fallbackActive || this.destroyed) {
      return;
    }
    this.fallbackActive = true;
    console.warn('[MSEPlayer] falling back to MJPEG:', reason);
    this._clearStartupTimer();
    if (this.ws) { this.ws.close(); this.ws = null; }
    if (this.ms && this.ms.readyState === 'open') {
      try { this.ms.endOfStream(); } catch (_) {}
    }

    const parent = this.video && this.video.parentElement;
    if (!parent) return;

    if (!this.fallbackImg) {
      const img = document.createElement('img');
      img.src = `${this.fallbackUrl}?_=${Date.now()}`;
      img.alt = this.video.alt || '';
      img.style.width = '100%';
      img.style.height = '100%';
      img.style.objectFit = 'contain';
      img.style.display = 'block';
      img.style.background = '#000';
      img.style.position = 'absolute';
      img.style.inset = '0';
      img.style.zIndex = '1';
      this.fallbackImg = img;
      parent.style.position = parent.style.position || 'relative';
      parent.appendChild(img);
    }

    this.video.style.display = 'none';
  }

  destroy() {
    this.destroyed = true;
    this._clearStartupTimer();
    if (this.ws) { this.ws.close(); this.ws = null; }
    if (this.ms && this.ms.readyState === 'open') {
      try { this.ms.endOfStream(); } catch (_) {}
    }
    if (this.fallbackImg) {
      this.fallbackImg.remove();
      this.fallbackImg = null;
    }
    if (this.video) {
      this.video.style.display = '';
    }
    this.video.src = '';
  }
}

window.MSEPlayer = MSEPlayer;

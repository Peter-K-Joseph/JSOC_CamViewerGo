'use strict';

/**
 * CanvasPlayer — streams Annex-B H.264/H.265 AccessUnits from /ws/annexb/{key}
 * and renders decoded frames to a canvas via WebCodecs VideoDecoder.
 *
 * Protocol:
 *   1. First WS message (text): JSON {"codec":"avc1.4D001E","streamCodec":"h264","format":"annexb-v1"}
 *   2. Subsequent WS messages (binary): [1-byte flags][8-byte pts-us][Annex-B access unit]
 *      - flags bit0: keyframe
 */
class CanvasPlayer {
  constructor(videoEl, wsUrl, options = {}) {
    this.video = videoEl;
    this.wsUrl = wsUrl;
    this.fallbackUrl = options.fallbackUrl || null;
    this.startupTimeoutMs = options.startupTimeoutMs || 10000;
    this.ws = null;
    this.decoder = null;
    this.codec = null;
    this.streamCodec = null;
    this.destroyed = false;
    this.started = false;
    this.fallbackActive = false;
    this.fallbackImg = null;
    this.startupTimer = null;
    this.canvas = null;
    this.ctx = null;
    this.frameCounter = 0;
    this.lastPTS = 0;

    this._onResize = this._onResize.bind(this);
    this._start();
  }

  _start() {
    this._setupCanvas();
    this._connect();
  }

  _setupCanvas() {
    if (!this.video || !this.video.parentElement) {
      return;
    }
    this.video.style.display = 'none';
    const parent = this.video.parentElement;
    parent.style.position = parent.style.position || 'relative';

    const canvas = document.createElement('canvas');
    canvas.style.position = 'absolute';
    canvas.style.inset = '0';
    canvas.style.width = '100%';
    canvas.style.height = '100%';
    canvas.style.background = '#000';
    canvas.style.display = 'block';
    canvas.style.zIndex = '1';
    parent.appendChild(canvas);

    this.canvas = canvas;
    this.ctx = canvas.getContext('2d', { alpha: false, desynchronized: true });
    window.addEventListener('resize', this._onResize);
    this._onResize();
  }

  _onResize() {
    if (!this.canvas) return;
    const dpr = window.devicePixelRatio || 1;
    const w = Math.max(1, Math.floor(this.canvas.clientWidth * dpr));
    const h = Math.max(1, Math.floor(this.canvas.clientHeight * dpr));
    if (this.canvas.width !== w || this.canvas.height !== h) {
      this.canvas.width = w;
      this.canvas.height = h;
    }
  }

  _connect() {
    if (this.destroyed) return;
    this._clearStartupTimer();

    if (!('VideoDecoder' in window)) {
      this._fallback('webcodecs not available in this browser');
      return;
    }

    this.ws = new WebSocket(this.wsUrl);
    this.ws.binaryType = 'arraybuffer';

    this.ws.onopen = () => {
      console.log('[CanvasPlayer] websocket open', this.wsUrl);
      this._startStartupTimer();
    };

    this.ws.onmessage = (e) => {
      if (typeof e.data === 'string') {
        // JSON stream info
        try {
          const info = JSON.parse(e.data);
          if (info.error) {
            this._fallback(info.detail || info.error);
            return;
          }
          this.codec = info.codec || 'avc1.42E01E';
          this.streamCodec = info.streamCodec || (this.codec.startsWith('hvc1') || this.codec.startsWith('hev1') ? 'h265' : 'h264');
          this._setupDecoder(this.codec, this.streamCodec);
        } catch (err) {
          this._fallback('invalid stream metadata');
        }
        return;
      }

      this._handleFrameBinary(e.data);
    };

    this.ws.onerror = (e) => {
      console.error('[CanvasPlayer] websocket error', e);
    };

    this.ws.onclose = () => {
      if (!this.destroyed) {
        console.warn('[CanvasPlayer] websocket closed, reconnecting');
        if (!this.started) {
          this._fallback('websocket closed before playback started');
          return;
        }
        setTimeout(() => {
          if (!this.destroyed && !this.fallbackActive) {
            this.lastPTS = 0;
            this.frameCounter = 0;
          }
          this._connect();
        }, 2000);
      }
    };
  }

  _setupDecoder(codec, streamCodec) {
    if (this.decoder || this.destroyed) return;

    const candidates = this._decoderConfigCandidates(codec, streamCodec);
    const isH265 = streamCodec === 'h265' || /^hvc1|^hev1/.test(codec);

    // Async pre-check: use isConfigSupported() to avoid the 10s startup
    // timeout when the browser lacks H.265 hardware decoding support.
    if (typeof VideoDecoder.isConfigSupported === 'function') {
      Promise.all(candidates.map(cfg => VideoDecoder.isConfigSupported(cfg).catch(() => ({ supported: false }))))
        .then(results => {
          if (this.destroyed) return;
          const idx = results.findIndex(r => r.supported);
          if (idx < 0) {
            const reason = isH265
              ? 'h265_not_supported'
              : 'no supported decoder config';
            this._fallback(reason);
            return;
          }
          this._createDecoder(candidates[idx], streamCodec);
        });
    } else {
      // Fallback: try synchronous configure (legacy path).
      this._createDecoder(candidates[0], streamCodec);
    }
  }

  _createDecoder(cfg, streamCodec) {
    if (this.decoder || this.destroyed) return;
    try {
      this.decoder = new VideoDecoder({
        output: (frame) => this._drawFrame(frame),
        error: (err) => {
          console.error('[CanvasPlayer] decoder error', err);
          this._fallback('decoder error');
        },
      });
      this.decoder.configure(cfg);
      this.codec = cfg.codec;
    } catch (err) {
      console.error('[CanvasPlayer] decoder setup failed', err);
      this._fallback('failed to initialize video decoder for ' + (streamCodec || 'unknown'));
    }
  }

  _decoderConfigCandidates(codec, streamCodec) {
    const base = {
      optimizeForLatency: true,
      hardwareAcceleration: 'prefer-hardware',
    };

    const dedupe = new Set();
    const out = [];
    const push = (cfg) => {
      const key = JSON.stringify(cfg);
      if (!dedupe.has(key)) {
        dedupe.add(key);
        out.push(cfg);
      }
    };

    if (streamCodec === 'h265' || /^hvc1|^hev1/.test(codec)) {
      push({ ...base, codec });
      push({ ...base, codec: codec.replace(/^hvc1/, 'hev1') });
      push({ ...base, codec: codec.replace(/^hev1/, 'hvc1') });
      push({ ...base, codec: 'hvc1.1.6.L93.B0' });
      push({ ...base, codec: 'hev1.1.6.L93.B0' });
      return out;
    }

    push({ ...base, codec, avc: { format: 'annexb' } });
    push({ ...base, codec });
    push({ ...base, codec: 'avc1.42E01E', avc: { format: 'annexb' } });
    push({ ...base, codec: 'avc1.42E01E' });
    return out;
  }

  _handleFrameBinary(data) {
    if (!this.decoder || this.destroyed) return;
    if (!(data instanceof ArrayBuffer) || data.byteLength <= 9) return;

    const view = new DataView(data);
    const flags = view.getUint8(0);
    const keyframe = (flags & 0x01) === 0x01;

    const hi = view.getUint32(1, false);
    const lo = view.getUint32(5, false);
    let ts = hi * 4294967296 + lo;
    if (!Number.isFinite(ts) || ts < 0) {
      ts = this.lastPTS + 33333;
    }
    if (ts <= this.lastPTS) {
      ts = this.lastPTS + 1;
    }
    this.lastPTS = ts;

    const payload = new Uint8Array(data, 9);
    if (payload.byteLength === 0) return;

    // Keep decode queue bounded in live mode.
    if (this.decoder.decodeQueueSize > 8 && !keyframe) {
      return;
    }

    try {
      const chunk = new EncodedVideoChunk({
        type: keyframe ? 'key' : 'delta',
        timestamp: ts,
        data: payload,
      });
      this.decoder.decode(chunk);
      this.frameCounter++;
    } catch (err) {
      console.warn('[CanvasPlayer] decode failed', err);
      if (keyframe) {
        this._fallback('decode failed on keyframe');
      }
    }

    if (!this.started && this.frameCounter > 0) {
      this.started = true;
      this._clearStartupTimer();
    }
  }

  _drawFrame(frame) {
    if (!this.canvas || !this.ctx) {
      frame.close();
      return;
    }
    this._onResize();

    const ctx = this.ctx;
    const cw = this.canvas.width;
    const ch = this.canvas.height;
    const fw = frame.displayWidth || frame.codedWidth;
    const fh = frame.displayHeight || frame.codedHeight;

    ctx.fillStyle = '#000';
    ctx.fillRect(0, 0, cw, ch);

    const scale = Math.min(cw / fw, ch / fh);
    const dw = Math.max(1, Math.floor(fw * scale));
    const dh = Math.max(1, Math.floor(fh * scale));
    const dx = Math.floor((cw - dw) / 2);
    const dy = Math.floor((ch - dh) / 2);

    try {
      ctx.drawImage(frame, dx, dy, dw, dh);
    } finally {
      frame.close();
    }

    if (!this.started) {
      this.started = true;
      this._clearStartupTimer();
    }

    this._ensurePlaying();
  }

  _ensurePlaying() {
    if (this.started || !this.video) return;
    if (this.frameCounter > 0) {
      this.started = true;
      this._clearStartupTimer();
      const playResult = this.video.play();
      if (playResult && typeof playResult.catch === 'function') {
        playResult.catch((e) => {
          console.warn('[CanvasPlayer] video.play() ignored', e);
        });
      }
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
    console.warn('[CanvasPlayer] falling back to MJPEG:', reason);
    this._clearStartupTimer();
    if (this.ws) { this.ws.close(); this.ws = null; }
    if (this.decoder) {
      try { this.decoder.close(); } catch (_) {}
      this.decoder = null;
    }

    const parent = this.video && this.video.parentElement;
    if (!parent) return;

    // Show H.265 requirements banner when codec is unsupported.
    if (reason === 'h265_not_supported') {
      const banner = document.createElement('div');
      banner.className = 'h265-banner';
      banner.style.cssText = 'position:absolute;bottom:8px;left:8px;right:8px;z-index:3;' +
        'background:rgba(0,0,0,0.75);color:#f59e0b;font-size:0.72rem;padding:6px 10px;' +
        'border-radius:6px;text-align:center;pointer-events:none';
      banner.textContent = 'H.265 \u2014 requires Safari 17+, Chrome 107+ (hardware), or use VLC with the RTSP URL';
      parent.style.position = parent.style.position || 'relative';
      parent.appendChild(banner);
    }

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
    if (this.canvas) {
      this.canvas.style.display = 'none';
    }
  }

  destroy() {
    this.destroyed = true;
    this._clearStartupTimer();
    if (this.ws) { this.ws.close(); this.ws = null; }
    if (this.decoder) {
      try { this.decoder.close(); } catch (_) {}
      this.decoder = null;
    }
    window.removeEventListener('resize', this._onResize);
    if (this.fallbackImg) {
      this.fallbackImg.remove();
      this.fallbackImg = null;
    }
    if (this.canvas) {
      this.canvas.remove();
      this.canvas = null;
      this.ctx = null;
    }
    if (this.video) {
      this.video.style.display = '';
      this.video.removeAttribute('src');
      try { this.video.load(); } catch (_) {}
    }
  }
}

// Keep legacy export name so existing dashboard wiring keeps working.
window.MSEPlayer = CanvasPlayer;
window.CanvasPlayer = CanvasPlayer;

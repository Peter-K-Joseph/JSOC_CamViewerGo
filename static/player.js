'use strict';

// ── WebGL YUV420p → RGB renderer ─────────────────────────────────────────────
//
// Used when the WASM H.265 decoder is active (Firefox).  The decoder outputs
// raw YUV420p frames; a tiny WebGL shader converts them to RGB on the GPU so
// the CPU conversion loop is avoided entirely.

class YUVRenderer {
  constructor(canvas) {
    const gl = canvas.getContext('webgl', { alpha: false });
    if (!gl) throw new Error('webgl unavailable');
    this.gl = gl;

    const vert = `
      attribute vec2 a_pos;
      attribute vec2 a_uv;
      varying   vec2 v_uv;
      void main() { gl_Position = vec4(a_pos, 0.0, 1.0); v_uv = a_uv; }`;

    // BT.601 limited-range YUV → RGB
    const frag = `
      precision mediump float;
      uniform sampler2D u_y;
      uniform sampler2D u_u;
      uniform sampler2D u_v;
      varying vec2 v_uv;
      void main() {
        float y =  texture2D(u_y, v_uv).r;
        float u =  texture2D(u_u, v_uv).r - 0.5;
        float v =  texture2D(u_v, v_uv).r - 0.5;
        gl_FragColor = vec4(
          clamp(y + 1.402  * v,               0.0, 1.0),
          clamp(y - 0.3441 * u - 0.7141 * v,  0.0, 1.0),
          clamp(y + 1.772  * u,               0.0, 1.0),
          1.0);
      }`;

    const prog = this._compile(vert, frag);
    gl.useProgram(prog);

    // Fullscreen quad: two triangles
    const verts = new Float32Array([
      -1, -1,  0, 1,
       1, -1,  1, 1,
      -1,  1,  0, 0,
       1,  1,  1, 0,
    ]);
    const buf = gl.createBuffer();
    gl.bindBuffer(gl.ARRAY_BUFFER, buf);
    gl.bufferData(gl.ARRAY_BUFFER, verts, gl.STATIC_DRAW);

    const aPos = gl.getAttribLocation(prog, 'a_pos');
    const aUV  = gl.getAttribLocation(prog, 'a_uv');
    gl.enableVertexAttribArray(aPos);
    gl.enableVertexAttribArray(aUV);
    gl.vertexAttribPointer(aPos, 2, gl.FLOAT, false, 16, 0);
    gl.vertexAttribPointer(aUV,  2, gl.FLOAT, false, 16, 8);

    this._yTex = this._mkTex(gl);
    this._uTex = this._mkTex(gl);
    this._vTex = this._mkTex(gl);

    gl.uniform1i(gl.getUniformLocation(prog, 'u_y'), 0);
    gl.uniform1i(gl.getUniformLocation(prog, 'u_u'), 1);
    gl.uniform1i(gl.getUniformLocation(prog, 'u_v'), 2);

    this._prog = prog;
    this._w = 0;
    this._h = 0;
  }

  draw(yuv, pixW, pixH, ylen) {
    const gl = this.gl;
    const halfW = pixW >> 1;
    const halfH = pixH >> 1;

    if (pixW !== this._w || pixH !== this._h) {
      gl.canvas.width  = pixW;
      gl.canvas.height = pixH;
      gl.viewport(0, 0, pixW, pixH);
      this._w = pixW;
      this._h = pixH;
    }

    const yuv8 = new Uint8Array(yuv);
    const uOff = ylen;
    const vOff = ylen + (ylen >> 2);

    this._upload(this._yTex, 0, yuv8, 0,    pixW,  pixH);
    this._upload(this._uTex, 1, yuv8, uOff, halfW, halfH);
    this._upload(this._vTex, 2, yuv8, vOff, halfW, halfH);

    gl.drawArrays(gl.TRIANGLE_STRIP, 0, 4);
  }

  _upload(tex, unit, data, offset, w, h) {
    const gl = this.gl;
    gl.activeTexture(gl.TEXTURE0 + unit);
    gl.bindTexture(gl.TEXTURE_2D, tex);
    gl.pixelStorei(gl.UNPACK_ALIGNMENT, 1);
    gl.texImage2D(
      gl.TEXTURE_2D, 0, gl.LUMINANCE, w, h, 0,
      gl.LUMINANCE, gl.UNSIGNED_BYTE,
      new Uint8Array(data.buffer, data.byteOffset + offset, w * h),
    );
  }

  _mkTex(gl) {
    const t = gl.createTexture();
    gl.bindTexture(gl.TEXTURE_2D, t);
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MIN_FILTER, gl.LINEAR);
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MAG_FILTER, gl.LINEAR);
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_S, gl.CLAMP_TO_EDGE);
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_T, gl.CLAMP_TO_EDGE);
    return t;
  }

  _compile(vsrc, fsrc) {
    const gl = this.gl;
    const vs = gl.createShader(gl.VERTEX_SHADER);
    gl.shaderSource(vs, vsrc); gl.compileShader(vs);
    const fs = gl.createShader(gl.FRAGMENT_SHADER);
    gl.shaderSource(fs, fsrc); gl.compileShader(fs);
    const p = gl.createProgram();
    gl.attachShader(p, vs); gl.attachShader(p, fs);
    gl.linkProgram(p);
    return p;
  }
}

// ── CanvasPlayer ──────────────────────────────────────────────────────────────
//
// Streams Annex-B H.264/H.265 AccessUnits from /ws/annexb/{key} and renders
// decoded frames to a canvas via WebCodecs VideoDecoder (all codecs on
// Safari/Chrome) or the camera's WASM H.265 decoder (Firefox H.265 fallback).
//
// Protocol:
//   1. First WS message (text): JSON {"codec":"avc1.4D001E","streamCodec":"h264","format":"annexb-v1"}
//   2. Subsequent WS messages (binary): [1-byte flags][8-byte pts-us][Annex-B access unit]
//      - flags bit0: keyframe

class CanvasPlayer {
  constructor(videoEl, wsUrl, options = {}) {
    this.video          = videoEl;
    this.wsUrl          = wsUrl;
    this.fallbackUrl    = options.fallbackUrl    || null;
    this.wasmCameraId   = options.wasmCameraId   || null;
    this.startupTimeoutMs = options.startupTimeoutMs || 10000;

    this.ws             = null;
    this.decoder        = null;   // WebCodecs VideoDecoder
    this.wasmWorker     = null;   // Web Worker running h265-worker.js
    this.wasmReady      = false;
    this.yuvRenderer    = null;   // YUVRenderer (WebGL)
    this.glCanvas       = null;   // dedicated WebGL canvas

    this.codec          = null;
    this.streamCodec    = null;
    this.destroyed      = false;
    this.started        = false;
    this.fallbackActive = false;
    this.fallbackImg    = null;
    this.startupTimer   = null;
    this.canvas         = null;   // 2D canvas for WebCodecs path
    this.ctx            = null;
    this.frameCounter   = 0;
    this.lastPTS        = 0;

    // rAF-paced rendering for WebCodecs path.
    this.pendingFrame   = null;
    this.rafId          = null;
    this._renderFrame   = this._renderFrame.bind(this);

    this._onResize      = this._onResize.bind(this);
    this._start();
  }

  _start() {
    this._setupCanvas();
    this._connect();
  }

  // ── Canvas setup ────────────────────────────────────────────────────────────

  _setupCanvas() {
    if (!this.video || !this.video.parentElement) return;
    this.video.style.display = 'none';
    const parent = this.video.parentElement;
    parent.style.position = parent.style.position || 'relative';

    const canvas = document.createElement('canvas');
    canvas.style.cssText = 'position:absolute;inset:0;width:100%;height:100%;background:#000;display:block;z-index:1';
    parent.appendChild(canvas);

    this.canvas = canvas;
    this.ctx    = canvas.getContext('2d', { alpha: false, desynchronized: true });
    window.addEventListener('resize', this._onResize);
    this._onResize();
  }

  // Create a separate WebGL canvas for the WASM YUV path, stacked above the
  // 2D canvas so we can toggle between them cleanly.
  _setupGLCanvas() {
    if (this.glCanvas || !this.canvas) return;
    const parent = this.canvas.parentElement;
    if (!parent) return;

    const gl = document.createElement('canvas');
    gl.style.cssText = 'position:absolute;inset:0;width:100%;height:100%;background:#000;display:none;z-index:2';
    parent.appendChild(gl);
    this.glCanvas = gl;

    try {
      this.yuvRenderer = new YUVRenderer(gl);
    } catch (err) {
      console.error('[CanvasPlayer] WebGL unavailable for YUV rendering', err);
      this._fallback('webgl_unavailable');
    }
  }

  _onResize() {
    if (!this.canvas) return;
    const dpr = window.devicePixelRatio || 1;
    const w   = Math.max(1, Math.floor(this.canvas.clientWidth  * dpr));
    const h   = Math.max(1, Math.floor(this.canvas.clientHeight * dpr));
    if (this.canvas.width !== w || this.canvas.height !== h) {
      this.canvas.width  = w;
      this.canvas.height = h;
    }
  }

  // ── WebSocket connection ─────────────────────────────────────────────────────

  _connect() {
    if (this.destroyed) return;
    this._clearStartupTimer();

    if (!('VideoDecoder' in window) && !this.wasmCameraId) {
      this._fallback('webcodecs not available and no wasm fallback configured');
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
        try {
          const info = JSON.parse(e.data);
          if (info.error) { this._fallback(info.detail || info.error); return; }
          this.codec       = info.codec       || 'avc1.42E01E';
          this.streamCodec = info.streamCodec || (
            /^hvc1|^hev1/.test(this.codec) ? 'h265' : 'h264'
          );
          this._setupDecoder(this.codec, this.streamCodec);
        } catch (_) {
          this._fallback('invalid stream metadata');
        }
        return;
      }
      this._handleFrameBinary(e.data);
    };

    this.ws.onerror  = (e) => console.error('[CanvasPlayer] ws error', e);
    this.ws.onclose  = () => {
      if (this.destroyed) return;
      console.warn('[CanvasPlayer] websocket closed, reconnecting');
      if (!this.started) { this._fallback('websocket closed before playback started'); return; }
      setTimeout(() => {
        if (!this.destroyed && !this.fallbackActive) {
          this.lastPTS = 0; this.frameCounter = 0;
        }
        this._connect();
      }, 2000);
    };
  }

  // ── Decoder setup ────────────────────────────────────────────────────────────

  _setupDecoder(codec, streamCodec) {
    if (this.destroyed) return;
    const isH265 = streamCodec === 'h265' || /^hvc1|^hev1/.test(codec);

    // ── WebCodecs path (Safari, Chrome) ─────────────────────────────────────
    if ('VideoDecoder' in window && typeof VideoDecoder.isConfigSupported === 'function') {
      const candidates = this._decoderConfigCandidates(codec, streamCodec);
      Promise.all(candidates.map(cfg =>
        VideoDecoder.isConfigSupported(cfg).catch(() => ({ supported: false }))
      )).then(results => {
        if (this.destroyed) return;
        const idx = results.findIndex(r => r.supported);
        if (idx >= 0) {
          this._createDecoder(candidates[idx], streamCodec);
          return;
        }
        // WebCodecs rejected — try WASM for H.265, hard fallback otherwise.
        if (isH265 && this.wasmCameraId) {
          this._setupWasmH265();
        } else {
          this._fallback(isH265 ? 'h265_not_supported' : 'no supported decoder config');
        }
      });
      return;
    }

    // No isConfigSupported — try WebCodecs synchronously, WASM for H.265 if absent.
    if ('VideoDecoder' in window) {
      this._createDecoder(this._decoderConfigCandidates(codec, streamCodec)[0], streamCodec);
      return;
    }

    if (isH265 && this.wasmCameraId) {
      this._setupWasmH265();
    } else {
      this._fallback(isH265 ? 'h265_not_supported' : 'webcodecs not available');
    }
  }

  _createDecoder(cfg, streamCodec) {
    if (this.decoder || this.destroyed) return;
    try {
      this.decoder = new VideoDecoder({
        output: (frame) => this._drawFrame(frame),
        error:  (err)   => {
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
    const base   = { optimizeForLatency: true, hardwareAcceleration: 'prefer-hardware' };
    const seen   = new Set();
    const out    = [];
    const push   = (cfg) => { const k = JSON.stringify(cfg); if (!seen.has(k)) { seen.add(k); out.push(cfg); } };

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

  // ── WASM H.265 path (Firefox) ────────────────────────────────────────────────

  _setupWasmH265() {
    if (this.wasmWorker || this.destroyed) return;
    this._setupGLCanvas();
    if (this.fallbackActive) return; // WebGL unavailable, already fell back

    const id = this.wasmCameraId;
    console.log('[CanvasPlayer] starting WASM H.265 decoder for camera', id);

    const worker = new Worker('/static/h265-worker.js');
    this.wasmWorker = worker;

    worker.onmessage = (e) => {
      const msg = e.data;
      if (msg.type === 'ready') {
        console.log('[CanvasPlayer] WASM H.265 decoder ready');
        this.wasmReady = true;
        this._clearStartupTimer();
        if (!this.started) {
          this.started = true;
        }
        // Switch display: hide 2D canvas, show GL canvas.
        if (this.canvas)   this.canvas.style.display   = 'none';
        if (this.glCanvas) this.glCanvas.style.display = 'block';
      } else if (msg.type === 'frame') {
        this._drawYUVFrame(msg);
      } else if (msg.type === 'error') {
        console.error('[CanvasPlayer] WASM H.265 worker error:', msg.message);
        this._teardownWasm();
        this._fallback('h265_not_supported');
      }
    };

    worker.onerror = (err) => {
      console.error('[CanvasPlayer] WASM worker load error', err);
      this._teardownWasm();
      this._fallback('h265_not_supported');
    };

    worker.postMessage({
      type:       'init',
      wasmUrl:    `/proxy/cameras/${id}/decoder/ffmpegasm.js`,
      decoderUrl: `/proxy/cameras/${id}/decoder/h265Decoder.js`,
    });
  }

  _teardownWasm() {
    if (this.wasmWorker) {
      this.wasmWorker.terminate();
      this.wasmWorker = null;
    }
    this.wasmReady = false;
  }

  // ── Frame handling ───────────────────────────────────────────────────────────

  _handleFrameBinary(data) {
    if (this.destroyed) return;
    if (!(data instanceof ArrayBuffer) || data.byteLength <= 9) return;

    const view     = new DataView(data);
    const flags    = view.getUint8(0);
    const keyframe = (flags & 0x01) === 0x01;

    const hi = view.getUint32(1, false);
    const lo = view.getUint32(5, false);
    let ts = hi * 4294967296 + lo;
    if (!Number.isFinite(ts) || ts < 0) ts = this.lastPTS + 33333;
    if (ts <= this.lastPTS) ts = this.lastPTS + 1;
    this.lastPTS = ts;

    const payload = new Uint8Array(data, 9);
    if (payload.byteLength === 0) return;

    // ── WASM path ─────────────────────────────────────────────────────────────
    if (this.wasmWorker) {
      if (!this.wasmReady) return; // still initialising
      // Only send keyframes until we're primed; avoids feeding garbage before
      // the decoder has seen a full IDR.
      const copy = payload.slice();
      this.wasmWorker.postMessage({ type: 'frame', annexb: copy.buffer }, [copy.buffer]);
      this.frameCounter++;
      return;
    }

    // ── WebCodecs path ────────────────────────────────────────────────────────
    if (!this.decoder) return;
    if (this.decoder.decodeQueueSize > 30 && !keyframe) return;

    try {
      this.decoder.decode(new EncodedVideoChunk({
        type:      keyframe ? 'key' : 'delta',
        timestamp: ts,
        data:      payload,
      }));
      this.frameCounter++;
    } catch (err) {
      console.warn('[CanvasPlayer] decode failed', err);
      if (keyframe) this._fallback('decode failed on keyframe');
    }

    if (!this.started && this.frameCounter > 0) {
      this.started = true;
      this._clearStartupTimer();
    }
  }

  // ── WebCodecs rendering (rAF-paced) ─────────────────────────────────────────

  _drawFrame(frame) {
    if (this.pendingFrame) this.pendingFrame.close();
    this.pendingFrame = frame;
    if (!this.rafId && !this.destroyed) {
      this.rafId = requestAnimationFrame(this._renderFrame);
    }
    if (!this.started) { this.started = true; this._clearStartupTimer(); }
  }

  _renderFrame() {
    this.rafId = null;
    const frame = this.pendingFrame;
    if (!frame || !this.canvas || !this.ctx) {
      if (frame) { frame.close(); this.pendingFrame = null; }
      return;
    }
    this.pendingFrame = null;
    this._onResize();

    const ctx = this.ctx;
    const cw  = this.canvas.width;
    const ch  = this.canvas.height;
    const fw  = frame.displayWidth  || frame.codedWidth;
    const fh  = frame.displayHeight || frame.codedHeight;

    ctx.fillStyle = '#000';
    ctx.fillRect(0, 0, cw, ch);

    const scale = Math.min(cw / fw, ch / fh);
    const dw    = Math.max(1, Math.floor(fw * scale));
    const dh    = Math.max(1, Math.floor(fh * scale));
    const dx    = Math.floor((cw - dw) / 2);
    const dy    = Math.floor((ch - dh) / 2);

    try { ctx.drawImage(frame, dx, dy, dw, dh); }
    finally { frame.close(); }
  }

  // ── WASM YUV rendering (WebGL) ────────────────────────────────────────────────

  _drawYUVFrame(msg) {
    if (!this.yuvRenderer || !this.glCanvas || this.destroyed) return;
    try {
      this.yuvRenderer.draw(msg.yuv, msg.pixW, msg.pixH, msg.ylen);
    } catch (err) {
      console.warn('[CanvasPlayer] YUV render error', err);
    }
    if (!this.started) { this.started = true; this._clearStartupTimer(); }
  }

  // ── Startup timer ─────────────────────────────────────────────────────────────

  _startStartupTimer() {
    if (!this.fallbackUrl || this.startupTimer || this.started || this.fallbackActive) return;
    this.startupTimer = setTimeout(() => {
      this.startupTimer = null;
      if (!this.started) this._fallback('startup timeout');
    }, this.startupTimeoutMs);
  }

  _clearStartupTimer() {
    if (this.startupTimer) { clearTimeout(this.startupTimer); this.startupTimer = null; }
  }

  // ── MJPEG fallback ────────────────────────────────────────────────────────────

  _fallback(reason) {
    if (!this.fallbackUrl || this.fallbackActive || this.destroyed) return;
    this.fallbackActive = true;
    console.warn('[CanvasPlayer] falling back to MJPEG:', reason);
    this._clearStartupTimer();
    this._teardownWasm();
    if (this.ws) { this.ws.close(); this.ws = null; }
    if (this.decoder) { try { this.decoder.close(); } catch (_) {} this.decoder = null; }

    const parent = this.video && this.video.parentElement;
    if (!parent) return;

    if (reason === 'h265_not_supported') {
      const banner = document.createElement('div');
      banner.className  = 'h265-banner';
      banner.style.cssText = 'position:absolute;bottom:8px;left:8px;right:8px;z-index:3;' +
        'background:rgba(0,0,0,0.75);color:#f59e0b;font-size:0.72rem;padding:6px 10px;' +
        'border-radius:6px;text-align:center;pointer-events:none';
      banner.textContent = 'H.265 \u2014 hardware decode unavailable; falling back to MJPEG. For full quality, use the RTSP URL in VLC.';
      parent.style.position = parent.style.position || 'relative';
      parent.appendChild(banner);
    }

    if (!this.fallbackImg) {
      const img        = document.createElement('img');
      img.src          = `${this.fallbackUrl}?_=${Date.now()}`;
      img.alt          = this.video.alt || '';
      img.style.cssText = 'position:absolute;inset:0;width:100%;height:100%;object-fit:contain;display:block;background:#000;z-index:1';
      this.fallbackImg = img;
      parent.style.position = parent.style.position || 'relative';
      parent.appendChild(img);
    }

    this.video.style.display = 'none';
    if (this.canvas)   this.canvas.style.display   = 'none';
    if (this.glCanvas) this.glCanvas.style.display  = 'none';
  }

  // ── Cleanup ───────────────────────────────────────────────────────────────────

  destroy() {
    this.destroyed = true;
    this._clearStartupTimer();
    this._teardownWasm();
    if (this.rafId)       { cancelAnimationFrame(this.rafId); this.rafId = null; }
    if (this.pendingFrame){ this.pendingFrame.close(); this.pendingFrame = null; }
    if (this.ws)          { this.ws.close(); this.ws = null; }
    if (this.decoder)     { try { this.decoder.close(); } catch (_) {} this.decoder = null; }
    window.removeEventListener('resize', this._onResize);
    if (this.fallbackImg) { this.fallbackImg.remove(); this.fallbackImg = null; }
    if (this.glCanvas)    { this.glCanvas.remove(); this.glCanvas = null; this.yuvRenderer = null; }
    if (this.canvas)      { this.canvas.remove(); this.canvas = null; this.ctx = null; }
    if (this.video)       {
      this.video.style.display = '';
      this.video.removeAttribute('src');
      try { this.video.load(); } catch (_) {}
    }
  }
}

// Keep legacy export name so existing dashboard wiring keeps working.
window.MSEPlayer    = CanvasPlayer;
window.CanvasPlayer = CanvasPlayer;

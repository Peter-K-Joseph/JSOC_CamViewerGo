'use strict';

// Stub the camera firmware's debug logger that h265Decoder.js references.
self.debug = { log: () => {}, info: () => {}, warn: () => {}, error: () => {} };

// Pre-allocate enough output buffer for up to 4K (3840×2160).
// The WASM decoder writes decoded YUV into this fixed allocation; any camera
// up to 4K will fit without needing a runtime resize.
const MAX_PIX = 3840 * 2160;

let decoder  = null;
let primed   = false; // first decode() call is discarded (decoder warm-up)

self.onmessage = (e) => {
  const msg = e.data;

  switch (msg.type) {

    // ── init ──────────────────────────────────────────────────────────────────
    // msg: { type, wasmUrl, decoderUrl }
    //
    // Load ffmpegasm.js (Emscripten Module) then h265Decoder.js (thin wrapper),
    // both served through our same-origin Go proxy.
    case 'init': {
      try {
        importScripts(msg.wasmUrl, msg.decoderUrl);
        decoder = H265Decoder();
        // setOutputSize pre-allocates the YUV output buffer; guard in case the
        // camera firmware version doesn't expose this method.
        if (typeof decoder.setOutputSize === 'function') {
          decoder.setOutputSize(MAX_PIX);
        }
        self.postMessage({ type: 'ready' });
      } catch (err) {
        self.postMessage({ type: 'error', message: String(err) });
        // Log to worker console for easier debugging in browser devtools.
        console.error('[h265-worker] init failed:', err);
      }
      break;
    }

    // ── frame ─────────────────────────────────────────────────────────────────
    // msg: { type, annexb: ArrayBuffer }
    //
    // Decode one Annex-B H.265 access unit.  On success posts back:
    //   { type:'frame', yuv: ArrayBuffer, pixW, pixH, ylen }
    // where yuv holds YUV420p: [Y: ylen bytes][U: ylen/4 bytes][V: ylen/4 bytes]
    case 'frame': {
      if (!decoder) return;
      try {
        const input  = new Uint8Array(msg.annexb);
        const result = decoder.decode(input);

        // First call always returns {firstFrame:true} — the decoder needs one
        // priming pass before it produces pixel data.
        if (!result || result.firstFrame) {
          primed = false;
          return;
        }

        // result.width  = Y-plane byte count  (= pixW × pixH)
        // result.height = pixel height
        const ylen = result.width;
        const pixH = result.height;
        if (ylen <= 0 || pixH <= 0) return;
        const pixW = Math.round(ylen / pixH);

        // result.data is a Uint8Array already copied from the WASM heap by the
        // decoder wrapper — safe to slice and transfer without an extra malloc.
        // YUV420p layout: Y (ylen) + U (ylen/4) + V (ylen/4) = 1.5×ylen bytes.
        const yuvLen = ylen + (ylen >> 1);
        const yuv    = result.data.slice(0, yuvLen); // own ArrayBuffer

        self.postMessage(
          { type: 'frame', yuv: yuv.buffer, pixW, pixH, ylen },
          [yuv.buffer],  // transfer — zero copy across thread boundary
        );
      } catch (_) { /* skip corrupt frames */ }
      break;
    }

    default:
      break;
  }
};

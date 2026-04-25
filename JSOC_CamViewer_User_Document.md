# JSOC CamViewer — User Document

---

## 1. Brief Description

**JSOC CamViewer** is a lightweight, self-contained Network Video Recorder (NVR) application built in Go. It aggregates live video feeds from multiple IP cameras onto a single web-based dashboard, accessible from any browser on your local network — with no cloud dependency and no subscription required.

### What It Does

| Capability | Details |
|---|---|
| **Multi-camera dashboard** | View 1, 4, 9, or 16 cameras simultaneously in a responsive grid |
| **Live video streaming** | Low-latency playback using browser-native WebCodecs (H.264 / H.265) |
| **Camera management** | Add, remove, and configure cameras; store credentials securely |
| **Auto-discovery** | Scan the local network for ONVIF/Dahua cameras automatically |
| **PTZ control** | Pan, tilt, zoom, and focus cameras directly from the dashboard |
| **Stream health monitoring** | Real-time FPS, bitrate, keyframe, and connectivity diagnostics |
| **Protocol resilience** | Automatically falls back between WebSocket and RTSP if a stream fails |
| **RTSP re-streaming** | Expose any ingested camera as an RTSP feed for VLC, FFmpeg, etc. |
| **Admin authentication** | Single password protects the entire interface with session cookies |
| **Cross-platform** | Runs on macOS, Windows, and Linux; optional OS-level autostart |

### Key Design Principles

- **Minimal footprint** — single ~14 MB binary, three external Go dependencies, no database
- **File-based persistence** — cameras and settings stored in plain JSON files
- **Browser-native decoding** — no browser plugins; video decoded via the WebCodecs API on a Canvas element
- **Concurrent, non-blocking** — each camera runs in its own goroutine; slow viewers never stall the source

---

## 2. Technical Architecture

### 2.1 Technology Stack

| Layer | Technology |
|---|---|
| **Backend language** | Go 1.24 |
| **HTTP router** | `chi/v5` |
| **WebSocket** | `gorilla/websocket` |
| **Frontend** | Vanilla JavaScript, HTML5, CSS3 |
| **Video decoding** | Browser WebCodecs API |
| **Video rendering** | HTML5 Canvas (`requestAnimationFrame` at 60 fps) |
| **Persistence** | JSON files (`cameras.json`, `settings.json`) |
| **Logging** | Dual-write to stdout and `logs/system.log` / `logs/system.error` |

### 2.2 Component Map

```
┌─────────────────────────────────────────────────────────────────────┐
│                         JSOC CamViewer                              │
│                                                                     │
│  ┌──────────────┐   ┌──────────────┐   ┌──────────────────────┐   │
│  │  Store       │   │  Settings    │   │  Config              │   │
│  │ cameras.json │   │settings.json │   │ (env variables)      │   │
│  └──────┬───────┘   └──────┬───────┘   └──────────┬───────────┘   │
│         │                  │                       │               │
│  ┌──────▼───────────────────▼───────────────────────▼───────────┐  │
│  │                    main.go (startup)                          │  │
│  │  Load config → resolve password → init managers → bind ports  │  │
│  └──────────────────────────┬────────────────────────────────────┘  │
│                             │                                       │
│         ┌───────────────────┼───────────────────┐                  │
│         │                   │                   │                  │
│  ┌──────▼───────┐   ┌───────▼──────┐   ┌───────▼──────┐          │
│  │  Streaming   │   │  Web Server  │   │  RTSP Server │          │
│  │  Manager     │   │  (port 8080) │   │  (port 8554) │          │
│  └──────┬───────┘   └───────┬──────┘   └───────┬──────┘          │
│         │                   │                   │                  │
│  ┌──────▼───────┐           │           ┌───────▼──────┐          │
│  │  Per-Camera  │    REST API + WS       │  RTP/RTSP    │          │
│  │  Goroutines  │    HTML pages          │  sessions    │          │
│  └──────┬───────┘           │           └──────────────┘          │
│         │                   │                                      │
│  ┌──────▼───────┐   ┌───────▼──────┐                              │
│  │    Track     │   │  Auth / PTZ  │                              │
│  │  (fan-out    │   │  Manager     │                              │
│  │   hub)       │   └──────────────┘                              │
│  └──────────────┘                                                   │
└─────────────────────────────────────────────────────────────────────┘
```

### 2.3 Streaming Pipeline — Flow Diagrams

#### Pipeline A: Default (Server-Side fMP4 Transcoding)

This is the primary path. The backend pulls video from the camera, packages it into Fragmented MP4, and delivers it to the browser over WebSocket.

```
  IP CAMERA                   JSOC BACKEND                    WEB BROWSER
  ─────────                   ────────────                    ───────────

  RTSP/TCP  ──────────────►  RtspSource.go
     or                      (reads RTP packets,
  WebSocket ──────────────►  WsSource.go         ──────────►  /ws/stream/{key}
                              assembles NAL units)              │
                                    │                           │  WebSocket frames:
                                    ▼                           │  1. JSON: codec + MIME
                              Track (fan-out hub)               │  2. Binary: ftyp+moov
                              - SPS/PPS/VPS cache               │     (init segment)
                              - 32-frame ring buffer            │  3. Binary: moof+mdat
                              - FPS/bitrate metrics             │     (per frame)
                                    │                           │
                              fmp4.go muxer                     ▼
                              (builds MP4 boxes)            MSEPlayer.js
                                                            - feeds SourceBuffer
                                                            - tracks decode queue

                                                                │
                                                                ▼
                                                        WebCodecs VideoDecoder
                                                        (H.264 / H.265)
                                                                │
                                                                ▼
                                                        Canvas.drawImage()
                                                        @ 60 fps (rAF loop)
```

#### Pipeline B: Direct MJPEG Proxy

Used when **Direct Stream Mode** is enabled. The backend acts as a thin HTTP proxy; the browser displays a raw MJPEG stream.

```
  IP CAMERA                   JSOC BACKEND                    WEB BROWSER
  ─────────                   ────────────                    ───────────

  MJPEG/HTTP ────────────►  proxy.go                ───────►  <img> tag
                             (chunked transfer)               (multipart/x-mixed-replace)
```

#### Pipeline C: RTSP Re-Streaming

Any ingested stream can be consumed by a third-party RTSP client (VLC, FFmpeg, NVR software).

```
  IP CAMERA        JSOC BACKEND                   RTSP CLIENT (VLC / FFmpeg)
  ─────────        ────────────                   ──────────────────────────

  RTSP/WS  ──►  Track (in memory)  ◄──  RTSP session.go
                                         │ DESCRIBE → SDP
                                         │ SETUP    → ports
                                         │ PLAY     → RTP/UDP or interleaved
                                         └──────────────────────────────────►  video
```

### 2.4 Protocol Fallback Flow

When a camera's primary protocol fails (e.g., auth rejected or connection dropped), the manager automatically tries the alternate protocol before surfacing an error state.

```
  Start camera stream
        │
        ▼
  Try primary protocol
  (WS or RTSP per config)
        │
        ├─── Success ──────────────────────────────► Streaming OK
        │
        └─── Failure (auth / timeout / disconnect)
                    │
                    ▼
             fallback enabled?
                    │
             Yes ───┴─── No ──────────────────────► Health: offline
                    │
                    ▼
             Try alternate protocol
             (RTSP if WS failed, WS if RTSP failed)
                    │
                    ├─── Success ─────────────────► Streaming OK
                    │                               (FallbackActive = true)
                    │
                    └─── Failure ─────────────────► Health: offline / auth-failed
```

### 2.5 Authentication Flow

#### App Login

```
  Browser                    JSOC Web Server
  ───────                    ───────────────

  GET /ui/login  ──────────► Serve login page
  POST /ui/login ──────────► Compare password (constant-time)
        │                          │
        │                    Match? Yes ──► Create session token (UUID)
        │                                  Set-Cookie: session=<token>
        │                                  Redirect → /
        │                    Match? No  ──► Re-render login (401)
        │
  Subsequent requests
  Cookie: session=<token> ──► Validate token (TTL 24h)
                               │
                         Valid ┴ Invalid → redirect /ui/login
```

#### Camera Credential Validation

```
  JSOC Backend
  ────────────

  Dahua camera?
        │
   Yes ─┴─ No ─────────────────────────────► RTSP DESCRIBE (Digest auth)
        │
        ▼
  Dahua RPC2 HTTP login
  (returns session cookie)
        │
        ├── Success ──► Store cookie, open /rtspoverwebsocket
        │
        └── Failure ──► AuthRejectedError → trigger fallback / mark auth-failed
```

### 2.6 Directory Structure

```
JSOC_CamViewerGo/
├── cmd/jsoc/main.go              # Entry point: config, logging, server startup
├── go.mod / go.sum               # Module definition (3 external deps)
│
├── static/                       # Frontend assets (served as-is)
│   ├── dashboard.js              # Grid UI, modal viewer, PTZ controls
│   ├── player.js                 # CanvasPlayer: WebCodecs + Canvas renderer
│   ├── health.js                 # Health dashboard: polling + charts
│   ├── settings.js               # Preferences panel
│   ├── discover.js               # Discovery UI
│   ├── login.js                  # Login form
│   └── app.css                   # Stylesheet
│
└── internal/
    ├── web/                      # HTTP layer
    │   ├── server.go             # Router, middleware, template registration
    │   ├── handlers.go           # Camera CRUD, discovery, page handlers
    │   ├── handlers_settings.go  # Settings, health, password APIs
    │   ├── ws_stream.go          # WebSocket stream endpoints
    │   ├── auth.go               # Session management (login/logout/GC)
    │   ├── proxy.go              # MJPEG proxy
    │   └── templates.go          # Embedded HTML templates
    │
    ├── streaming/                # Ingest & fan-out
    │   ├── manager.go            # Per-camera goroutine lifecycle + fallback logic
    │   ├── track.go              # Thread-safe fan-out hub (subscribe/publish)
    │   ├── rtsp_source.go        # RTSP/TCP camera client
    │   ├── ws_source.go          # WebSocket camera client (Dahua)
    │   └── rtp.go                # RTP packet parsing
    │
    ├── rtsp/                     # Outbound RTSP server
    │   ├── server.go             # TCP listener, session dispatch
    │   ├── session.go            # Per-client DESCRIBE/SETUP/PLAY handler
    │   └── sdp.go                # SDP generation
    │
    ├── mux/                      # fMP4 encoding
    │   ├── fmp4.go               # Init segment + media segment builder
    │   └── box.go                # MP4 box primitives
    │
    ├── store/store.go            # cameras.json persistence (RWMutex)
    ├── settings/settings.go      # settings.json persistence
    ├── config/config.go          # Environment variable parsing
    ├── auth/                     # Camera authentication
    │   ├── rtsp.go               # RTSP Digest auth
    │   └── rpc2.go               # Dahua RPC2 HTTP auth
    ├── ptz/                      # PTZ control
    │   ├── manager.go            # PTZ client registry
    │   └── client.go             # ONVIF SOAP client
    ├── discovery/discovery.go    # WS-Discovery UDP multicast
    ├── netutil/                  # IP/port helpers, auto-port-fallback
    └── autostart/                # OS autostart (macOS plist, Windows registry, systemd)
```

### 2.7 Configuration Reference

JSOC is configured via environment variables at startup. All settings have sensible defaults.

| Variable | Default | Description |
|---|---|---|
| `JSOC_DATA_DIR` | `~/.jsoc_camviewer` | Directory for JSON data files and logs |
| `JSOC_HOST` | `0.0.0.0` | HTTP server bind address |
| `JSOC_PORT` | `8080` | HTTP server port |
| `JSOC_RTSP_HOST` | `0.0.0.0` | Advertised RTSP server address |
| `JSOC_RTSP_PORT` | `8554` | RTSP server port |
| `JSOC_STREAM_PREFIX` | `cam` | RTSP path prefix (`rtsp://host/cam/<id>`) |
| `JSOC_WS_KEEPALIVE_S` | `15.0` | WebSocket keepalive ping interval (seconds) |
| `JSOC_PASSWORD` | *(random)* | Admin password; if unset, generated and printed at startup |

### 2.8 API Quick Reference

| Method | Endpoint | Purpose |
|---|---|---|
| `GET` | `/` | Dashboard (grid view) |
| `GET` | `/health` | Stream health monitoring |
| `GET` | `/discover` | Camera discovery |
| `GET` | `/preferences` | User settings |
| `POST` | `/ui/login` | Authenticate |
| `GET` | `/api/cameras` | List cameras |
| `POST` | `/api/cameras` | Add camera |
| `DELETE` | `/api/cameras/{id}` | Remove camera |
| `POST` | `/api/cameras/{id}/login` | Validate credentials & start stream |
| `POST` | `/api/cameras/{id}/restart` | Restart a camera stream |
| `POST` | `/api/cameras/{id}/ptz` | Send PTZ command |
| `GET` | `/api/health` | Stream diagnostics (JSON) |
| `POST` | `/api/discover` | Scan network for cameras |
| `GET` | `/ws/stream/{streamKey}` | fMP4 WebSocket stream |
| `GET` | `/proxy/cameras/{id}/stream` | MJPEG proxy stream |

---

*Document generated: 2026-04-25*

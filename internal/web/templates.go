package web

// ── App login (standalone — no sidebar, shown before auth) ───────────────────
const appLoginTmpl = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width,initial-scale=1">
  <title>JSOC NVR — Login</title>
  <script>(function(){function a(t){var p=(t==='light'||t==='dark'||t==='system')?t:'system';var v=p==='system'?(window.matchMedia&&window.matchMedia('(prefers-color-scheme: light)').matches?'light':'dark'):p;document.documentElement.setAttribute('data-theme',v);document.documentElement.style.colorScheme=v;return p;}var pref='system';try{pref=localStorage.getItem('jsoc-theme')||'system';}catch(e){}a(pref);window.setTheme=function(t){var p=a(t);try{localStorage.setItem('jsoc-theme',p);}catch(e){}};})()</script>
  <link rel="stylesheet" href="/static/app.css?v=20260425d">
</head>
<body style="display:flex;align-items:center;justify-content:center;height:100vh;background:var(--bg)">
<div class="login-card" style="width:320px">
  <div>
    <div style="font-size:1rem;font-weight:700;color:var(--accent3);letter-spacing:0.06em;text-transform:uppercase">JSOC NVR</div>
    <div style="font-size:0.75rem;color:var(--text3);margin-top:0.2rem">Camera Viewer</div>
  </div>
  <form method="POST" action="/ui/login">
    <input type="hidden" name="next" value="{{.Next}}">
    <div class="login-field">
      <label>Password</label>
      <input type="password" name="password" autofocus autocomplete="current-password">
    </div>
    {{if .Error}}<div class="login-error">{{.Error}}</div>{{end}}
    <button type="submit" class="btn" style="width:100%;margin-top:0.25rem">Sign in</button>
  </form>
</div>
</body>
</html>`

// ── Direct camera page (standalone — fullscreen, no sidebar) ─────────────────
const directTmpl = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width,initial-scale=1">
  <title>{{.Camera.Name}}</title>
  <style>
    * { margin: 0; padding: 0; box-sizing: border-box; }
    html, body { width: 100%; height: 100%; overflow: hidden; background: #000; }
    #stream { width: 100%; height: 100%; object-fit: contain; display: block; }
    #dot {
      position: fixed; top: 14px; right: 14px;
      width: 12px; height: 12px; border-radius: 50%;
      background: #22c55e; box-shadow: 0 0 8px #22c55e;
      transition: background .3s, box-shadow .3s;
    }
    #dot.offline { background: #ef4444; box-shadow: 0 0 8px #ef4444; }
    #dot.warn    { background: #f59e0b; box-shadow: 0 0 8px #f59e0b; }
    #label {
      position: fixed; top: 10px; left: 14px;
      color: rgba(255,255,255,.65); font-size: .8rem;
      font-family: system-ui, sans-serif; pointer-events: none;
    }
  </style>
</head>
<body>
  <div id="label">{{.Camera.Name}} — {{.Camera.IP}}</div>
  <div id="dot"></div>
  <img id="stream" src="/proxy/cameras/{{.Camera.ID}}/stream" alt="{{.Camera.Name}}">

  <script>
    const dot = document.getElementById('dot');
    const stream = document.getElementById('stream');
    const camID = "{{.Camera.ID}}";

    // Request fullscreen on first user interaction (browsers require gesture).
    let fsRequested = false;
    document.addEventListener('click', () => {
      if (!fsRequested) { fsRequested = true; document.documentElement.requestFullscreen && document.documentElement.requestFullscreen().catch(()=>{}); }
    }, { once: true });
    // Auto-attempt on load (works in some browsers / Electron).
    document.documentElement.requestFullscreen && document.documentElement.requestFullscreen().catch(()=>{});

    // ── Stream error / reload handling ────────────────────────────────────────
    let reloadTimer = null;
    stream.addEventListener('error', () => {
      dot.className = 'offline';
      if (!reloadTimer) {
        reloadTimer = setTimeout(() => {
          reloadTimer = null;
          stream.src = '/proxy/cameras/' + camID + '/stream?' + Date.now();
        }, 3000);
      }
    });
    stream.addEventListener('load', () => { dot.className = ''; });

    // ── Health polling ────────────────────────────────────────────────────────
    async function poll() {
      try {
        const r = await fetch('/api/cameras/' + camID);
        if (!r.ok) { dot.className = 'offline'; return; }
        const d = await r.json();
        if (d.health === 'ok')      dot.className = '';
        else if (d.health === 'unknown') dot.className = 'warn';
        else                        dot.className = 'offline';
      } catch { dot.className = 'offline'; }
    }
    setInterval(poll, 5000);
    poll();
  </script>
</body>
</html>`

// ── Base shell ────────────────────────────────────────────────────────────────
const baseTmpl = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width,initial-scale=1">
  <title>JSOC NVR</title>
  <script>(function(){function a(t){var p=(t==='light'||t==='dark'||t==='system')?t:'system';var v=p==='system'?(window.matchMedia&&window.matchMedia('(prefers-color-scheme: light)').matches?'light':'dark'):p;document.documentElement.setAttribute('data-theme',v);document.documentElement.style.colorScheme=v;return p;}var pref='system';try{pref=localStorage.getItem('jsoc-theme')||'system';}catch(e){}a(pref);window.setTheme=function(t){var p=a(t);try{localStorage.setItem('jsoc-theme',p);}catch(e){}};})()</script>
  <link rel="stylesheet" href="/static/app.css?v=20260425d">
</head>
<body>
<aside class="sidebar">
  <div class="sidebar-logo">
    <div class="brand">JSOC NVR</div>
    <div class="sub">Camera Viewer</div>
  </div>
  <nav class="sidebar-nav">
    <a href="/"            class="nav-item {{if eq .Page "dashboard"}}active{{end}}"><span class="nav-icon">⊞</span> Dashboard</a>
    <a href="/health"      class="nav-item {{if eq .Page "health"}}active{{end}}"><span class="nav-icon">♡</span> Stream Health</a>
    <a href="/discover"    class="nav-item {{if eq .Page "discover"}}active{{end}}"><span class="nav-icon">⌖</span> Discover</a>
    <a href="/config"      class="nav-item {{if eq .Page "config"}}active{{end}}"><span class="nav-icon">⚙</span> Configuration</a>
    <a href="/preferences" class="nav-item {{if eq .Page "preferences"}}active{{end}}"><span class="nav-icon">⊛</span> Preferences</a>
  </nav>
  <form method="POST" action="/ui/logout" style="padding:0.4rem 0.6rem;border-top:1px solid var(--border)">
    <button type="submit" class="btn btn-ghost" style="width:100%;font-size:0.72rem;justify-content:center">Sign out</button>
  </form>
  <div class="sidebar-footer">JSOC CamViewerGo</div>
</aside>
<div class="main">
  {{block "content" .}}{{end}}
</div>
{{block "scripts" .}}{{end}}
</body>
</html>`

// ── Dashboard ─────────────────────────────────────────────────────────────────
const dashboardTmpl = `{{define "content"}}
<div class="topbar">
  <span class="topbar-title">Live View</span>
  {{if .DirectMode}}<span class="pill pill-warn" style="margin-left:0.5rem">Direct Stream</span>{{end}}
  <div class="grid-controls">
    <button class="grid-btn active" onclick="setGrid(1)" id="btn-1x1" title="1×1">1×1</button>
    <button class="grid-btn"       onclick="setGrid(2)" id="btn-2x2" title="2×2">2×2</button>
    <button class="grid-btn"       onclick="setGrid(3)" id="btn-3x3" title="3×3">3×3</button>
    <button class="grid-btn"       onclick="setGrid(4)" id="btn-4x4" title="4×4">4×4</button>
    <div class="sep"></div>
    <button class="grid-btn active" onclick="setMode('grid')"  id="btn-grid"  title="Grid view">⊞ Grid</button>
    <button class="grid-btn"        onclick="setMode('table')" id="btn-table" title="Table view">☰ Table</button>
  </div>
</div>

{{/* ── Grid view ── */}}
<div id="view-grid" class="grid-container">
  <div class="camera-grid grid-2x2" id="camera-grid">
    {{range $i, $cam := .Cameras}}
    <div class="cam-cell" id="cell-{{$cam.ID}}"
         data-id="{{$cam.ID}}"
         data-key="{{$cam.StreamKey}}"
         data-health="{{$cam.Health}}"
         data-creds="{{$cam.HasCredentials}}"
         {{if not $.DirectWindowed}}
           onclick="openModal('{{$cam.ID}}','{{$cam.Name}}','{{$cam.IP}}','{{$cam.StreamKey}}','{{$cam.Health}}','{{$cam.StreamRTSPURL}}',{{$cam.HasPTZ}})"
         {{end}}>
      <span class="cam-channel">CH {{inc $i}}</span>
      {{if $cam.HasCredentials}}
        {{if $.DirectMode}}
          <img id="img-{{$cam.ID}}" class="cam-mjpeg"
               src="/proxy/cameras/{{$cam.ID}}/stream"
               alt="{{$cam.Name}}" onerror="this.dataset.err='1'">
        {{else}}
          <video id="video-{{$cam.ID}}" autoplay muted playsinline></video>
        {{end}}
      {{else}}
        <div class="cam-placeholder">
          <div class="cam-icon">📷</div>
          <div>Not configured</div>
        </div>
      {{end}}
      <div class="cam-overlay">
        <span class="cam-name">{{$cam.Name}}</span>
        <span class="status-dot dot-{{$cam.Health}}"></span>
      </div>
      {{/* "Open failed" banner — injected by JS when popup is blocked */}}
    </div>
    {{end}}
    {{/* Empty cells to fill grid */}}
    {{range (emptySlots (len .Cameras))}}
    <div class="cam-cell empty">
      <div class="cam-placeholder">
        <div class="cam-icon" style="opacity:0.08">📷</div>
      </div>
    </div>
    {{end}}
  </div>
</div>

{{/* ── Table view ── */}}
<div id="view-table" class="table-container hidden">
  <table class="nvr-table">
    <thead>
      <tr>
        <th>#</th>
        <th>Name</th>
        <th>IP</th>
        <th>Status</th>
        <th>Stream Key</th>
        <th>RTSP URL</th>
        <th>Actions</th>
      </tr>
    </thead>
    <tbody>
      {{range $i, $cam := .Cameras}}
      <tr>
        <td class="text-muted">{{inc $i}}</td>
        <td>{{$cam.Name}}</td>
        <td class="mono">{{$cam.IP}}:{{$cam.Port}}</td>
        <td><span class="pill pill-{{$cam.Health}}">{{$cam.Health}}</span></td>
        <td class="mono text-sm">{{$cam.StreamKey}}</td>
        <td class="mono text-sm" style="max-width:200px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap">
          {{if $cam.StreamRTSPURL}}{{$cam.StreamRTSPURL}}{{else}}—{{end}}
        </td>
        <td>
          {{if $cam.HasCredentials}}
            {{if $.DirectWindowed}}
              <button class="btn btn-sm" onclick="openDirectWindow('{{$cam.ID}}')">Open</button>
            {{else}}
              <button class="btn btn-sm" onclick="openModal('{{$cam.ID}}','{{$cam.Name}}','{{$cam.IP}}','{{$cam.StreamKey}}','{{$cam.Health}}','{{$cam.StreamRTSPURL}}',{{$cam.HasPTZ}})">View</button>
            {{end}}
          {{else}}
            <a href="/cameras/{{$cam.ID}}/login" class="btn btn-warn btn-sm">Login</a>
          {{end}}
        </td>
      </tr>
      {{else}}
      <tr><td colspan="7" class="text-muted" style="text-align:center;padding:2rem">
        No cameras. <a href="/discover" style="color:var(--accent2)">Discover</a> or <a href="/config" style="color:var(--accent2)">add manually</a>.
      </td></tr>
      {{end}}
    </tbody>
  </table>
</div>

{{/* ── Fullscreen modal (hidden in direct-windowed mode) ── */}}
{{if not .DirectWindowed}}
<div id="modal" class="modal-overlay hidden">
  <div class="modal-header">
    <span id="modal-title" class="modal-title"></span>
    <span id="modal-meta"  class="modal-meta"></span>
    <button class="modal-close" onclick="closeModal()">✕</button>
  </div>
  <div class="modal-body">
    {{if .DirectMode}}
      <img id="modal-img" style="width:100%;height:100%;object-fit:contain" alt="">
    {{else}}
      <video id="modal-video" autoplay muted playsinline controls></video>
    {{end}}
  </div>

  {{/* ── PTZ overlay panel ── */}}
  <div id="ptz-panel" class="ptz-panel hidden">
    <div class="ptz-section">
      <div class="ptz-dpad">
        <button class="ptz-btn ptz-tl" data-pan="-0.5" data-tilt="0.5"  data-zoom="0">↖</button>
        <button class="ptz-btn ptz-t"  data-pan="0"    data-tilt="0.5"  data-zoom="0">↑</button>
        <button class="ptz-btn ptz-tr" data-pan="0.5"  data-tilt="0.5"  data-zoom="0">↗</button>
        <button class="ptz-btn ptz-l"  data-pan="-0.5" data-tilt="0"    data-zoom="0">←</button>
        <button class="ptz-btn ptz-stop" onclick="ptzStop()">■</button>
        <button class="ptz-btn ptz-r"  data-pan="0.5"  data-tilt="0"    data-zoom="0">→</button>
        <button class="ptz-btn ptz-bl" data-pan="-0.5" data-tilt="-0.5" data-zoom="0">↙</button>
        <button class="ptz-btn ptz-b"  data-pan="0"    data-tilt="-0.5" data-zoom="0">↓</button>
        <button class="ptz-btn ptz-br" data-pan="0.5"  data-tilt="-0.5" data-zoom="0">↘</button>
      </div>
    </div>
    <div class="ptz-section ptz-side">
      <div class="ptz-group">
        <div class="ptz-label">Zoom</div>
        <button class="ptz-btn" data-pan="0" data-tilt="0" data-zoom="0.5">＋</button>
        <button class="ptz-btn" data-pan="0" data-tilt="0" data-zoom="-0.5">－</button>
      </div>
      <div class="ptz-group">
        <div class="ptz-label">Focus</div>
        <button class="ptz-btn ptz-focus" data-speed="0.5">Far</button>
        <button class="ptz-btn ptz-focus" data-speed="-0.5">Near</button>
        <button class="ptz-btn ptz-focus-auto" onclick="ptzFocusAuto()">Auto</button>
      </div>
    </div>
  </div>

  <div class="modal-footer">
    <span>RTSP:</span>
    <code id="modal-rtsp">—</code>
    <button class="btn btn-ghost btn-sm" onclick="copyRTSP()">Copy</button>
    <span style="flex:1"></span>
    <button id="ptz-toggle-btn" class="btn btn-ghost btn-sm hidden" onclick="togglePTZ()">⊕ PTZ</button>
    <span class="text-muted text-sm">Press <kbd>Esc</kbd> to close</span>
    <button class="btn btn-danger btn-sm" onclick="deleteModalCam()">Remove Camera</button>
  </div>
</div>
{{end}}
{{end}}

{{define "scripts"}}
<script src="/static/player.js?v=20260425d"></script>
<script src="/static/dashboard.js?v=20260425d"></script>
<script>
const DIRECT_MODE     = {{.DirectMode}};
const DIRECT_WINDOWED = {{.DirectWindowed}};
const CAMERAS = [
  {{range .Cameras}}
  {id:"{{.ID}}",name:"{{.Name}}",ip:"{{.IP}}",key:"{{.StreamKey}}",health:"{{.Health}}",hasCreds:{{.HasCredentials}},hasPTZ:{{.HasPTZ}},rtsp:"{{.StreamRTSPURL}}"},
  {{end}}
];
initDashboard(CAMERAS);
</script>
{{end}}`

// ── Discover ──────────────────────────────────────────────────────────────────
const discoverTmpl = `{{define "content"}}
<div class="topbar">
  <span class="topbar-title">Discover Cameras</span>
  <button class="btn" id="scan-btn" onclick="scan()">⌖ Scan LAN</button>
</div>
<div class="page-content">
  <div class="scan-status" id="scan-status">Click "Scan LAN" to discover ONVIF cameras on your network.</div>

  <table class="nvr-table hidden" id="results-table">
    <thead>
      <tr><th>IP</th><th>Port</th><th>Manufacturer</th><th>Model</th><th>Action</th></tr>
    </thead>
    <tbody id="results-body"></tbody>
  </table>
</div>
{{end}}

{{define "scripts"}}
<script src="/static/discover.js?v=20260425d"></script>
{{end}}`

// ── Config ────────────────────────────────────────────────────────────────────
const configTmpl = `{{define "content"}}
<div class="topbar">
  <span class="topbar-title">Configuration</span>
  <button class="btn" onclick="showAddForm()">+ Add Camera</button>
</div>
<div class="page-content">

  {{/* Add form */}}
  <div id="add-form" class="hidden" style="background:var(--surface);border:1px solid var(--border);border-radius:8px;padding:1rem;margin-bottom:1.25rem">
    <div style="font-size:0.85rem;font-weight:600;margin-bottom:0.75rem;color:var(--text)">Add Camera Manually</div>
    <div class="form-row">
      <input type="text"   id="add-name" placeholder="Camera name" style="flex:2;min-width:140px">
      <input type="text"   id="add-ip"   placeholder="IP address"  style="flex:2;min-width:130px">
      <input type="number" id="add-port" placeholder="Port (80)"   style="flex:1;min-width:80px">
      <button class="btn"       onclick="addCamera()">Add</button>
      <button class="btn btn-ghost" onclick="hideAddForm()">Cancel</button>
    </div>
  </div>

  {{/* Camera table */}}
  <table class="nvr-table">
    <thead>
      <tr>
        <th>#</th><th>Name</th><th>IP / Port</th><th>Credentials</th><th>Status</th><th>Stream Key</th><th>Actions</th>
      </tr>
    </thead>
    <tbody id="cam-tbody">
      {{range $i, $cam := .Cameras}}
      <tr id="row-{{$cam.ID}}">
        <td class="text-muted">{{inc $i}}</td>
        <td>{{$cam.Name}}</td>
        <td class="mono">{{$cam.IP}}:{{$cam.Port}}</td>
        <td>{{if $cam.HasCredentials}}<span style="color:var(--ok)">✓ {{.Username}}</span>{{else}}<span class="text-muted">—</span>{{end}}</td>
        <td><span class="pill pill-{{$cam.Health}}">{{$cam.Health}}</span></td>
        <td class="mono text-sm">{{$cam.StreamKey}}</td>
        <td style="display:flex;gap:0.35rem;flex-wrap:wrap">
          <a href="/cameras/{{$cam.ID}}/login" class="btn btn-warn btn-sm">Login</a>
          <button class="btn btn-sm" onclick="restartCam('{{$cam.ID}}',this)">Restart</button>
          <button class="btn btn-danger btn-sm" onclick="deleteCam('{{$cam.ID}}')">Remove</button>
        </td>
      </tr>
      {{else}}
      <tr><td colspan="7" class="text-muted" style="text-align:center;padding:2rem">
        No cameras. <a href="/discover" style="color:var(--accent2)">Discover</a> cameras or add one manually.
      </td></tr>
      {{end}}
    </tbody>
  </table>
</div>
{{end}}

{{define "scripts"}}
<script src="/static/config.js?v=20260425d"></script>
{{end}}`

// ── Login ─────────────────────────────────────────────────────────────────────
const loginTmpl = `{{define "content"}}
<div class="login-wrap">
  <div class="login-card">
    <div>
      <h2>Camera Login</h2>
      <div class="cam-ip">{{.Camera.Name}} — {{.Camera.IP}}:{{.Camera.Port}}</div>
    </div>

    {{/* ── Stream credentials ── */}}
    <form id="login-form" onsubmit="doLogin(event)">
      <div class="login-field">
        <label>Username</label>
        <input type="text" id="username" value="admin" autocomplete="username">
      </div>
      <div class="login-field">
        <label>Password</label>
        <input type="password" id="password" autocomplete="current-password">
      </div>
      <div class="login-error" id="login-error"></div>
      <button type="submit" class="btn" style="width:100%">Login &amp; Start Stream</button>
    </form>

    {{/* ── ONVIF / PTZ (optional, shown after stream login succeeds) ── */}}
    <div id="onvif-section" class="hidden">
      <div class="login-divider">PTZ Control <span class="text-muted">(optional)</span></div>
      <div style="font-size:0.75rem;color:var(--text2);margin-bottom:0.6rem">
        Leave blank to try the same credentials as above.
      </div>
      <div class="login-field">
        <label>ONVIF Username</label>
        <input type="text" id="onvif-username" placeholder="Same as above" autocomplete="off">
      </div>
      <div class="login-field">
        <label>ONVIF Password</label>
        <input type="password" id="onvif-password" placeholder="Same as above" autocomplete="off">
      </div>
      <div class="login-error" id="onvif-error"></div>
      <div style="display:flex;gap:0.5rem">
        <button class="btn" style="flex:1" onclick="doONVIFLogin()">Enable PTZ</button>
        <button class="btn btn-ghost" onclick="skipPTZ()">Skip, stream only</button>
      </div>
    </div>
  </div>
</div>
{{end}}

{{define "scripts"}}
<script>
const CAMERA_ID = "{{.Camera.ID}}";
</script>
<script src="/static/login.js?v=20260425d"></script>
{{end}}`

// ── Viewer (standalone fullscreen) ────────────────────────────────────────────
const viewerTmpl = `{{define "content"}}
<div class="topbar">
  <span class="topbar-title">{{.Camera.Name}}</span>
  <span class="text-muted text-sm">{{.Camera.IP}}:{{.Camera.Port}}</span>
  <span class="pill pill-{{.Camera.Health}}" style="margin-left:0.5rem">{{.Camera.Health}}</span>
  <span style="flex:1"></span>
  <a href="/" class="btn btn-ghost btn-sm">← Dashboard</a>
</div>
<div style="flex:1;display:flex;flex-direction:column;background:#000;overflow:hidden">
  <video id="main-video" autoplay muted playsinline controls
         style="flex:1;width:100%;height:100%;object-fit:contain;background:#000"></video>
</div>
<div style="padding:0.5rem 1rem;background:var(--surface);border-top:1px solid var(--border);font-size:0.78rem;color:var(--text3);display:flex;gap:1rem;align-items:center">
  <span>RTSP:</span>
  <code style="color:var(--accent2);font-family:monospace">{{.RTSPURL}}</code>
  <button class="btn btn-ghost btn-sm" onclick="navigator.clipboard.writeText('{{.RTSPURL}}')">Copy</button>
</div>
{{end}}

{{define "scripts"}}
<script src="/static/player.js?v=20260425d"></script>
<script>
  const v = document.getElementById('main-video');
  const proto = location.protocol === 'https:' ? 'wss' : 'ws';
  const wsUrl = proto + '://' + location.host + '/ws/camera/{{.Camera.ID}}/annexb';
  new MSEPlayer(v, wsUrl, {
    fallbackUrl:  '/proxy/cameras/{{.Camera.ID}}/stream',
    wasmCameraId: '{{.Camera.ID}}',
  });
</script>
{{end}}`

// ── Preferences ───────────────────────────────────────────────────────────────
const preferencesTmpl = `{{define "content"}}
<div class="topbar">
  <span class="topbar-title">Preferences</span>
</div>
<div class="page-content">

  <div id="pref-saved" class="hidden" style="background:var(--ok-dim,rgba(34,197,94,.15));border:1px solid var(--ok);border-radius:6px;padding:0.5rem 0.9rem;margin-bottom:1rem;font-size:0.82rem;color:var(--ok)">
    ✓ Settings saved
  </div>
  <div id="pref-error" class="hidden" style="background:rgba(239,68,68,.12);border:1px solid #ef4444;border-radius:6px;padding:0.5rem 0.9rem;margin-bottom:1rem;font-size:0.82rem;color:#ef4444"></div>

  {{/* Main settings grid: 2 columns on desktop, 1 on mobile */}}
  <div class="pref-grid">
    {{/* Left column: Appearance + System */}}
    <div class="pref-column">
      {{/* ── Appearance ── */}}
      <div class="pref-section">
        <div class="pref-section-title">Appearance</div>
        <div class="pref-row">
          <div class="pref-label">
            <span>Theme</span>
            <span class="pref-desc">Choose dark, light, or follow your system setting.</span>
          </div>
          <select id="pref-theme">
            <option value="dark">Dark</option>
            <option value="system">System</option>
            <option value="light">Light</option>
          </select>
        </div>
      </div>

      {{/* ── System ── */}}
      <div class="pref-section">
        <div class="pref-section-title">System</div>

        <div class="pref-row">
          <div class="pref-label">
            <span>Auto-start on boot</span>
            <span class="pref-desc">Register JSOC NVR as a login/startup service (LaunchAgent on macOS, systemd on Linux, Registry on Windows).</span>
          </div>
          <label class="toggle">
            <input type="checkbox" id="pref-autostart" {{if .Settings.AutoStart}}checked{{end}}>
            <span class="toggle-slider"></span>
          </label>
        </div>

        <div class="pref-row">
          <div class="pref-label">
            <span>Advanced health monitoring</span>
            <span class="pref-desc">Enable the Stream Health page with per-camera diagnostics, protocol badges, and live error tracking. Disable to save resources.</span>
          </div>
          <label class="toggle">
            <input type="checkbox" id="pref-health" {{if .Settings.HealthMonitoring}}checked{{end}}>
            <span class="toggle-slider"></span>
          </label>
        </div>
      </div>
    </div>

    {{/* Right column: Streaming + Security */}}
    <div class="pref-column">
      {{/* ── Streaming ── */}}
      <div class="pref-section">
        <div class="pref-section-title">Streaming</div>

        <div class="pref-row">
          <div class="pref-label">
            <span>Direct stream mode</span>
            <span class="pref-desc">Bypass the server pipeline. Your browser fetches the camera MJPEG stream via a thin proxy. Lower latency; no fMP4/MSE transcoding.</span>
          </div>
          <label class="toggle">
            <input type="checkbox" id="pref-direct" {{if .Settings.DirectStreamMode}}checked{{end}}>
            <span class="toggle-slider"></span>
          </label>
        </div>

        <div class="pref-row pref-indent" id="row-windowed" {{if not .Settings.DirectStreamMode}}style="opacity:.4;pointer-events:none"{{end}}>
          <div class="pref-label">
            <span>Open each camera in its own window</span>
            <span class="pref-desc">Each camera cell opens a fullscreen popup showing the MJPEG stream with a live status dot. Requires popups to be allowed for this site.</span>
          </div>
          <label class="toggle">
            <input type="checkbox" id="pref-windowed" {{if .Settings.DirectStreamWindowed}}checked{{end}}>
            <span class="toggle-slider"></span>
          </label>
        </div>

        <div class="pref-row" style="margin-top:0.75rem">
          <div class="pref-label">
            <span>Stream protocol</span>
            <span class="pref-desc">Primary protocol the server uses to connect to cameras.</span>
          </div>
          <div style="display:flex;flex-direction:column;gap:0.35rem">
            <label class="radio-row">
              <input type="radio" name="pref-proto" value="ws"    {{if eq .Settings.StreamProtocol "ws"   }}checked{{end}}> WebSocket (WS) — Dahua /rtspoverwebsocket (port 80)
            </label>
            <label class="radio-row">
              <input type="radio" name="pref-proto" value="rtsp"  {{if eq .Settings.StreamProtocol "rtsp" }}checked{{end}}> RTSP TCP — standard port 554, Digest auth
            </label>
          </div>
        </div>

        <div class="pref-row" style="margin-top:0.5rem">
          <div class="pref-label">
            <span>Auto-reconnect on failure</span>
            <span class="pref-desc">Automatically retry the chosen protocol when the stream drops.</span>
          </div>
          <label class="toggle">
            <input type="checkbox" id="pref-fallback" {{if .Settings.StreamProtocolFallback}}checked{{end}}>
            <span class="toggle-slider"></span>
          </label>
        </div>
      </div>

      {{/* ── Security ── */}}
      <div class="pref-section">
        <div class="pref-section-title">Security</div>

        <div class="pref-row" style="flex-direction:column;align-items:stretch;gap:0.6rem">
          <div class="pref-label" style="margin-bottom:0.2rem">
            <span>Change login password</span>
            <span class="pref-desc">All active sessions will be signed out immediately after the change.</span>
          </div>

          <div class="login-field">
            <label>Current password</label>
            <input type="password" id="pwd-current" autocomplete="current-password" placeholder="Current password">
          </div>
          <div class="login-field">
            <label>New password <span class="text-muted">(min 6 chars)</span></label>
            <input type="password" id="pwd-new" autocomplete="new-password" placeholder="New password">
          </div>
          <div class="login-field">
            <label>Confirm new password</label>
            <input type="password" id="pwd-confirm" autocomplete="new-password" placeholder="Repeat new password">
          </div>

          <div id="pwd-error" class="login-error hidden"></div>
          <div id="pwd-ok"    class="hidden" style="font-size:0.82rem;color:var(--ok)">✓ Password changed — please sign in again.</div>
        </div>
      </div>
    </div>
  </div>

  {{/* Sticky action bar at bottom */}}
  <div class="pref-actions">
    <button class="btn" onclick="savePrefs()">Save changes</button>
    <button class="btn" onclick="changePassword()">Change password</button>
    <div id="pref-status" style="margin-left:0.5rem;font-size:0.82rem"></div>
  </div>
</div>
{{end}}

{{define "scripts"}}
<script src="/static/settings.js?v=20260425d"></script>
{{end}}`

// ── Stream Health ─────────────────────────────────────────────────────────────
const healthTmpl = `{{define "content"}}
<div class="topbar">
  <span class="topbar-title">Stream Health</span>
  {{if not .HealthMonitoring}}<span class="pill pill-warn" style="margin-left:0.5rem">Monitoring Disabled</span>{{end}}
  <span style="flex:1"></span>
  <button class="btn btn-sm" id="health-refresh" onclick="refreshHealth()">↻ Refresh</button>
</div>
<div class="page-content" id="health-page">

  {{if not .HealthMonitoring}}
  <div style="text-align:center;padding:3rem 1rem;color:var(--text3)">
    <div style="font-size:2rem;opacity:0.3;margin-bottom:0.75rem">♡</div>
    <div style="font-size:0.9rem;margin-bottom:0.5rem">Advanced health monitoring is disabled.</div>
    <div style="font-size:0.78rem">Enable it in <a href="/preferences" style="color:var(--accent2)">Preferences → System</a> to see per-camera diagnostics.</div>
  </div>
  {{else}}

  {{/* Summary cards */}}
  <div class="health-summary" id="health-summary">
    <div class="health-card health-card-total">
      <div class="health-card-value" id="hc-total">{{len .Cameras}}</div>
      <div class="health-card-label">Total Cameras</div>
    </div>
    <div class="health-card health-card-ok">
      <div class="health-card-value" id="hc-ok">—</div>
      <div class="health-card-label">Streaming</div>
    </div>
    <div class="health-card health-card-starting">
      <div class="health-card-value" id="hc-starting">—</div>
      <div class="health-card-label">Starting</div>
    </div>
    <div class="health-card health-card-auth">
      <div class="health-card-value" id="hc-auth">—</div>
      <div class="health-card-label">Auth Failed</div>
    </div>
    <div class="health-card health-card-offline">
      <div class="health-card-value" id="hc-offline">—</div>
      <div class="health-card-label">Offline</div>
    </div>
  </div>

  {{/* Per-camera detail table */}}
  <table class="nvr-table" id="health-table">
    <thead>
      <tr>
        <th>Camera</th>
        <th>IP</th>
        <th>Status</th>
        <th>Protocol</th>
        <th>Codec</th>
        <th>FPS</th>
        <th>Bitrate</th>
        <th>Uptime</th>
        <th>Reconnects</th>
        <th>Dropped</th>
        <th>Viewers</th>
        <th>Last Error</th>
      </tr>
    </thead>
    <tbody id="health-tbody">
      <tr><td colspan="12" class="text-muted" style="text-align:center;padding:2rem">Loading…</td></tr>
    </tbody>
  </table>

  {{/* Per-camera chart panel (hidden until a row is clicked) */}}
  <div class="health-chart-panel" id="health-chart-panel" style="display:none">
    <div class="health-chart-header">
      <span class="health-chart-title" id="chart-cam-name">—</span>
      <div class="health-chart-live-stats" id="chart-live-stats"></div>
      <button class="btn btn-sm" id="chart-close" onclick="closeChart()">✕ Close</button>
    </div>
    <div class="health-chart-hint">Click a camera row to view live metrics. Data updates every 2 seconds.</div>
    <div class="health-charts-grid">
      <div class="health-chart-box">
        <div class="health-chart-box-label">Frame Rate (FPS)</div>
        <canvas id="chart-fps" width="560" height="180"></canvas>
      </div>
      <div class="health-chart-box">
        <div class="health-chart-box-label">Bitrate</div>
        <canvas id="chart-bitrate" width="560" height="180"></canvas>
      </div>
    </div>
  </div>

  {{end}}
</div>
{{end}}

{{define "scripts"}}
{{if .HealthMonitoring}}
<script src="/static/health.js?v=20260425d"></script>
{{end}}
{{end}}`

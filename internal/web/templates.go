package web

func templateFiles() map[string]string {
	return map[string]string{
		"base.html":      baseTmpl,
		"dashboard.html": dashboardTmpl,
		"discover.html":  discoverTmpl,
		"config.html":    configTmpl,
		"login.html":     loginTmpl,
		"viewer.html":    viewerTmpl,
	}
}

// ── Base shell ────────────────────────────────────────────────────────────────
const baseTmpl = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width,initial-scale=1">
  <title>JSOC NVR</title>
  <link rel="stylesheet" href="/static/app.css">
</head>
<body>
<aside class="sidebar">
  <div class="sidebar-logo">
    <div class="brand">JSOC NVR</div>
    <div class="sub">Camera Viewer</div>
  </div>
  <nav class="sidebar-nav">
    <a href="/"        class="nav-item {{if eq .Page "dashboard"}}active{{end}}"><span class="nav-icon">⊞</span> Dashboard</a>
    <a href="/discover" class="nav-item {{if eq .Page "discover"}}active{{end}}"><span class="nav-icon">⌖</span> Discover</a>
    <a href="/config"   class="nav-item {{if eq .Page "config"}}active{{end}}"><span class="nav-icon">⚙</span> Configuration</a>
  </nav>
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
  <div class="grid-controls">
    <button class="grid-btn active" onclick="setGrid(1)" id="btn-1x1" title="1×1">1×1</button>
    <button class="grid-btn"       onclick="setGrid(2)" id="btn-2x2" title="2×2">2×2</button>
    <button class="grid-btn"       onclick="setGrid(3)" id="btn-3x3" title="3×3">3×3</button>
    <button class="grid-btn"       onclick="setGrid(4)" id="btn-4x4" title="4×4">4×4</button>
    <div class="sep"></div>
    <button class="grid-btn active" onclick="setMode('grid')" id="btn-grid" title="Grid view">⊞ Grid</button>
    <button class="grid-btn"        onclick="setMode('table')" id="btn-table" title="Table view">☰ Table</button>
  </div>
</div>

{{/* ── Grid view ── */}}
<div id="view-grid" class="grid-container">
  <div class="camera-grid grid-2x2" id="camera-grid">
    {{range $i, $cam := .Cameras}}
    <div class="cam-cell" id="cell-{{$cam.ID}}"
         onclick="openModal('{{$cam.ID}}','{{$cam.Name}}','{{$cam.IP}}','{{$cam.StreamKey}}','{{$cam.Health}}','{{$cam.StreamRTSPURL}}')"
         data-id="{{$cam.ID}}"
         data-key="{{$cam.StreamKey}}"
         data-health="{{$cam.Health}}"
         data-creds="{{$cam.HasCredentials}}">
      <span class="cam-channel">CH {{inc $i}}</span>
      {{if $cam.HasCredentials}}
        <video id="video-{{$cam.ID}}" autoplay muted playsinline></video>
      {{else}}
        <div class="cam-placeholder">
          <div class="cam-icon">📷</div>
          <div>Not configured</div>
        </div>
      {{end}}
      {{if and (not $cam.HasCredentials) false}}<div class="cam-login-badge">⚠ Login required</div>{{end}}
      <div class="cam-overlay">
        <span class="cam-name">{{$cam.Name}}</span>
        <span class="status-dot dot-{{$cam.Health}}"></span>
      </div>
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
            <button class="btn btn-sm" onclick="openModal('{{$cam.ID}}','{{$cam.Name}}','{{$cam.IP}}','{{$cam.StreamKey}}','{{$cam.Health}}','{{$cam.StreamRTSPURL}}')">View</button>
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

{{/* ── Fullscreen modal ── */}}
<div id="modal" class="modal-overlay hidden">
  <div class="modal-header">
    <span id="modal-title" class="modal-title"></span>
    <span id="modal-meta"  class="modal-meta"></span>
    <button class="modal-close" onclick="closeModal()">✕</button>
  </div>
  <div class="modal-body">
    <video id="modal-video" autoplay muted playsinline controls></video>
  </div>
  <div class="modal-footer">
    <span>RTSP:</span>
    <code id="modal-rtsp">—</code>
    <button class="btn btn-ghost btn-sm" onclick="copyRTSP()">Copy</button>
    <span style="flex:1"></span>
    <span class="text-muted text-sm">Press <kbd>Esc</kbd> to close</span>
    <button class="btn btn-danger btn-sm" onclick="deleteModalCam()">Remove Camera</button>
  </div>
</div>
{{end}}

{{define "scripts"}}
<script src="/static/player.js"></script>
<script src="/static/dashboard.js"></script>
<script>
const CAMERAS = [
  {{range .Cameras}}
  {id:"{{.ID}}",name:"{{.Name}}",ip:"{{.IP}}",key:"{{.StreamKey}}",health:"{{.Health}}",hasCreds:{{.HasCredentials}},rtsp:"{{.StreamRTSPURL}}"},
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
<script src="/static/discover.js"></script>
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
<script src="/static/config.js"></script>
{{end}}`

// ── Login ─────────────────────────────────────────────────────────────────────
const loginTmpl = `{{define "content"}}
<div class="login-wrap">
  <div class="login-card">
    <div>
      <h2>Camera Login</h2>
      <div class="cam-ip">{{.Camera.Name}} — {{.Camera.IP}}:{{.Camera.Port}}</div>
    </div>
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
      <button type="submit" class="btn" style="width:100%">Login</button>
    </form>
  </div>
</div>
{{end}}

{{define "scripts"}}
<script>
const CAMERA_ID = "{{.Camera.ID}}";
</script>
<script src="/static/login.js"></script>
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
<script src="/static/player.js"></script>
<script>
  const v = document.getElementById('main-video');
  const proto = location.protocol === 'https:' ? 'wss' : 'ws';
  const wsUrl = proto + '://' + location.host + '/ws/camera/{{.Camera.ID}}';
  new MSEPlayer(v, wsUrl);
</script>
{{end}}`

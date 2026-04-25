'use strict';

let currentGrid = 2;
let currentMode = 'grid';
let modalPlayer = null;
let modalCamId  = null;   // ID of the camera currently shown in the modal
let ptzVisible  = false;
const players   = {};     // cameraID → MSEPlayer

function initDashboard(cameras) {
  // In direct-windowed mode wire cell clicks to open popup windows.
  if (typeof DIRECT_WINDOWED !== 'undefined' && DIRECT_WINDOWED) {
    document.querySelectorAll('.cam-cell:not(.empty)').forEach(cell => {
      cell.style.cursor = 'pointer';
      cell.addEventListener('click', () => openDirectWindow(cell.dataset.id));
    });
  }

  // Start MSE players only when NOT in direct stream mode.
  if (typeof DIRECT_MODE === 'undefined' || !DIRECT_MODE) {
    for (const cam of cameras) {
      if (cam.hasCreds && cam.health !== 'offline') {
        startPlayer(cam.id, cam.key);
      }
    }
  }

  // Restore grid size from localStorage.
  const saved = localStorage.getItem('nvrGrid');
  if (saved) setGrid(parseInt(saved), true);

  // Auto-refresh health every 5s.
  setInterval(refreshHealth, 5000);

  // Keyboard: Esc closes modal.
  document.addEventListener('keydown', (e) => {
    if (e.key === 'Escape') closeModal();
  });

  // Wire PTZ D-pad and zoom buttons (mousedown = start, mouseup/leave = stop).
  wirePTZButtons();
}

// ── Direct-window mode ────────────────────────────────────────────────────────

function openDirectWindow(camId) {
  if (!camId) return;
  const url = '/cameras/' + camId + '/direct';
  const w = window.open(url, 'jsoc-direct-' + camId,
    'noopener,width=1280,height=720');
  if (!w) {
    // Popup was blocked — show transient banner on the camera cell.
    const cell = document.getElementById('cell-' + camId);
    if (cell) {
      let banner = cell.querySelector('.popup-blocked-msg');
      if (!banner) {
        banner = document.createElement('div');
        banner.className = 'popup-blocked-msg';
        banner.textContent = 'Open failed — please allow popups for this site';
        cell.appendChild(banner);
      }
      clearTimeout(banner._timer);
      banner._timer = setTimeout(() => banner.remove(), 5000);
    }
  }
}

// ── Grid size ────────────────────────────────────────────────────────────────

function setGrid(n, silent) {
  currentGrid = n;
  if (!silent) localStorage.setItem('nvrGrid', n);

  const grid = document.getElementById('camera-grid');
  grid.className = `camera-grid grid-${n}x${n}`;

  [1,2,3,4].forEach(i => {
    const btn = document.getElementById(`btn-${i}x${i}`);
    if (btn) btn.classList.toggle('active', i === n);
  });
}

// ── View mode ─────────────────────────────────────────────────────────────────

function setMode(mode) {
  currentMode = mode;
  document.getElementById('view-grid').classList.toggle('hidden', mode !== 'grid');
  document.getElementById('view-table').classList.toggle('hidden', mode !== 'table');
  document.getElementById('btn-grid').classList.toggle('active', mode === 'grid');
  document.getElementById('btn-table').classList.toggle('active', mode === 'table');
}

// ── Canvas/WebCodecs players ─────────────────────────────────────────────────

function startPlayer(camId, streamKey) {
  const video = document.getElementById('video-' + camId);
  if (!video) return;
  if (players[camId]) { players[camId].destroy(); }

  const proto = location.protocol === 'https:' ? 'wss' : 'ws';
  const wsUrl = `${proto}://${location.host}/ws/annexb/${streamKey}`;
  players[camId] = new MSEPlayer(video, wsUrl, {
    fallbackUrl:  '/proxy/cameras/' + camId + '/stream',
    wasmCameraId: camId,
  });
}

// ── Fullscreen modal ──────────────────────────────────────────────────────────

function openModal(id, name, ip, key, health, rtsp, hasPTZ) {
  const modal = document.getElementById('modal');
  if (!modal) return; // not rendered in windowed direct mode

  modalCamId = id;
  ptzVisible = false;
  document.getElementById('ptz-panel') && document.getElementById('ptz-panel').classList.add('hidden');

  document.getElementById('modal-title').textContent = name;
  document.getElementById('modal-meta').textContent  = ip;
  document.getElementById('modal-rtsp').textContent  = rtsp || '—';

  // Show/hide PTZ toggle button.
  const ptzBtn = document.getElementById('ptz-toggle-btn');
  if (ptzBtn) ptzBtn.classList.toggle('hidden', !hasPTZ);

  // Destroy previous modal player.
  if (modalPlayer) { modalPlayer.destroy(); modalPlayer = null; }

  modal.classList.remove('hidden');
  document.body.style.overflow = 'hidden';

  const directMode = typeof DIRECT_MODE !== 'undefined' && DIRECT_MODE;

  if (directMode) {
    // Show MJPEG stream in modal image.
    const img = document.getElementById('modal-img');
    if (img) img.src = '/proxy/cameras/' + id + '/stream';
  } else {
    // Canvas/WebCodecs player.
    const video = document.getElementById('modal-video');
    if (video && health !== 'offline' && health !== 'auth-failed' && health !== 'unknown') {
      const proto = location.protocol === 'https:' ? 'wss' : 'ws';
      modalPlayer = new MSEPlayer(video, `${proto}://${location.host}/ws/annexb/${key}`, {
        fallbackUrl:  '/proxy/cameras/' + id + '/stream',
        wasmCameraId: id,
      });
    }
  }
}

function closeModal() {
  const modal = document.getElementById('modal');
  if (!modal) return;
  modal.classList.add('hidden');
  document.body.style.overflow = '';
  if (modalPlayer) { modalPlayer.destroy(); modalPlayer = null; }

  const video = document.getElementById('modal-video');
  if (video) video.src = '';
  const img = document.getElementById('modal-img');
  if (img) img.src = '';

  modalCamId = null;
  ptzVisible = false;
  const ptzPanel = document.getElementById('ptz-panel');
  if (ptzPanel) ptzPanel.classList.add('hidden');
}

function copyRTSP() {
  const url = document.getElementById('modal-rtsp').textContent;
  if (url && url !== '—') navigator.clipboard.writeText(url);
}

async function deleteModalCam() {
  if (!modalCamId) return;
  const title = document.getElementById('modal-title').textContent;
  if (!confirm(`Remove "${title}" from the system?`)) return;

  const id = modalCamId;
  try {
    const resp = await fetch('/api/cameras/' + id, { method: 'DELETE' });
    if (!resp.ok) {
      const err = await resp.json().catch(() => ({}));
      alert('Error: ' + (err.error || 'unknown'));
      return;
    }
  } catch (ex) {
    alert('Network error: ' + ex.message);
    return;
  }

  // Stop and remove grid cell.
  if (players[id]) { players[id].destroy(); delete players[id]; }
  const cell = document.getElementById('cell-' + id);
  if (cell) cell.remove();

  // Remove table row if present.
  const row = document.getElementById('row-' + id);
  if (row) row.remove();

  closeModal();
}

// ── PTZ controls ──────────────────────────────────────────────────────────────

function togglePTZ() {
  ptzVisible = !ptzVisible;
  document.getElementById('ptz-panel').classList.toggle('hidden', !ptzVisible);
  document.getElementById('ptz-toggle-btn').classList.toggle('active', ptzVisible);
}

async function ptzSend(body) {
  if (!modalCamId) return;
  try {
    await fetch('/api/cameras/' + modalCamId + '/ptz', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    });
  } catch (_) {}
}

function ptzStop()      { ptzSend({ action: 'stop' }); }
function ptzFocusStop() { ptzSend({ action: 'focus-stop' }); }
function ptzFocusAuto() { ptzSend({ action: 'focus-auto' }); }

// Wire directional + zoom buttons: hold = continuous move, release = stop.
function wirePTZButtons() {
  // Directional / zoom buttons share data-pan / data-tilt / data-zoom attrs.
  document.querySelectorAll('.ptz-btn[data-pan]').forEach(btn => {
    const start = () => ptzSend({
      action: 'move',
      pan:  parseFloat(btn.dataset.pan  || 0),
      tilt: parseFloat(btn.dataset.tilt || 0),
      zoom: parseFloat(btn.dataset.zoom || 0),
    });
    btn.addEventListener('mousedown',   start);
    btn.addEventListener('touchstart',  start, { passive: true });
    btn.addEventListener('mouseup',     ptzStop);
    btn.addEventListener('mouseleave',  ptzStop);
    btn.addEventListener('touchend',    ptzStop);
  });

  // Focus buttons use data-speed attr.
  document.querySelectorAll('.ptz-btn.ptz-focus').forEach(btn => {
    const speed = parseFloat(btn.dataset.speed || 0);
    btn.addEventListener('mousedown',   () => ptzSend({ action: 'focus', speed }));
    btn.addEventListener('touchstart',  () => ptzSend({ action: 'focus', speed }), { passive: true });
    btn.addEventListener('mouseup',     ptzFocusStop);
    btn.addEventListener('mouseleave',  ptzFocusStop);
    btn.addEventListener('touchend',    ptzFocusStop);
  });
}

// ── Health polling ────────────────────────────────────────────────────────────

async function refreshHealth() {
  try {
    const resp = await fetch('/api/cameras');
    if (!resp.ok) return;
    const cameras = await resp.json();
    for (const cam of cameras) {
      // Update grid cell overlay dot
      const cell = document.getElementById('cell-' + cam.id);
      if (cell) {
        const dot = cell.querySelector('.status-dot');
        if (dot) { dot.className = `status-dot dot-${cam.health}`; }

        // Start player if it just came online
        if (cam.has_credentials && cam.health === 'ok' && !players[cam.id]) {
          startPlayer(cam.id, cam.stream_key);
        }
      }
      // Update table row pill
      const row = document.getElementById('row-' + cam.id);
      if (row) {
        const pill = row.querySelector('.pill');
        if (pill) { pill.className = `pill pill-${cam.health}`; pill.textContent = cam.health; }
      }
    }
  } catch (_) {}
}

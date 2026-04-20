'use strict';

let currentGrid = 2;
let currentMode = 'grid';
let modalPlayer = null;
let modalCamId = null;  // ID of the camera currently shown in the modal
const players = {};    // cameraID → MSEPlayer

function initDashboard(cameras) {
  // Start MSE players for cameras that have credentials.
  for (const cam of cameras) {
    if (cam.hasCreds && cam.health !== 'offline') {
      startPlayer(cam.id, cam.key);
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

// ── MSE players ───────────────────────────────────────────────────────────────

function startPlayer(camId, streamKey) {
  const video = document.getElementById('video-' + camId);
  if (!video) return;
  if (players[camId]) { players[camId].destroy(); }

  const proto = location.protocol === 'https:' ? 'wss' : 'ws';
  const wsUrl = `${proto}://${location.host}/ws/stream/${streamKey}`;
  players[camId] = new MSEPlayer(video, wsUrl);
}

// ── Fullscreen modal ──────────────────────────────────────────────────────────

function openModal(id, name, ip, key, health, rtsp) {
  const modal = document.getElementById('modal');
  const video = document.getElementById('modal-video');

  modalCamId = id;
  document.getElementById('modal-title').textContent = name;
  document.getElementById('modal-meta').textContent  = ip;
  document.getElementById('modal-rtsp').textContent  = rtsp || '—';

  // Destroy previous modal player.
  if (modalPlayer) { modalPlayer.destroy(); modalPlayer = null; }

  modal.classList.remove('hidden');
  document.body.style.overflow = 'hidden';

  if (health !== 'offline' && health !== 'auth-failed' && health !== 'unknown') {
    const proto = location.protocol === 'https:' ? 'wss' : 'ws';
    modalPlayer = new MSEPlayer(video, `${proto}://${location.host}/ws/stream/${key}`);
  }
}

function closeModal() {
  document.getElementById('modal').classList.add('hidden');
  document.body.style.overflow = '';
  if (modalPlayer) { modalPlayer.destroy(); modalPlayer = null; }
  const video = document.getElementById('modal-video');
  video.src = '';
  modalCamId = null;
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

  // Stop and remove grid cell
  if (players[id]) { players[id].destroy(); delete players[id]; }
  const cell = document.getElementById('cell-' + id);
  if (cell) cell.remove();

  // Remove table row if present
  const row = document.getElementById('row-' + id);
  if (row) row.remove();

  closeModal();
}

function copyRTSP() {
  const url = document.getElementById('modal-rtsp').textContent;
  if (url && url !== '—') navigator.clipboard.writeText(url);
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

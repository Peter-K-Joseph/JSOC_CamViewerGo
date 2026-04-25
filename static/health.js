/* Stream Health page — health.js */

(function () {
  'use strict';

  const MAX_POINTS  = 60;  // 60 × 2s = 2 minutes of history
  const CHART_POLL  = 2000;
  const TABLE_POLL  = 5000;

  let pollTimer     = null;
  let chartTimer    = null;
  let selectedCamId = null;
  let lastCameras   = [];

  // Per-camera rolling history: { camId: { fps: [], bitrate: [], ts: [] } }
  const history = {};

  /* ── Formatting helpers ─────────────────────────────────────────────── */

  function formatUptime(seconds) {
    if (!seconds || seconds <= 0) return '—';
    const h = Math.floor(seconds / 3600);
    const m = Math.floor((seconds % 3600) / 60);
    const s = Math.floor(seconds % 60);
    if (h > 0) return h + 'h ' + m + 'm';
    if (m > 0) return m + 'm ' + s + 's';
    return s + 's';
  }

  function healthPill(status) {
    const cls = {
      'ok':          'pill-ok',
      'starting':    'pill-starting',
      'auth-failed': 'pill-auth',
      'offline':     'pill-offline',
      'unknown':     'pill-unknown',
    }[status] || 'pill-unknown';
    return '<span class="pill ' + cls + '">' + (status || 'unknown') + '</span>';
  }

  function protoBadge(proto, fallback) {
    if (!proto) return '<span class="text-muted">—</span>';
    let html = '<span class="health-proto">' + proto.toUpperCase() + '</span>';
    if (fallback) html += ' <span class="health-fallback-badge">fallback</span>';
    return html;
  }

  function truncateError(err, maxLen) {
    if (!err) return '<span class="text-muted">—</span>';
    const safe = err.replace(/</g, '&lt;').replace(/>/g, '&gt;');
    if (safe.length > (maxLen || 80)) {
      return '<span class="health-error" title="' + safe + '">' + safe.slice(0, maxLen || 80) + '…</span>';
    }
    return '<span class="health-error">' + safe + '</span>';
  }

  function formatBitrate(bps) {
    if (!bps || bps <= 0) return '<span class="text-muted">—</span>';
    if (bps >= 1000000) return (bps / 1000000).toFixed(1) + ' Mbps';
    if (bps >= 1000)    return (bps / 1000).toFixed(0) + ' Kbps';
    return Math.round(bps) + ' bps';
  }

  function formatBitrateText(bps) {
    if (!bps || bps <= 0) return '—';
    if (bps >= 1000000) return (bps / 1000000).toFixed(2) + ' Mbps';
    if (bps >= 1000)    return (bps / 1000).toFixed(1) + ' Kbps';
    return Math.round(bps) + ' bps';
  }

  function formatFPS(fps) {
    if (!fps || fps <= 0) return '<span class="text-muted">—</span>';
    return fps.toFixed(1);
  }

  function codecBadge(codec) {
    if (!codec) return '<span class="text-muted">—</span>';
    return '<span class="health-codec">' + codec.toUpperCase() + '</span>';
  }

  /* ── API ────────────────────────────────────────────────────────────── */

  async function fetchHealth() {
    try {
      const r = await fetch('/api/health');
      if (!r.ok) return null;
      const data = await r.json();
      if (data.status === 'disabled') return null;
      return data;
    } catch { return null; }
  }

  /* ── Summary cards ──────────────────────────────────────────────────── */

  function updateSummary(cameras) {
    let ok = 0, starting = 0, auth = 0, offline = 0;
    cameras.forEach(function (c) {
      const h = c.diag && c.diag.health || 'unknown';
      if (h === 'ok') ok++;
      else if (h === 'starting') starting++;
      else if (h === 'auth-failed') auth++;
      else offline++;
    });
    var el = function (id) { return document.getElementById(id); };
    if (el('hc-total'))    el('hc-total').textContent    = cameras.length;
    if (el('hc-ok'))       el('hc-ok').textContent       = ok;
    if (el('hc-starting')) el('hc-starting').textContent  = starting;
    if (el('hc-auth'))     el('hc-auth').textContent      = auth;
    if (el('hc-offline'))  el('hc-offline').textContent    = offline;
  }

  /* ── Table ──────────────────────────────────────────────────────────── */

  function updateTable(cameras) {
    var tbody = document.getElementById('health-tbody');
    if (!tbody) return;

    if (!cameras || cameras.length === 0) {
      tbody.innerHTML = '<tr><td colspan="12" class="text-muted" style="text-align:center;padding:2rem">No cameras configured.</td></tr>';
      return;
    }

    var html = '';
    cameras.forEach(function (c) {
      var d = c.diag || {};
      var tk = d.track || {};
      var status = d.health || 'unknown';
      var sel = (c.id === selectedCamId) ? ' health-row-selected' : '';
      html += '<tr class="health-row health-row-' + status + sel + '" data-cam-id="' + (c.id || '') + '" style="cursor:pointer">';
      html += '<td><strong>' + (c.name || '—').replace(/</g, '&lt;') + '</strong></td>';
      html += '<td class="mono">' + (c.ip || '—') + ':' + (c.port || 80) + '</td>';
      html += '<td>' + healthPill(status) + '</td>';
      html += '<td>' + protoBadge(d.active_protocol, d.fallback_active) + '</td>';
      html += '<td>' + codecBadge(tk.codec) + '</td>';
      html += '<td class="mono">' + formatFPS(tk.fps) + '</td>';
      html += '<td class="mono">' + formatBitrate(tk.bitrate_bps) + '</td>';
      html += '<td>' + formatUptime(d.uptime_seconds) + '</td>';
      html += '<td>' + (d.reconnects || 0) + '</td>';
      html += '<td>' + (tk.dropped || 0) + '</td>';
      html += '<td>' + (tk.subscribers || 0) + '</td>';
      html += '<td>' + truncateError(d.last_error, 60) + '</td>';
      html += '</tr>';
    });
    tbody.innerHTML = html;
  }

  /* ── Row click → open chart ─────────────────────────────────────────── */

  document.addEventListener('click', function (e) {
    var row = e.target.closest('tr[data-cam-id]');
    if (!row) return;
    var id = row.getAttribute('data-cam-id');
    if (!id) return;
    selectCamera(id);
  });

  function selectCamera(id) {
    selectedCamId = id;

    // Find camera name from last fetch
    var cam = null;
    lastCameras.forEach(function (c) { if (c.id === id) cam = c; });
    var name = cam ? cam.name : id;

    var panel = document.getElementById('health-chart-panel');
    var title = document.getElementById('chart-cam-name');
    var hint  = panel.querySelector('.health-chart-hint');
    if (title) title.textContent = name;
    if (hint) hint.style.display = 'none';
    if (panel) panel.style.display = '';

    // Re-render table for selected highlight
    updateTable(lastCameras);

    // Start chart polling at 2s
    startChartPoll();

    // Immediately record first point and draw
    if (cam) recordPoint(cam);
    drawCharts(id);
  }

  window.closeChart = function () {
    selectedCamId = null;
    var panel = document.getElementById('health-chart-panel');
    if (panel) panel.style.display = 'none';
    stopChartPoll();
    updateTable(lastCameras);
  };

  /* ── Chart polling (2s) ─────────────────────────────────────────────── */

  function startChartPoll() {
    stopChartPoll();
    chartTimer = setInterval(chartTick, CHART_POLL);
  }

  function stopChartPoll() {
    if (chartTimer) { clearInterval(chartTimer); chartTimer = null; }
  }

  async function chartTick() {
    var cameras = await fetchHealth();
    if (!cameras) return;
    lastCameras = cameras;
    updateSummary(cameras);
    updateTable(cameras);

    // Record data point for selected camera
    var cam = null;
    cameras.forEach(function (c) { if (c.id === selectedCamId) cam = c; });
    if (cam) {
      recordPoint(cam);
      updateLiveStats(cam);
      drawCharts(selectedCamId);
    }
  }

  function recordPoint(cam) {
    var d  = cam.diag || {};
    var tk = d.track || {};
    var id = cam.id;
    if (!history[id]) history[id] = { fps: [], bitrate: [], ts: [] };
    var h  = history[id];
    var now = Date.now();
    h.fps.push(tk.fps || 0);
    h.bitrate.push(tk.bitrate_bps || 0);
    h.ts.push(now);
    // Trim to MAX_POINTS
    while (h.fps.length > MAX_POINTS) {
      h.fps.shift(); h.bitrate.shift(); h.ts.shift();
    }
  }

  function updateLiveStats(cam) {
    var d  = cam.diag || {};
    var tk = d.track || {};
    var el = document.getElementById('chart-live-stats');
    if (!el) return;
    var parts = [];
    if (tk.codec)      parts.push('<span class="health-codec">' + tk.codec.toUpperCase() + '</span>');
    if (tk.fps > 0)    parts.push('<span class="chart-stat">' + tk.fps.toFixed(1) + ' fps</span>');
    if (tk.bitrate_bps > 0) parts.push('<span class="chart-stat">' + formatBitrateText(tk.bitrate_bps) + '</span>');
    if (tk.total_frames) parts.push('<span class="chart-stat">' + tk.total_frames.toLocaleString() + ' frames</span>');
    if (tk.total_bytes)  parts.push('<span class="chart-stat">' + formatBitrateText(tk.total_bytes * 8).replace('bps', 'b total') + '</span>');
    if (tk.subscribers != null) parts.push('<span class="chart-stat">' + tk.subscribers + ' viewer' + (tk.subscribers !== 1 ? 's' : '') + '</span>');
    el.innerHTML = parts.join('<span class="chart-stat-sep">·</span>');
  }

  /* ── Canvas chart drawing ───────────────────────────────────────────── */

  function drawCharts(camId) {
    var h = history[camId];
    if (!h || h.fps.length === 0) return;
    drawLine('chart-fps',     h.fps,     h.ts, '#5b97ff', '', formatFPSAxis);
    drawLine('chart-bitrate', h.bitrate, h.ts, '#22c55e', '', formatBitrateAxis);
  }

  function formatFPSAxis(v)     { return v.toFixed(0); }
  function formatBitrateAxis(v) {
    if (v >= 1000000) return (v / 1000000).toFixed(1) + 'M';
    if (v >= 1000)    return (v / 1000).toFixed(0) + 'K';
    return v.toFixed(0);
  }

  function drawLine(canvasId, data, ts, color, label, fmtY) {
    var canvas = document.getElementById(canvasId);
    if (!canvas) return;
    var ctx = canvas.getContext('2d');
    var dpr = window.devicePixelRatio || 1;
    var w   = canvas.clientWidth;
    var h   = canvas.clientHeight;

    // Handle HiDPI
    if (canvas.width !== w * dpr || canvas.height !== h * dpr) {
      canvas.width  = w * dpr;
      canvas.height = h * dpr;
      ctx.scale(dpr, dpr);
    }

    var pad = { top: 12, right: 14, bottom: 28, left: 52 };
    var cw  = w - pad.left - pad.right;
    var ch  = h - pad.top  - pad.bottom;

    // Clear
    ctx.clearRect(0, 0, w, h);

    if (data.length < 2) {
      ctx.fillStyle = '#8ca3c7';
      ctx.font = '11px -apple-system, BlinkMacSystemFont, sans-serif';
      ctx.textAlign = 'center';
      ctx.fillText('Collecting data…', w / 2, h / 2);
      return;
    }

    // Compute Y range
    var maxY = 0;
    for (var i = 0; i < data.length; i++) { if (data[i] > maxY) maxY = data[i]; }
    if (maxY <= 0) maxY = 1;
    maxY = maxY * 1.15; // 15% headroom

    // Grid lines (4 horizontal)
    ctx.strokeStyle = 'rgba(34,52,81,0.6)';
    ctx.lineWidth = 1;
    ctx.font = '10px -apple-system, BlinkMacSystemFont, sans-serif';
    ctx.fillStyle = '#8ca3c7';
    ctx.textAlign = 'right';
    for (var g = 0; g <= 4; g++) {
      var gy = pad.top + ch - (g / 4) * ch;
      ctx.beginPath();
      ctx.moveTo(pad.left, gy);
      ctx.lineTo(pad.left + cw, gy);
      ctx.stroke();
      ctx.fillText(fmtY((g / 4) * maxY), pad.left - 6, gy + 3);
    }

    // Time labels on X axis
    var now = ts[ts.length - 1];
    ctx.textAlign = 'center';
    ctx.fillStyle = '#8ca3c7';
    for (var t = 0; t <= 4; t++) {
      var tVal   = now - (MAX_POINTS * CHART_POLL) + (t / 4) * (MAX_POINTS * CHART_POLL);
      var secAgo = Math.max(0, Math.round((now - tVal) / 1000));
      var lbl    = secAgo === 0 ? 'now' : '-' + secAgo + 's';
      var tx     = pad.left + (t / 4) * cw;
      ctx.fillText(lbl, tx, h - 6);
    }

    // Draw line
    ctx.strokeStyle = color;
    ctx.lineWidth = 2;
    ctx.lineJoin = 'round';
    ctx.lineCap = 'round';
    ctx.beginPath();

    // X positions: map timestamps relative to the full 2-min window
    var winStart = now - (MAX_POINTS * CHART_POLL);
    for (var j = 0; j < data.length; j++) {
      var x = pad.left + ((ts[j] - winStart) / (MAX_POINTS * CHART_POLL)) * cw;
      var y = pad.top + ch - (data[j] / maxY) * ch;
      if (j === 0) ctx.moveTo(x, y);
      else ctx.lineTo(x, y);
    }
    ctx.stroke();

    // Fill area under curve
    ctx.lineTo(pad.left + ((ts[data.length - 1] - winStart) / (MAX_POINTS * CHART_POLL)) * cw, pad.top + ch);
    ctx.lineTo(pad.left + ((ts[0] - winStart) / (MAX_POINTS * CHART_POLL)) * cw, pad.top + ch);
    ctx.closePath();
    ctx.fillStyle = color.replace(')', ', 0.08)').replace('rgb', 'rgba').replace('#', '');
    // Use hex-to-rgba for fill
    ctx.fillStyle = hexToRGBA(color, 0.08);
    ctx.fill();

    // Draw dots for the last point
    var lastX = pad.left + ((ts[data.length - 1] - winStart) / (MAX_POINTS * CHART_POLL)) * cw;
    var lastY = pad.top + ch - (data[data.length - 1] / maxY) * ch;
    ctx.beginPath();
    ctx.arc(lastX, lastY, 3.5, 0, Math.PI * 2);
    ctx.fillStyle = color;
    ctx.fill();
  }

  function hexToRGBA(hex, alpha) {
    var r = parseInt(hex.slice(1, 3), 16);
    var g = parseInt(hex.slice(3, 5), 16);
    var b = parseInt(hex.slice(5, 7), 16);
    return 'rgba(' + r + ',' + g + ',' + b + ',' + alpha + ')';
  }

  /* ── Main refresh cycle ─────────────────────────────────────────────── */

  async function refresh() {
    var cameras = await fetchHealth();
    if (!cameras) return;
    lastCameras = cameras;
    updateSummary(cameras);
    updateTable(cameras);
  }

  window.refreshHealth = function () {
    var btn = document.getElementById('health-refresh');
    if (btn) { btn.disabled = true; btn.textContent = '↻ …'; }
    refresh().finally(function () {
      if (btn) { btn.disabled = false; btn.textContent = '↻ Refresh'; }
    });
  };

  // Initial load + table poll every 5s.
  refresh();
  pollTimer = setInterval(refresh, TABLE_POLL);

  // Stop polling when page is hidden.
  document.addEventListener('visibilitychange', function () {
    if (document.hidden) {
      clearInterval(pollTimer); pollTimer = null;
      stopChartPoll();
    } else {
      if (!pollTimer) { refresh(); pollTimer = setInterval(refresh, TABLE_POLL); }
      if (selectedCamId) startChartPoll();
    }
  });
})();

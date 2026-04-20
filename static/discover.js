'use strict';

async function scan() {
  const btn    = document.getElementById('scan-btn');
  const status = document.getElementById('scan-status');
  const table  = document.getElementById('results-table');
  const tbody  = document.getElementById('results-body');

  btn.disabled = true;
  btn.textContent = '⌖ Scanning…';
  status.textContent = 'Scanning LAN for ONVIF cameras (5s)…';
  table.classList.add('hidden');
  tbody.innerHTML = '';

  try {
    const resp = await fetch('/api/discover', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ timeout_s: 5 }),
    });
    const devices = await resp.json();

    if (!Array.isArray(devices) || devices.length === 0) {
      status.textContent = 'No cameras found on the network.';
      return;
    }

    status.textContent = `Found ${devices.length} device(s).`;
    for (const d of devices) {
      const tr = document.createElement('tr');
      tr.innerHTML = `
        <td class="mono">${d.ip}</td>
        <td>${d.port}</td>
        <td>${d.manufacturer || '<span class="text-muted">—</span>'}</td>
        <td>${d.model || '<span class="text-muted">—</span>'}</td>
        <td><button class="btn btn-sm" onclick="addDevice('${d.ip}',${d.port},'${esc(d.manufacturer)}','${esc(d.model)}',this)">Add</button></td>
      `;
      tbody.appendChild(tr);
    }
    table.classList.remove('hidden');
  } catch (ex) {
    status.textContent = 'Scan failed: ' + ex.message;
  } finally {
    btn.disabled = false;
    btn.textContent = '⌖ Scan LAN';
  }
}

async function addDevice(ip, port, manufacturer, model, btn) {
  const defaultName = manufacturer ? `${manufacturer} ${ip}` : ip;
  const name = prompt('Camera name:', defaultName);
  if (!name) return;

  btn.disabled = true;
  try {
    const resp = await fetch('/api/cameras', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ name, ip, port, manufacturer, model }),
    });
    if (resp.ok) {
      const cam = await resp.json();
      window.location.href = '/cameras/' + cam.id + '/login';
    } else {
      const err = await resp.json().catch(() => ({}));
      alert('Error: ' + (err.error || 'unknown'));
      btn.disabled = false;
    }
  } catch (ex) {
    alert('Network error: ' + ex.message);
    btn.disabled = false;
  }
}

function esc(s) { return (s || '').replace(/'/g, "\\'"); }

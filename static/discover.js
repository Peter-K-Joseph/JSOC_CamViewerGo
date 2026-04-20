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
      tbody.appendChild(makeRow(d));
    }
    table.classList.remove('hidden');
  } catch (ex) {
    status.textContent = 'Scan failed: ' + ex.message;
  } finally {
    btn.disabled = false;
    btn.textContent = '⌖ Scan LAN';
  }
}

// Build a table row safely — no innerHTML; all untrusted values via textContent.
function makeRow(d) {
  const tr = document.createElement('tr');

  const tdIP = document.createElement('td');
  tdIP.className = 'mono';
  tdIP.textContent = d.ip;
  tr.appendChild(tdIP);

  const tdPort = document.createElement('td');
  tdPort.textContent = String(d.port);
  tr.appendChild(tdPort);

  tr.appendChild(makeOptionalCell(d.manufacturer));
  tr.appendChild(makeOptionalCell(d.model));

  // Add button — device data stored in data-* attributes, never interpolated into HTML.
  const addBtn = document.createElement('button');
  addBtn.className = 'btn btn-sm';
  addBtn.textContent = 'Add';
  addBtn.dataset.ip           = d.ip;
  addBtn.dataset.port         = String(d.port);
  addBtn.dataset.manufacturer = d.manufacturer || '';
  addBtn.dataset.model        = d.model || '';
  addBtn.addEventListener('click', function () { addDevice(this); });

  const tdAction = document.createElement('td');
  tdAction.appendChild(addBtn);
  tr.appendChild(tdAction);

  return tr;
}

function makeOptionalCell(value) {
  const td = document.createElement('td');
  td.textContent = value || '—';
  if (!value) td.className = 'text-muted';
  return td;
}

async function addDevice(btn) {
  const ip           = btn.dataset.ip;
  const port         = parseInt(btn.dataset.port, 10);
  const manufacturer = btn.dataset.manufacturer;
  const model        = btn.dataset.model;

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

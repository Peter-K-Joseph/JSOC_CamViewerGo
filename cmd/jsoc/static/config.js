'use strict';

function showAddForm() {
  document.getElementById('add-form').classList.remove('hidden');
  document.getElementById('add-name').focus();
}
function hideAddForm() {
  document.getElementById('add-form').classList.add('hidden');
}

async function addCamera() {
  const name = document.getElementById('add-name').value.trim();
  const ip   = document.getElementById('add-ip').value.trim();
  const port = parseInt(document.getElementById('add-port').value) || 80;
  if (!name || !ip) { alert('Name and IP are required.'); return; }

  const resp = await fetch('/api/cameras', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ name, ip, port }),
  });
  if (resp.ok) {
    window.location.reload();
  } else {
    const err = await resp.json();
    alert('Error: ' + (err.error || 'unknown'));
  }
}

async function deleteCam(id) {
  if (!confirm('Remove this camera and stop its stream?')) return;
  const resp = await fetch('/api/cameras/' + id, { method: 'DELETE' });
  if (resp.ok) {
    document.getElementById('row-' + id)?.remove();
  } else {
    alert('Failed to remove camera.');
  }
}

async function restartCam(id, btn) {
  btn.disabled = true;
  btn.textContent = '…';
  const resp = await fetch('/api/cameras/' + id + '/restart', { method: 'POST' });
  btn.disabled = false;
  btn.textContent = 'Restart';
  if (!resp.ok) {
    const err = await resp.json().catch(() => ({}));
    alert('Restart failed: ' + (err.error || 'unknown'));
  }
}

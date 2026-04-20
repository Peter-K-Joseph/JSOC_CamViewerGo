'use strict';

// ── Stream login ──────────────────────────────────────────────────────────────

async function doLogin(e) {
  e.preventDefault();
  const errEl = document.getElementById('login-error');
  errEl.textContent = '';

  const username = document.getElementById('username').value.trim();
  const password = document.getElementById('password').value;
  if (!username || !password) {
    errEl.textContent = 'Username and password are required.';
    return;
  }

  const btn = e.target.querySelector('button[type=submit]');
  if (btn) { btn.disabled = true; btn.textContent = 'Logging in…'; }

  try {
    const resp = await fetch('/api/cameras/' + CAMERA_ID + '/login', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ username, password }),
    });
    if (resp.ok) {
      // Stream login succeeded — reveal the optional ONVIF/PTZ section.
      if (btn) { btn.textContent = '✓ Stream connected'; }
      document.getElementById('onvif-section').classList.remove('hidden');
    } else {
      const err = await resp.json().catch(() => ({}));
      errEl.textContent = err.error || 'Login failed.';
      if (btn) { btn.disabled = false; btn.textContent = 'Login & Start Stream'; }
    }
  } catch (ex) {
    errEl.textContent = 'Network error: ' + ex.message;
    if (btn) { btn.disabled = false; btn.textContent = 'Login & Start Stream'; }
  }
}

// ── ONVIF / PTZ (optional) ───────────────────────────────────────────────────

async function doONVIFLogin() {
  const errEl = document.getElementById('onvif-error');
  const btns  = document.querySelectorAll('#onvif-section button');
  errEl.textContent = '';
  errEl.style.color = 'var(--danger)';

  const enableBtn = document.querySelector('#onvif-section .btn:not(.btn-ghost)');
  enableBtn.disabled = true;
  enableBtn.textContent = 'Probing PTZ…';

  const username = document.getElementById('onvif-username').value.trim();
  const password = document.getElementById('onvif-password').value;

  try {
    const resp = await fetch('/api/cameras/' + CAMERA_ID + '/onvif-login', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ username, password }),
    });
    const data = await resp.json().catch(() => ({}));
    if (resp.ok && data.ok) {
      enableBtn.textContent = '✓ PTZ enabled';
      errEl.style.color = 'var(--ok2)';
      errEl.textContent = 'PTZ ready — going to dashboard…';
      setTimeout(() => { window.location.href = '/'; }, 900);
    } else {
      errEl.textContent = data.error || 'ONVIF probe failed.';
      enableBtn.disabled = false;
      enableBtn.textContent = 'Enable PTZ';
    }
  } catch (ex) {
    errEl.textContent = 'Network error: ' + ex.message;
    enableBtn.disabled = false;
    enableBtn.textContent = 'Enable PTZ';
  }
}

function skipPTZ() {
  window.location.href = '/';
}

'use strict';

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
      window.location.href = '/';
    } else {
      const err = await resp.json().catch(() => ({}));
      errEl.textContent = err.error || 'Login failed.';
    }
  } catch (ex) {
    errEl.textContent = 'Network error: ' + ex.message;
  } finally {
    if (btn) { btn.disabled = false; btn.textContent = 'Login'; }
  }
}

/* Preferences page — settings.js */

// ── Theme ─────────────────────────────────────────────────────────────────────
(function () {
  const sel = document.getElementById('pref-theme');
  if (!sel) return;
  let stored = 'system';
  try {
    stored = localStorage.getItem('jsoc-theme') || 'system';
  } catch (_) {
    stored = 'system';
  }
  sel.value = stored;
  sel.addEventListener('change', function () {
    if (typeof window.setTheme === 'function') {
      window.setTheme(sel.value);
    }
  });
})();

window.setTheme = function (t) {
  const next = (t === 'light' || t === 'dark' || t === 'system') ? t : 'system';
  try {
    localStorage.setItem('jsoc-theme', next);
  } catch (_) {
    // Ignore localStorage errors (private mode / restricted storage).
  }
  const applied = next === 'system'
    ? (window.matchMedia('(prefers-color-scheme: light)').matches ? 'light' : 'dark')
    : next;
  document.documentElement.setAttribute('data-theme', applied);
  document.documentElement.style.colorScheme = applied;
};

(function () {
  const media = window.matchMedia('(prefers-color-scheme: light)');
  const onSystemThemeChange = function () {
    let pref = 'system';
    try {
      pref = localStorage.getItem('jsoc-theme') || 'system';
    } catch (_) {
      pref = 'system';
    }
    if (pref === 'system') {
      window.setTheme('system');
    }
  };

  if (typeof media.addEventListener === 'function') {
    media.addEventListener('change', onSystemThemeChange);
  } else if (typeof media.addListener === 'function') {
    media.addListener(onSystemThemeChange);
  }
})();

(function () {
  const saved  = document.getElementById('pref-saved');
  const errBox = document.getElementById('pref-error');

  // ── Direct mode toggle: enable/disable the windowed sub-option ──────────────
  const directCheck  = document.getElementById('pref-direct');
  const windowedRow  = document.getElementById('row-windowed');

  function updateWindowedState() {
    if (!windowedRow) return;
    const on = directCheck && directCheck.checked;
    windowedRow.style.opacity      = on ? '1' : '0.4';
    windowedRow.style.pointerEvents = on ? '' : 'none';
  }
  if (directCheck) directCheck.addEventListener('change', updateWindowedState);
  updateWindowedState();

  // ── Save ─────────────────────────────────────────────────────────────────────
  window.savePrefs = async function () {
    const proto = document.querySelector('input[name="pref-proto"]:checked');

    const body = {
      auto_start:               !!(document.getElementById('pref-autostart') || {}).checked,
      direct_stream_mode:       !!(document.getElementById('pref-direct')    || {}).checked,
      direct_stream_windowed:   !!(document.getElementById('pref-windowed')  || {}).checked,
      stream_protocol:          proto ? proto.value : 'ws',
      stream_protocol_fallback: !!(document.getElementById('pref-fallback')  || {}).checked,
      health_monitoring:        !!(document.getElementById('pref-health')    || {}).checked,
    };

    try {
      const r = await fetch('/api/settings', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
      });
      const data = await r.json();
      if (!r.ok) {
        showError(data.error || 'Save failed');
        return;
      }
      showSaved();
    } catch (e) {
      showError(String(e));
    }
  };

  // ── Change password ───────────────────────────────────────────────────────
  window.changePassword = async function () {
    const current  = (document.getElementById('pwd-current')  || {}).value || '';
    const newPwd   = (document.getElementById('pwd-new')      || {}).value || '';
    const confirm  = (document.getElementById('pwd-confirm')  || {}).value || '';
    const errEl    = document.getElementById('pwd-error');
    const okEl     = document.getElementById('pwd-ok');

    if (errEl) { errEl.textContent = ''; errEl.classList.add('hidden'); }
    if (okEl)  { okEl.classList.add('hidden'); }

    try {
      const r = await fetch('/api/change-password', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ current, new: newPwd, confirm }),
      });
      const data = await r.json();
      if (!r.ok) {
        if (errEl) { errEl.textContent = data.error || 'Failed'; errEl.classList.remove('hidden'); }
        return;
      }
      // Success — show message then redirect to login (sessions invalidated).
      if (okEl) okEl.classList.remove('hidden');
      ['pwd-current','pwd-new','pwd-confirm'].forEach(id => {
        const el = document.getElementById(id);
        if (el) el.value = '';
      });
      setTimeout(() => { window.location.href = '/ui/login'; }, 1800);
    } catch (e) {
      if (errEl) { errEl.textContent = String(e); errEl.classList.remove('hidden'); }
    }
  };

  function showSaved() {
    if (!saved) return;
    errBox && errBox.classList.add('hidden');
    saved.classList.remove('hidden');
    setTimeout(() => saved.classList.add('hidden'), 3000);
  }

  function showError(msg) {
    if (!errBox) return;
    saved && saved.classList.add('hidden');
    errBox.textContent = '⚠ ' + msg;
    errBox.classList.remove('hidden');
  }
})();

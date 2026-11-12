'use strict';
const $ = (s, el = document) => el.querySelector(s);
const ESC = { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' };
const esc = (s) => String(s ?? '').replace(/[&<>"']/g, (c) => ESC[c]);
const plural = (n, one, many) => `${n} ${n === 1 ? one : many || one + 's'}`;

async function api(method, path, body) {
  const r = await fetch(path, {
    method, credentials: 'same-origin',
    headers: { 'X-Typehaus': '1', ...(body ? { 'Content-Type': 'application/json' } : {}) },
    body: body ? JSON.stringify(body) : undefined,
  });
  const data = await r.json().catch(() => ({}));
  if (!r.ok) throw new Error(data.error || r.statusText);
  return data;
}

function ago(iso) {
  const t = new Date(iso).getTime();
  if (!t || t < 0 || new Date(iso).getFullYear() < 2000) return 'never';
  const s = Math.max(0, Math.round((Date.now() - t) / 1000));
  if (s < 10) return 'just now';
  if (s < 60) return `${s} s ago`;
  if (s < 3600) return `${Math.floor(s / 60)} min ago`;
  if (s < 86400) return `${Math.floor(s / 3600)} h ago`;
  return new Date(t).toLocaleDateString();
}
const clock = (iso) => new Date(iso).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
const host = (u) => { try { return new URL(u).host; } catch { return u; } };

const HEADLINE = {
  starting: ['Connecting…', ''],
  syncing: ['Syncing fonts…', ''],
  ok: ['Up to date', ''],
  offline: ['Server unreachable', 'Your fonts stay active. typehaus keeps retrying in the background.'],
  error: ['Some fonts need attention', ''],
  suspended: ['Account disabled', 'Your typehaus fonts were removed from this computer. They come back automatically once an administrator enables your account again.'],
  disconnected: ['Not connected', ''],
};

let S = null;
let lastKey = '';

async function refresh() {
  try {
    S = await api('GET', '/api/state');
  } catch (e) {
    document.body.innerHTML = '<main class="win"><div class="loading">typehaus has quit.<br><span style="font-size:15px">Start it again to reopen this window.</span></div></main>';
    clearInterval(timer);
    return;
  }
  const phase = S.status.phase;
  // do not re-render the connect form while the user is typing in it
  if (phase === 'disconnected' && $('#connect')) { updateNotice(); return; }
  const key = JSON.stringify([S.status, S.events.length, S.events[0], S.fonts.map((f) => f.sha256), S.autostart, S.update, S.notice]);
  if (key === lastKey) { tickTimes(); return; }
  lastKey = key;
  render();
}

function banners() {
  let h = '';
  if (S.notice) h += `<div class="notice" style="margin-bottom:16px">${esc(S.notice)}</div>`;
  if (S.update && S.update.available) {
    h += `<div class="update"><span><b>typehaus ${esc(S.update.version)}</b> is available on your server.</span>
      <a class="btn primary" href="${esc(S.update.url)}" target="_blank" rel="noopener">Download update</a></div>`;
  }
  return h;
}

function header() {
  const st = S.status;
  return `<div class="head"><img src="/icon.svg" alt="">
    <div><h1>typehaus</h1><div class="who">${st.user ? `${esc(st.user)} · ${esc(host(st.server))}` : 'Font sync for this computer'}</div></div>
    <span class="spacer"></span>
    ${st.server ? `<button class="btn" id="lib">Open font library ↗</button>` : ''}</div>`;
}

function render() {
  const app = $('#app');
  const st = S.status;
  if (st.phase === 'disconnected') { renderConnect(app); return; }
  const [title, sub] = HEADLINE[st.phase] || [st.phase, ''];
  const sub2 = st.phase === 'ok' || st.phase === 'syncing' || st.phase === 'error'
    ? `${plural(st.fonts, 'font file')} active · checked <span data-ago="${esc(st.last_sync)}">${ago(st.last_sync)}</span>` : sub;
  const showDetail = st.detail && !['ok', 'suspended'].includes(st.phase);

  // group fonts by family
  const fams = new Map();
  for (const f of S.fonts) { if (!fams.has(f.family)) fams.set(f.family, []); fams.get(f.family).push(f); }
  const famList = [...fams.entries()].map(([name, faces]) => {
    const rep = faces.find((f) => /^(regular|book|roman|normal)$/i.test(f.style)) || faces[0];
    return { name, faces, rep };
  });

  app.innerHTML = `${header()}${banners()}
    <section class="hero">
      <div class="light ${esc(st.phase)}" aria-hidden="true"></div>
      <div class="grow"><div class="t">${esc(title)}</div><div class="s">${sub2}</div>
        ${showDetail ? `<div class="detail">${esc(st.detail)}</div>` : ''}</div>
      <button class="btn" id="sync" ${st.phase === 'syncing' ? 'disabled' : ''}>Sync now</button>
    </section>

    <h2><span>Active on this computer</span><span>${plural(famList.length, 'family', 'families')}</span></h2>
    <div id="fonts">${famList.length ? famList.map((f) => `
      <div class="fam"><div class="spec" data-sha="${esc(f.rep.sha256)}">${esc(f.name)}</div>
      <div class="meta">${esc(f.faces.map((x) => x.style).join(' · '))}</div></div>`).join('')
      : `<div class="empty">No fonts yet. Turn fonts on in the <button class="btn link" id="lib2">font library</button> and they appear here within a minute.</div>`}</div>

    <h2><span>Recent activity</span></h2>
    <div>${S.events.length ? S.events.slice(0, 12).map((e) => `
      <div class="ev ${esc(e.kind)}"><i></i><span>${e.kind === 'added' ? 'Installed ' : e.kind === 'removed' ? 'Removed ' : ''}${esc(e.text)}</span><time>${clock(e.time)}</time></div>`).join('')
      : '<div class="empty">Nothing yet in this session.</div>'}</div>

    <h2><span>Settings</span></h2>
    <div class="set"><div class="grow">Start typehaus at login<div class="sub">Keeps your fonts in sync after a restart.</div></div>
      <label class="toggle"><input type="checkbox" id="auto" ${S.autostart ? 'checked' : ''}><span></span></label></div>
    <div class="set"><div class="grow">Disconnect this computer<div class="sub">Removes all fonts typehaus installed here.</div></div>
      <button class="btn danger" id="disc">Disconnect…</button></div>

    <div class="foot"><span>typehaus agent ${esc(S.version)} · ${esc(S.hostname)}</span><span>Device #${st.device_id}</span></div>`;

  $('#sync').onclick = async (e) => { e.target.disabled = true; await api('POST', '/api/sync').catch(() => {}); setTimeout(refresh, 300); };
  for (const id of ['#lib', '#lib2']) { const b = $(id); if (b) b.onclick = () => api('POST', '/api/open-library'); }
  $('#auto').onchange = async (e) => {
    try { await api('POST', '/api/autostart', { enabled: e.target.checked }); } catch (ex) { e.target.checked = !e.target.checked; alert(ex.message); }
  };
  $('#disc').onclick = confirmDisconnect;
  loadPreviews();
}

const loaded = new Map();
function loadPreviews() {
  document.querySelectorAll('.spec[data-sha]').forEach((el) => {
    const sha = el.dataset.sha;
    if (!loaded.has(sha)) {
      const ff = new FontFace(`f${sha.slice(0, 16)}`, `url(/font/${sha})`);
      loaded.set(sha, ff.load().then((f) => { document.fonts.add(f); return f.family; }).catch(() => null));
    }
    loaded.get(sha).then((fam) => { if (fam) el.style.fontFamily = `"${fam}", var(--serif)`; });
  });
}

function tickTimes() {
  document.querySelectorAll('[data-ago]').forEach((el) => { el.textContent = ago(el.dataset.ago); });
}

function confirmDisconnect() {
  const d = document.createElement('dialog');
  d.innerHTML = `<h3>Disconnect this computer?</h3>
    <p>All fonts typehaus installed on this computer will be removed. Your account and the font library are not affected – you can connect again any time.</p>
    <div id="derr"></div>
    <div class="actions" style="justify-content:flex-end"><button class="btn" value="cancel">Cancel</button><button class="btn primary" id="dok">Disconnect</button></div>`;
  document.body.append(d);
  d.querySelector('[value=cancel]').onclick = () => d.close();
  d.addEventListener('close', () => d.remove());
  $('#dok', d).onclick = async (e) => {
    e.target.disabled = true;
    try { await api('POST', '/api/disconnect'); d.close(); lastKey = ''; refresh(); }
    catch (ex) { $('#derr', d).innerHTML = `<div class="alert">${esc(ex.message)}</div>`; e.target.disabled = false; }
  };
  d.showModal();
}

// ---- connect -------------------------------------------------------------
function renderConnect(app) {
  const last = S.last_server || '';
  app.innerHTML = `${header()}
    <form class="connect" id="connect" autocomplete="off">
      <p class="intro">Connect this computer to your team's typehaus to get the fonts you switch on in the library – installed for you, no admin rights needed.</p>
      ${S.notice ? `<div class="notice">${esc(S.notice)}</div>` : ''}
      <div id="notice"></div>
      <label>Server address
        <input name="server" type="text" inputmode="url" placeholder="fonts.example.com" value="${esc(last)}" required>
      </label>
      <label>Enrollment code
        <input name="code" class="code" type="text" placeholder="XXXX-XXXX-XXXX" maxlength="14" required spellcheck="false">
        <span class="hint">In typehaus, open <b>Devices → Create enrollment code</b>. Codes work once and expire after 15 minutes.</span>
      </label>
      <label>Computer name
        <input name="name" type="text" value="${esc(S.hostname)}">
        <span class="hint">How this computer appears in your device list.</span>
      </label>
      <details class="adv"><summary>Advanced</summary>
        <label>Custom certificate authority <span class="hint">Only needed if your server uses a private or self-signed certificate that this computer doesn't trust. Paste the CA certificate (PEM).</span>
          <textarea name="ca" rows="5" spellcheck="false" placeholder="-----BEGIN CERTIFICATE-----"></textarea>
        </label>
      </details>
      <div id="err"></div>
      <div class="actions"><button class="btn primary" type="submit" id="go">Connect</button></div>
    </form>
    <div class="foot"><span>typehaus agent ${esc(S.version)}</span></div>`;
  updateNotice();
  const f = $('#connect');
  const code = f.code;
  code.addEventListener('input', () => {
    const raw = code.value.toUpperCase().replace(/[^A-Z0-9]/g, '').slice(0, 12);
    code.value = raw.match(/.{1,4}/g)?.join('-') || '';
  });
  (last ? code : f.server).focus();
  f.onsubmit = async (e) => {
    e.preventDefault();
    const go = $('#go');
    go.disabled = true; go.textContent = 'Connecting…';
    $('#err').innerHTML = '';
    let server = f.server.value.trim();
    if (!/^https?:\/\//i.test(server)) server = 'https://' + server;
    try {
      await api('POST', '/api/enroll', { server, code: code.value, name: f.name.value.trim(), ca_pem: f.ca.value });
      lastKey = '';
      $('#app').innerHTML = '';
      await refresh();
    } catch (ex) {
      $('#err').innerHTML = `<div class="alert">${esc(ex.message)}</div>`;
      go.disabled = false; go.textContent = 'Connect';
    }
  };
}

function updateNotice() {
  const n = $('#notice');
  if (n) n.innerHTML = S.status.detail ? `<div class="notice">${esc(S.status.detail)}</div>` : '';
}
// (the server address is remembered by the agent itself – browser storage
// would be lost because the window's port changes on every start)

const timer = setInterval(refresh, 2000);
refresh();

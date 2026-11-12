/* typehaus web UI – dependency-free. */
'use strict';

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------
const $ = (s, el = document) => el.querySelector(s);
const $$ = (s, el = document) => [...el.querySelectorAll(s)];
const ESC = { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' };
const esc = (s) => String(s ?? '').replace(/[&<>"']/g, (c) => ESC[c]);
const plural = (n, one, many) => `${n} ${n === 1 ? one : (many || one + 's')}`;

class ApiError extends Error {
  constructor(msg, status) { super(msg); this.status = status; }
}

async function api(method, path, body) {
  const opts = { method, headers: { 'X-Typehaus': '1' }, credentials: 'same-origin' };
  if (body instanceof FormData) opts.body = body;
  else if (body !== undefined) { opts.body = JSON.stringify(body); opts.headers['Content-Type'] = 'application/json'; }
  const r = await fetch(path, opts);
  let data = null;
  const text = await r.text();
  try { data = text ? JSON.parse(text) : null; } catch { data = null; }
  if (!r.ok) {
    if (r.status === 401 && S.me && !path.startsWith('/api/auth')) { S.me = null; render(); }
    throw new ApiError((data && data.error) || r.statusText, r.status);
  }
  return data;
}

function toast(msg, err = false) {
  const t = document.createElement('div');
  t.className = 'toast' + (err ? ' err' : '');
  t.textContent = msg;
  $('#toasts').append(t);
  setTimeout(() => t.remove(), err ? 6000 : 3200);
}
const fail = (e) => toast(e.message || String(e), true);

function ago(ts) {
  if (!ts) return 'never';
  const s = Math.floor(Date.now() / 1000 - ts);
  if (s < 60) return 'just now';
  if (s < 3600) return `${Math.floor(s / 60)} min ago`;
  if (s < 86400) return `${Math.floor(s / 3600)} h ago`;
  if (s < 86400 * 30) return `${Math.floor(s / 86400)} d ago`;
  return new Date(ts * 1000).toLocaleDateString();
}
const fmtDate = (ts) => new Date(ts * 1000).toLocaleString();
const fmtSize = (b) => b > 1048576 ? (b / 1048576).toFixed(1) + ' MB' : Math.max(1, Math.round(b / 1024)) + ' KB';

// ---------------------------------------------------------------------------
// state & routing
// ---------------------------------------------------------------------------
const S = {
  me: null,
  auth: null,
  lib: {
    fonts: [], collections: [], licenses: [], loaded: false,
    q: '', coll: 'all', preview: '', size: 40, expanded: new Set(), selected: new Set(),
  },
};

const ROUTES = {
  library: { title: 'Library', render: renderLibrary },
  devices: { title: 'Devices', render: renderDevices },
  collections: { title: 'Collections', render: renderCollections, admin: true },
  people: { title: 'People', render: renderPeople, admin: true },
  licenses: { title: 'Licenses', render: renderLicenses, admin: true },
  activity: { title: 'Activity', render: renderActivity, admin: true },
};

function route() {
  const r = location.hash.replace(/^#\/?/, '').split('?')[0] || 'library';
  return ROUTES[r] ? r : 'library';
}
window.addEventListener('hashchange', () => render());

async function boot() {
  try { S.auth = await api('GET', '/api/auth/config'); } catch { S.auth = { local_login: true }; }
  try { S.me = await api('GET', '/api/me'); } catch { S.me = null; }
  render();
}

function render() {
  const app = $('#app');
  if (!S.me) { renderLogin(app); return; }
  const r = route();
  const def = ROUTES[r];
  if (def.admin && !S.me.is_admin) { location.hash = '#/library'; return; }
  const nav = Object.entries(ROUTES)
    .filter(([, d]) => !d.admin || S.me.is_admin)
    .map(([k, d]) => `<a href="#/${k}" class="${k === r ? 'active' : ''}">${d.title}</a>`).join('');
  app.innerHTML = `
    <div class="shell">
      <header class="top">
        <a class="brand" href="#/library">typehaus<i></i></a>
        <nav class="main">${nav}</nav>
        <div class="who">
          <span>${esc(S.me.display_name || S.me.username)}${S.me.is_admin ? ' <span class="badge">admin</span>' : ''}</span>
          <button class="btn ghost sm" id="acct">Account</button>
          <button class="btn ghost sm" id="logout">Log out</button>
        </div>
      </header>
      <main id="page"></main>
    </div>`;
  $('#logout').onclick = async () => { await api('POST', '/api/auth/logout').catch(() => {}); S.me = null; S.lib.loaded = false; render(); };
  $('#acct').onclick = accountDialog;
  document.title = `${def.title} · typehaus`;
  def.render($('#page')).catch?.(fail);
}

// ---------------------------------------------------------------------------
// login
// ---------------------------------------------------------------------------
function renderLogin(app) {
  const params = new URLSearchParams(location.search);
  const err = params.get('login_error');
  const a = S.auth || {};
  app.innerHTML = `
    <div class="login">
      <div class="art">
        <div class="big">Aa<b>.</b><br>typehaus</div>
        <p>Your team's fonts, in one place. Activate them on every computer you use – no admin rights, no USB sticks, no licensing guesswork.</p>
      </div>
      <form id="loginform" autocomplete="on">
        <h1>Sign in</h1>
        ${err ? `<div class="alert">${esc(err)}</div>` : ''}
        <div id="loginerr"></div>
        ${a.local_login !== false ? `
          <label class="field">Username<input type="text" name="username" autocomplete="username" required autofocus></label>
          <label class="field">Password<input type="password" name="password" autocomplete="current-password" required></label>
          <button class="btn primary" type="submit">Sign in</button>` : ''}
        ${a.oidc ? `${a.local_login !== false ? '<div class="or">or</div>' : ''}
          <a class="btn ${a.local_login === false ? 'primary' : ''}" style="text-align:center;text-decoration:none" href="/auth/oidc/login">Continue with ${esc(a.oidc_name || 'SSO')}</a>` : ''}
        <p class="muted small">typehaus ${esc(a.version || '')}</p>
      </form>
    </div>`;
  const f = $('#loginform');
  f.onsubmit = async (e) => {
    e.preventDefault();
    const fd = new FormData(f);
    try {
      await api('POST', '/api/auth/login', { username: fd.get('username'), password: fd.get('password') });
      if (err) history.replaceState(null, '', '/' + location.hash);
      S.me = await api('GET', '/api/me');
      render();
    } catch (ex) {
      $('#loginerr').innerHTML = `<div class="alert">${esc(ex.message)}</div>`;
    }
  };
}

// ---------------------------------------------------------------------------
// dialogs
// ---------------------------------------------------------------------------
function dialog(title, body, { submit = 'Save', danger = false, onSubmit } = {}) {
  const d = document.createElement('dialog');
  d.innerHTML = `<form method="dialog"><h3>${esc(title)}</h3>${body}
    <div class="err"></div>
    <div class="actions"><button type="button" class="btn ghost" value="cancel">Cancel</button>
    <button type="submit" class="btn ${danger ? 'accent' : 'primary'}">${esc(submit)}</button></div></form>`;
  document.body.append(d);
  const form = $('form', d);
  $('[value=cancel]', d).onclick = () => d.close();
  d.addEventListener('close', () => d.remove());
  form.onsubmit = async (e) => {
    e.preventDefault();
    try {
      await onSubmit(new FormData(form), form);
      d.close();
    } catch (ex) {
      $('.err', d).innerHTML = `<div class="alert">${esc(ex.message)}</div>`;
    }
  };
  d.showModal();
  return d;
}

const confirmDialog = (title, text, onYes, submit = 'Delete') =>
  dialog(title, `<p>${text}</p>`, { submit, danger: true, onSubmit: onYes });

function accountDialog() {
  const needCurrent = S.me.has_password;
  dialog('Account', `
    <p class="muted small">Signed in as <b>${esc(S.me.username)}</b>. typehaus ${esc(S.me.version)}</p>
    ${needCurrent ? '<label class="field">Current password<input type="password" name="current" autocomplete="current-password" required></label>' : '<p class="small muted">You sign in with SSO. Setting a password also enables local login.</p>'}
    <label class="field">New password (min. 10 characters)<input type="password" name="new" minlength="10" autocomplete="new-password" required></label>`,
  {
    submit: 'Change password',
    onSubmit: async (fd) => {
      await api('POST', '/api/me/password', { current: fd.get('current') || '', new: fd.get('new') });
      S.me.has_password = true;
      toast('Password changed');
    },
  });
}

// ---------------------------------------------------------------------------
// font previews
// ---------------------------------------------------------------------------
const faceCache = new Map(); // id -> Promise<string|null>
function loadFace(id) {
  if (!faceCache.has(id)) {
    const fam = `thf${id}`;
    const ff = new FontFace(fam, `url(/api/fonts/${id}/file)`);
    faceCache.set(id, ff.load().then((f) => { document.fonts.add(f); return fam; }).catch(() => null));
  }
  return faceCache.get(id);
}
const previewObserver = new IntersectionObserver((entries) => {
  for (const en of entries) {
    if (!en.isIntersecting) continue;
    const el = en.target;
    previewObserver.unobserve(el);
    loadFace(+el.dataset.font).then((fam) => {
      el.classList.remove('loading');
      if (fam) el.style.fontFamily = `"${fam}", "Adobe NotDef", monospace`;
      else { el.classList.add('nopreview'); el.textContent = 'No browser preview for this format'; }
    });
  }
}, { rootMargin: '300px' });

// ---------------------------------------------------------------------------
// library
// ---------------------------------------------------------------------------
async function loadLibrary() {
  const L = S.lib;
  const reqs = [api('GET', '/api/fonts'), api('GET', '/api/collections')];
  if (S.me.is_admin) reqs.push(api('GET', '/api/licenses'));
  const [fonts, colls, lics] = await Promise.all(reqs);
  L.fonts = fonts; L.collections = colls; L.licenses = lics || []; L.loaded = true;
  const ids = new Set(fonts.map((f) => f.id));
  for (const id of [...L.selected]) if (!ids.has(id)) L.selected.delete(id);
}

function groupFamilies(fonts) {
  const m = new Map();
  for (const f of fonts) {
    if (!m.has(f.family)) m.set(f.family, []);
    m.get(f.family).push(f);
  }
  return [...m.entries()].map(([name, faces]) => {
    const rep = [...faces].sort((a, b) => (a.italic - b.italic) || (Math.abs(a.weight - 400) - Math.abs(b.weight - 400)))[0];
    return { name, faces, rep, active: faces.filter((f) => f.activated).length };
  });
}

function filteredFonts() {
  const L = S.lib;
  const q = L.q.trim().toLowerCase();
  return L.fonts.filter((f) => {
    if (L.coll === 'active' && !f.activated) return false;
    if (L.coll !== 'all' && L.coll !== 'active' && !f.collections.includes(+L.coll)) return false;
    if (q && !(`${f.family} ${f.full_name} ${f.postscript_name} ${f.designer} ${f.manufacturer}`.toLowerCase().includes(q))) return false;
    return true;
  });
}

async function renderLibrary(page) {
  const L = S.lib;
  if (!L.loaded) {
    page.innerHTML = '<div class="empty">Loading library…</div>';
    await loadLibrary();
  }
  const isAdmin = S.me.is_admin;
  const activeCount = L.fonts.filter((f) => f.activated).length;
  page.innerHTML = `
    <div class="lib">
      <aside class="side">
        <h1 style="font-size:28px">Library</h1>
        <p class="muted small" style="margin:0">${plural(groupFamilies(L.fonts).length, 'family', 'families')} · ${plural(L.fonts.length, 'style')}</p>
        <h4>Show</h4>
        <a href="#" data-coll="all" class="${L.coll === 'all' ? 'active' : ''}">All fonts <span class="n">${L.fonts.length}</span></a>
        <a href="#" data-coll="active" class="${L.coll === 'active' ? 'active' : ''}">Active for me <span class="n">${activeCount}</span></a>
        <h4>Collections</h4>
        ${L.collections.map((c) => `<a href="#" data-coll="${c.id}" class="${String(L.coll) === String(c.id) ? 'active' : ''}">${esc(c.name)} <span class="n">${c.font_count}</span></a>`).join('') || '<p class="muted small" style="padding:0 10px">None yet</p>'}
        ${isAdmin ? '<p style="padding:0 10px"><a href="#/collections" class="small" style="padding:0">Manage collections →</a></p>' : ''}
      </aside>
      <section>
        ${isAdmin ? `<div class="drop" id="drop">
          <b>Drop fonts here</b> or click to upload. TTF, OTF, TTC or ZIP archives.
          ${!['all', 'active'].includes(String(L.coll)) ? `<br><span class="small muted">Uploads are added to the selected collection.</span>` : ''}
          <input type="file" id="file" multiple accept=".ttf,.otf,.ttc,.otc,.zip" hidden></div>` : ''}
        <div class="toolbar">
          <input type="search" id="q" placeholder="Search fonts…" value="${esc(L.q)}">
          <input type="text" class="preview-text" id="pv" placeholder="Type to preview…" value="${esc(L.preview)}">
          <input type="range" id="sz" min="14" max="120" value="${L.size}" title="Preview size">
          <button class="btn sm" id="expandall">${L.expanded.size ? 'Collapse all' : 'Expand all'}</button>
        </div>
        <div id="bulk"></div>
        <div id="families"></div>
      </section>
    </div>`;

  $$('.side a[data-coll]', page).forEach((a) => a.onclick = (e) => { e.preventDefault(); L.coll = a.dataset.coll; L.selected.clear(); renderLibrary(page); });
  const q = $('#q', page);
  q.oninput = () => { L.q = q.value; drawFamilies(); };
  const pv = $('#pv', page);
  pv.oninput = () => { L.preview = pv.value; $$('.specimen:not(.nopreview)', page).forEach((el) => el.textContent = L.preview || el.dataset.def); };
  const sz = $('#sz', page);
  sz.oninput = () => { L.size = +sz.value; $$('.family > .specimen', page).forEach((el) => el.style.fontSize = L.size + 'px'); };
  $('#expandall', page).onclick = () => {
    if (L.expanded.size) L.expanded.clear();
    else filteredFonts().forEach((f) => L.expanded.add(f.family));
    renderLibrary(page);
  };
  if (isAdmin) setupUpload(page);
  drawFamilies();
}

function drawFamilies() {
  const L = S.lib;
  const box = $('#families');
  if (!box) return;
  const fams = groupFamilies(filteredFonts());
  if (!fams.length) {
    box.innerHTML = `<div class="empty"><div class="big">Nothing here yet.</div>${
      L.fonts.length ? 'No fonts match this view.' : (S.me.is_admin ? 'Upload fonts above to build your library.' : 'Ask an administrator to give you access to a collection.')}</div>`;
    drawBulk();
    return;
  }
  const admin = S.me.is_admin;
  box.innerHTML = fams.map((fam) => {
    const open = L.expanded.has(fam.name);
    const allSel = fam.faces.every((f) => L.selected.has(f.id));
    const lic = admin && fam.rep.license_id ? L.licenses.find((l) => l.id === fam.rep.license_id) : null;
    const restricted = fam.faces.some((f) => f.fs_type & 0x2 && !(f.fs_type & 0xc));
    return `<div class="family" data-fam="${esc(fam.name)}">
      <div class="family-head">
        ${admin ? `<input type="checkbox" class="selfam" ${allSel ? 'checked' : ''} title="Select family">` : ''}
        <label class="toggle" title="Activate all styles"><input type="checkbox" class="actfam" ${fam.active === fam.faces.length ? 'checked' : ''}><span></span></label>
        <span class="name">${esc(fam.name)}</span>
        <span class="meta">${plural(fam.faces.length, 'style')}${fam.active ? ` · <span class="badge on">${fam.active} active</span>` : ''}${lic ? ` · <span class="badge">${esc(lic.name)}</span>` : ''}${restricted ? ' · <span class="badge warn" title="OS/2 fsType: restricted license embedding">restricted</span>' : ''}</span>
        <span class="spacer"></span>
        <span class="meta small">${esc(fam.rep.manufacturer || fam.rep.designer || '')}</span>
        <button class="expand">${open ? 'Hide styles ▴' : 'Styles ▾'}</button>
      </div>
      <div class="specimen loading" data-font="${fam.rep.id}" data-def="${esc(fam.name)}" style="font-size:${L.size}px">${esc(L.preview || fam.name)}</div>
      ${open ? `<div class="styles">${fam.faces.map((f) => `
        <div class="style-row" data-id="${f.id}">
          ${admin ? `<input type="checkbox" class="selface" ${L.selected.has(f.id) ? 'checked' : ''}>` : '<span></span>'}
          <span class="sname" title="${esc(f.full_name)} · ${esc(f.postscript_name)} · v${esc(f.version)} · ${f.format.toUpperCase()} · ${fmtSize(f.size)}">${esc(f.style)} <span class="muted">${f.weight}</span></span>
          <div class="specimen loading" data-font="${f.id}" data-def="${esc(f.full_name)}">${esc(L.preview || f.full_name)}</div>
          <label class="toggle"><input type="checkbox" class="actface" ${f.activated ? 'checked' : ''}><span></span></label>
        </div>`).join('')}</div>` : ''}
    </div>`;
  }).join('');

  $$('.actfam', box).forEach((cb) => {
    const fam = fams.find((x) => x.name === cb.closest('.family').dataset.fam);
    cb.indeterminate = fam.active > 0 && fam.active < fam.faces.length;
    cb.onchange = () => setActive(fam.faces.map((f) => f.id), fam.active < fam.faces.length);
  });
  $$('.actface', box).forEach((cb) => cb.onchange = () => setActive([+cb.closest('.style-row').dataset.id], cb.checked));
  $$('.expand', box).forEach((b) => b.onclick = () => {
    const n = b.closest('.family').dataset.fam;
    L.expanded.has(n) ? L.expanded.delete(n) : L.expanded.add(n);
    drawFamilies();
  });
  $$('.selfam', box).forEach((cb) => cb.onchange = () => {
    const fam = fams.find((x) => x.name === cb.closest('.family').dataset.fam);
    fam.faces.forEach((f) => cb.checked ? L.selected.add(f.id) : L.selected.delete(f.id));
    drawFamilies();
  });
  $$('.selface', box).forEach((cb) => cb.onchange = () => {
    const id = +cb.closest('.style-row').dataset.id;
    cb.checked ? L.selected.add(id) : L.selected.delete(id);
    drawFamilies();
  });
  $$('.specimen', box).forEach((el) => previewObserver.observe(el));
  drawBulk();
}

async function setActive(ids, on) {
  try {
    await api(on ? 'POST' : 'DELETE', '/api/activations', { font_ids: ids });
    for (const f of S.lib.fonts) if (ids.includes(f.id)) f.activated = on;
    toast(on ? `Activated ${plural(ids.length, 'style')} – your devices sync shortly` : `Deactivated ${plural(ids.length, 'style')}`);
  } catch (e) { fail(e); }
  const page = $('#page');
  if (page && route() === 'library') renderLibrary(page);
}

function drawBulk() {
  const L = S.lib;
  const box = $('#bulk');
  if (!box || !S.me.is_admin) return;
  const n = L.selected.size;
  if (!n) { box.innerHTML = ''; return; }
  const inColl = !['all', 'active'].includes(String(L.coll));
  box.innerHTML = `<div class="bulk">
    <b>${plural(n, 'style')} selected</b>
    <select id="b-coll"><option value="">Add to collection…</option>${L.collections.map((c) => `<option value="${c.id}">${esc(c.name)}</option>`).join('')}</select>
    ${inColl ? '<button class="btn sm" id="b-rm">Remove from this collection</button>' : ''}
    <select id="b-lic"><option value="">Assign license…</option><option value="0">— No license —</option>${L.licenses.map((l) => `<option value="${l.id}">${esc(l.name)}</option>`).join('')}</select>
    <button class="btn sm" id="b-del">Delete</button>
    <span class="spacer"></span>
    <button class="btn sm" id="b-clear">Clear selection</button></div>`;
  const ids = [...L.selected];
  const reload = async (msg) => { toast(msg); L.loaded = false; await renderLibrary($('#page')); };
  $('#b-clear').onclick = () => { L.selected.clear(); drawFamilies(); };
  $('#b-coll').onchange = async (e) => {
    if (!e.target.value) return;
    try { await api('POST', `/api/collections/${e.target.value}/fonts`, { font_ids: ids }); await reload('Added to collection'); } catch (ex) { fail(ex); }
  };
  if (inColl) $('#b-rm').onclick = async () => {
    try { await api('DELETE', `/api/collections/${L.coll}/fonts`, { font_ids: ids }); L.selected.clear(); await reload('Removed from collection'); } catch (ex) { fail(ex); }
  };
  $('#b-lic').onchange = async (e) => {
    if (e.target.value === '') return;
    try { await api('PATCH', '/api/fonts', { font_ids: ids, license_id: +e.target.value }); await reload('License assigned'); } catch (ex) { fail(ex); }
  };
  $('#b-del').onclick = () => confirmDialog('Delete fonts',
    `Permanently delete ${plural(n, 'style')} from the library? They will be removed from every device on its next sync.`,
    async () => { await api('DELETE', '/api/fonts', { font_ids: ids }); L.selected.clear(); await reload('Deleted'); });
}

function setupUpload(page) {
  const drop = $('#drop', page);
  const input = $('#file', page);
  drop.onclick = () => input.click();
  input.onchange = () => upload([...input.files]);
  ['dragenter', 'dragover'].forEach((ev) => drop.addEventListener(ev, (e) => { e.preventDefault(); drop.classList.add('over'); }));
  ['dragleave', 'drop'].forEach((ev) => drop.addEventListener(ev, (e) => { e.preventDefault(); drop.classList.remove('over'); }));
  drop.addEventListener('drop', (e) => upload([...e.dataTransfer.files]));
}

async function upload(files) {
  if (!files.length) return;
  const L = S.lib;
  const fd = new FormData();
  if (!['all', 'active'].includes(String(L.coll))) fd.append('collection_id', L.coll);
  files.forEach((f) => fd.append('files', f, f.name));
  const drop = $('#drop');
  if (drop) drop.innerHTML = `<b>Uploading ${plural(files.length, 'file')}…</b>`;
  try {
    const res = await api('POST', '/api/fonts', fd);
    const r = res.results || [];
    const added = r.filter((x) => x.status === 'added').length;
    const dup = r.filter((x) => x.status === 'duplicate').length;
    const errs = r.filter((x) => x.status === 'error');
    toast(`${added} added${dup ? `, ${dup} already in library` : ''}${errs.length ? `, ${errs.length} failed` : ''}`);
    errs.slice(0, 5).forEach((x) => toast(`${x.file}: ${x.message}`, true));
  } catch (e) { fail(e); }
  L.loaded = false;
  renderLibrary($('#page'));
}

// ---------------------------------------------------------------------------
// devices
// ---------------------------------------------------------------------------
async function renderDevices(page) {
  const showAll = S.me.is_admin && new URLSearchParams(location.hash.split('?')[1] || '').get('all') === '1';
  const [devices, downloads] = await Promise.all([
    api('GET', '/api/devices' + (showAll ? '?all=1' : '')),
    api('GET', '/api/downloads').catch(() => []),
  ]);
  page.innerHTML = `
    <h1>Devices</h1>
    <p class="sub">The typehaus agent keeps the fonts you activate installed on your computers – for your user account only, no administrator rights needed.</p>
    <div class="grid2">
      <div class="card">
        <h2 style="margin-top:0">Add a computer</h2>
        <ol class="small" style="padding-left:18px;color:var(--ink-2)">
          <li>Download the agent for your system.</li>
          <li>Create an enrollment code (valid for 15 minutes, single use).</li>
          <li>Run the commands shown below in a terminal.</li>
        </ol>
        <div id="dl" class="row" style="margin:12px 0"></div>
        <button class="btn primary" id="mkcode">Create enrollment code</button>
        <div id="codebox"></div>
      </div>
      <div class="card">
        <h2 style="margin-top:0">How it works</h2>
        <p class="small" style="color:var(--ink-2)">Turn fonts on in the <a href="#/library">Library</a>. Every enrolled computer picks up the change within about a minute and installs the fonts for your user. Turn them off and they disappear again. If access is revoked, the agent removes the fonts it installed.</p>
        <p class="small muted">Windows: per-user font registration (Windows 10 1809+). macOS: ~/Library/Fonts. Linux: ~/.local/share/fonts via fontconfig.</p>
      </div>
    </div>
    <div class="row" style="margin-top:28px"><h2 style="margin:0">${showAll ? 'All devices' : 'Your devices'}</h2><span class="spacer"></span>
      ${S.me.is_admin ? `<a class="btn sm" href="#/devices${showAll ? '' : '?all=1'}">${showAll ? 'Show only mine' : 'Show all users'}</a>` : ''}</div>
    ${devices.length ? `<table class="t" style="margin-top:12px"><thead><tr>
      <th>Device</th>${showAll ? '<th>User</th>' : ''}<th>System</th><th>Last seen</th><th class="num">Fonts</th><th>Agent</th><th></th></tr></thead><tbody>
      ${devices.map((d) => {
        const fresh = d.last_seen && Date.now() / 1000 - d.last_seen < 600;
        return `<tr><td><span class="dot ${fresh ? 'ok' : 'warn'}"></span><b>${esc(d.name)}</b>${d.hostname && d.hostname !== d.name ? ` <span class="muted small">${esc(d.hostname)}</span>` : ''}</td>
        ${showAll ? `<td>${esc(d.username)}</td>` : ''}<td>${esc(d.platform)}</td>
        <td title="${d.last_seen ? fmtDate(d.last_seen) : ''}${d.last_ip ? ' · ' + esc(d.last_ip) : ''}">${ago(d.last_seen)}</td>
        <td class="num">${d.installed}</td><td class="mono">${esc(d.agent_version || '–')}</td>
        <td style="text-align:right"><button class="btn sm danger" data-revoke="${d.id}" data-name="${esc(d.name)}">Revoke</button></td></tr>`;
      }).join('')}</tbody></table>` : '<div class="empty">No devices enrolled yet.</div>'}`;

  const dl = $('#dl', page);
  const visible = downloads.filter((d) => !d.headless);
  if (visible.length) {
    const label = { windows: 'Windows', darwin: 'macOS', linux: 'Linux' };
    const archL = { amd64: 'x64', arm64: 'ARM' };
    dl.innerHTML = visible.map((d) => `<a class="btn sm" href="${esc(d.url)}" download>${label[d.os] || esc(d.os)} ${archL[d.arch] || esc(d.arch)}</a>`).join('');
    const hl = downloads.filter((d) => d.headless);
    if (hl.length) dl.insertAdjacentHTML('beforeend', `<span class="small muted">Windows: also get ${hl.map((d) => `<a href="${esc(d.url)}" download>${esc(d.name)}</a>`).join(', ')} (runs without a console window) and keep it in the same folder.</span>`);
  } else {
    dl.innerHTML = '<span class="small muted">Agent binaries are available on the <a href="https://github.com/4nonX/typehaus/releases/latest" target="_blank" rel="noopener">GitHub releases page</a>.</span>';
  }

  $('#mkcode', page).onclick = async () => {
    try {
      const c = await api('POST', '/api/enroll-codes');
      const bin = { windows: '.\\typehaus-agent-windows-amd64.exe', darwin: './typehaus-agent-darwin-arm64', linux: './typehaus-agent-linux-amd64' };
      const cmds = {
        windows: `${bin.windows} enroll --server ${c.server} --code ${c.code}\n${bin.windows} autostart enable`,
        darwin: `chmod +x typehaus-agent-darwin-arm64\nxattr -d com.apple.quarantine typehaus-agent-darwin-arm64 2>/dev/null\n${bin.darwin} enroll --server ${c.server} --code ${c.code}\n${bin.darwin} autostart enable`,
        linux: `chmod +x typehaus-agent-linux-amd64\n${bin.linux} enroll --server ${c.server} --code ${c.code}\n${bin.linux} autostart enable`,
      };
      const guess = /Win/.test(navigator.platform) ? 'windows' : /Mac/.test(navigator.platform) ? 'darwin' : 'linux';
      const box = $('#codebox', page);
      box.innerHTML = `<div style="margin-top:16px"><div class="enroll-code">${esc(c.code)}</div>
        <p class="small muted">Expires ${new Date(c.expires_at * 1000).toLocaleTimeString()}.</p>
        <div class="tabs">${['windows', 'darwin', 'linux'].map((o) => `<button data-os="${o}" class="${o === guess ? 'active' : ''}">${{ windows: 'Windows', darwin: 'macOS', linux: 'Linux' }[o]}</button>`).join('')}</div>
        <div class="code" id="cmd"></div>
        <div class="row" style="margin-top:8px"><button class="btn sm" id="copy">Copy commands</button><span class="small muted">Adjust the file name if you downloaded another architecture.</span></div></div>`;
      const show = (o) => { $('#cmd', box).textContent = cmds[o]; $$('.tabs button', box).forEach((b) => b.classList.toggle('active', b.dataset.os === o)); };
      $$('.tabs button', box).forEach((b) => b.onclick = () => show(b.dataset.os));
      $('#copy', box).onclick = () => navigator.clipboard.writeText($('#cmd', box).textContent).then(() => toast('Copied'), fail);
      show(guess);
    } catch (e) { fail(e); }
  };
  $$('[data-revoke]', page).forEach((b) => b.onclick = () => confirmDialog('Revoke device',
    `Revoke <b>${esc(b.dataset.name)}</b>? On its next sync the agent removes all fonts it installed and stops.`,
    async () => { await api('DELETE', `/api/devices/${b.dataset.revoke}`); toast('Device revoked'); renderDevices(page); }, 'Revoke'));
}

// ---------------------------------------------------------------------------
// collections (admin)
// ---------------------------------------------------------------------------
async function renderCollections(page) {
  const [colls, groups] = await Promise.all([api('GET', '/api/collections'), api('GET', '/api/groups')]);
  const gname = (id) => groups.find((g) => g.id === id)?.name || '?';
  page.innerHTML = `
    <div class="row"><div><h1>Collections</h1><p class="sub">Collections decide who can see and activate which fonts. Add fonts to collections from the Library.</p></div>
      <span class="spacer"></span><button class="btn primary" id="new">New collection</button></div>
    ${colls.length ? `<table class="t"><thead><tr><th>Name</th><th>Access</th><th class="num">Styles</th><th></th></tr></thead><tbody>
      ${colls.map((c) => `<tr><td><b>${esc(c.name)}</b>${c.description ? `<div class="small muted">${esc(c.description)}</div>` : ''}</td>
        <td>${c.everyone ? '<span class="badge on">Everyone</span>' : (c.group_ids.length ? c.group_ids.map((g) => `<span class="badge">${esc(gname(g))}</span>`).join(' ') : '<span class="muted small">Admins only</span>')}</td>
        <td class="num"><a href="#/library" data-open="${c.id}">${c.font_count}</a></td>
        <td style="text-align:right"><button class="btn sm" data-edit="${c.id}">Edit</button> <button class="btn sm danger" data-del="${c.id}">Delete</button></td></tr>`).join('')}
      </tbody></table>` : '<div class="empty"><div class="big">No collections yet.</div>Create one to share fonts with your team.</div>'}`;
  const edit = (c) => dialog(c ? 'Edit collection' : 'New collection', `
    <label class="field">Name<input type="text" name="name" required value="${esc(c?.name)}"></label>
    <label class="field">Description<input type="text" name="description" value="${esc(c?.description)}"></label>
    <label class="check"><input type="checkbox" name="everyone" ${c?.everyone ? 'checked' : ''}> Visible to everyone</label>
    <div class="field small">Or only to these groups${groups.length ? '' : ' <span class="muted">(create groups under People)</span>'}
      <div class="checks">${groups.map((g) => `<label class="check"><input type="checkbox" name="g" value="${g.id}" ${c?.group_ids.includes(g.id) ? 'checked' : ''}> ${esc(g.name)}</label>`).join('') || '<span class="muted">No groups</span>'}</div></div>`,
  {
    onSubmit: async (fd) => {
      const body = { name: fd.get('name'), description: fd.get('description'), everyone: fd.get('everyone') === 'on', group_ids: fd.getAll('g').map(Number) };
      if (c) await api('PATCH', `/api/collections/${c.id}`, body); else await api('POST', '/api/collections', body);
      S.lib.loaded = false;
      renderCollections(page);
    },
  });
  $('#new', page).onclick = () => edit(null);
  $$('[data-edit]', page).forEach((b) => b.onclick = () => edit(colls.find((c) => c.id === +b.dataset.edit)));
  $$('[data-open]', page).forEach((a) => a.onclick = () => { S.lib.coll = a.dataset.open; });
  $$('[data-del]', page).forEach((b) => b.onclick = () => {
    const c = colls.find((x) => x.id === +b.dataset.del);
    confirmDialog('Delete collection', `Delete <b>${esc(c.name)}</b>? The fonts stay in the library, but users who only had access through this collection lose them.`,
      async () => { await api('DELETE', `/api/collections/${c.id}`); S.lib.loaded = false; S.lib.coll = 'all'; renderCollections(page); });
  });
}

// ---------------------------------------------------------------------------
// people (admin)
// ---------------------------------------------------------------------------
async function renderPeople(page) {
  const [users, groups] = await Promise.all([api('GET', '/api/users'), api('GET', '/api/groups')]);
  const gname = (id) => groups.find((g) => g.id === id)?.name || '?';
  page.innerHTML = `
    <div class="row"><div><h1>People</h1><p class="sub">Users sign in with a local password${S.auth?.oidc ? ' or single sign-on' : ''}. Groups grant access to collections.</p></div>
      <span class="spacer"></span><button class="btn" id="newg">New group</button><button class="btn primary" id="newu">New user</button></div>
    <table class="t"><thead><tr><th>User</th><th>Groups</th><th>Sign-in</th><th class="num">Active</th><th class="num">Devices</th><th></th></tr></thead><tbody>
    ${users.map((u) => `<tr style="${u.disabled ? 'opacity:.5' : ''}"><td><b>${esc(u.username)}</b>${u.display_name && u.display_name !== u.username ? ` <span class="muted">${esc(u.display_name)}</span>` : ''}
        ${u.is_admin ? ' <span class="badge">admin</span>' : ''}${u.disabled ? ' <span class="badge warn">disabled</span>' : ''}${u.email ? `<div class="small muted">${esc(u.email)}</div>` : ''}</td>
      <td>${u.group_ids.map((g) => `<span class="badge">${esc(gname(g))}</span>`).join(' ') || '<span class="muted small">–</span>'}</td>
      <td class="small">${[u.has_password && 'password', u.sso && 'SSO'].filter(Boolean).join(' + ') || '<span class="muted">none</span>'}</td>
      <td class="num">${u.active_fonts}</td><td class="num">${u.devices}</td>
      <td style="text-align:right"><button class="btn sm" data-edit="${u.id}">Edit</button>${u.id !== S.me.id ? ` <button class="btn sm danger" data-del="${u.id}">Delete</button>` : ''}</td></tr>`).join('')}
    </tbody></table>
    <h2>Groups</h2>
    ${groups.length ? `<table class="t"><thead><tr><th>Group</th><th>Description</th><th class="num">Members</th><th></th></tr></thead><tbody>
      ${groups.map((g) => `<tr><td><b>${esc(g.name)}</b></td><td class="muted">${esc(g.description)}</td><td class="num">${g.members}</td>
      <td style="text-align:right"><button class="btn sm" data-gedit="${g.id}">Edit</button> <button class="btn sm danger" data-gdel="${g.id}">Delete</button></td></tr>`).join('')}
    </tbody></table>` : '<p class="muted">No groups yet.</p>'}
    ${S.auth?.oidc ? '<p class="small muted">With TYPEHAUS_OIDC_GROUPS_CLAIM set, SSO users are placed into groups whose names match their identity provider groups at every login.</p>' : ''}`;

  const editUser = (u) => dialog(u ? `Edit ${u.username}` : 'New user', `
    <label class="field">Username<input type="text" name="username" required value="${esc(u?.username)}" autocomplete="off"></label>
    <label class="field">Display name<input type="text" name="display_name" value="${esc(u?.display_name)}"></label>
    <label class="field">Email<input type="email" name="email" value="${esc(u?.email)}"></label>
    <label class="field">${u ? 'New password (leave empty to keep)' : 'Password (min. 10 characters; leave empty for SSO-only)'}<input type="password" name="password" minlength="10" autocomplete="new-password"></label>
    <div class="field small">Groups<div class="checks">${groups.map((g) => `<label class="check"><input type="checkbox" name="g" value="${g.id}" ${u?.group_ids.includes(g.id) ? 'checked' : ''}> ${esc(g.name)}</label>`).join('') || '<span class="muted">No groups yet</span>'}</div></div>
    <label class="check"><input type="checkbox" name="is_admin" ${u?.is_admin ? 'checked' : ''} ${u?.id === S.me.id ? 'disabled' : ''}> Administrator</label>
    ${u ? `<label class="check"><input type="checkbox" name="disabled" ${u.disabled ? 'checked' : ''} ${u.id === S.me.id ? 'disabled' : ''}> Disabled (signs out, devices stop syncing)</label>` : ''}`,
  {
    onSubmit: async (fd) => {
      const body = { username: fd.get('username'), display_name: fd.get('display_name'), email: fd.get('email'), group_ids: fd.getAll('g').map(Number) };
      if (u?.id !== S.me.id) body.is_admin = fd.get('is_admin') === 'on';
      if (u && u.id !== S.me.id) body.disabled = fd.get('disabled') === 'on';
      if (fd.get('password')) body.password = fd.get('password');
      if (u) await api('PATCH', `/api/users/${u.id}`, body); else await api('POST', '/api/users', body);
      renderPeople(page);
    },
  });
  const editGroup = (g) => dialog(g ? 'Edit group' : 'New group', `
    <label class="field">Name<input type="text" name="name" required value="${esc(g?.name)}"></label>
    <label class="field">Description<input type="text" name="description" value="${esc(g?.description)}"></label>`,
  {
    onSubmit: async (fd) => {
      const body = { name: fd.get('name'), description: fd.get('description') };
      if (g) await api('PATCH', `/api/groups/${g.id}`, body); else await api('POST', '/api/groups', body);
      renderPeople(page);
    },
  });
  $('#newu', page).onclick = () => editUser(null);
  $('#newg', page).onclick = () => editGroup(null);
  $$('[data-edit]', page).forEach((b) => b.onclick = () => editUser(users.find((u) => u.id === +b.dataset.edit)));
  $$('[data-gedit]', page).forEach((b) => b.onclick = () => editGroup(groups.find((g) => g.id === +b.dataset.gedit)));
  $$('[data-del]', page).forEach((b) => b.onclick = () => {
    const u = users.find((x) => x.id === +b.dataset.del);
    confirmDialog('Delete user', `Delete <b>${esc(u.username)}</b>? Their devices are revoked and remove all typehaus fonts on the next sync.`,
      async () => { await api('DELETE', `/api/users/${u.id}`); renderPeople(page); });
  });
  $$('[data-gdel]', page).forEach((b) => b.onclick = () => {
    const g = groups.find((x) => x.id === +b.dataset.gdel);
    confirmDialog('Delete group', `Delete <b>${esc(g.name)}</b>? Members lose access to collections shared only with this group.`,
      async () => { await api('DELETE', `/api/groups/${g.id}`); S.lib.loaded = false; renderPeople(page); });
  });
}

// ---------------------------------------------------------------------------
// licenses (admin)
// ---------------------------------------------------------------------------
async function renderLicenses(page) {
  const [lics, usage] = await Promise.all([api('GET', '/api/licenses'), api('GET', '/api/reports/usage')]);
  const meter = (used, seats) => seats ? `<span class="meter ${used > seats ? 'over' : ''}"><i style="width:${Math.min(100, used / seats * 100)}%"></i></span> ${used} / ${seats}` : `${used} / ∞`;
  page.innerHTML = `
    <div class="row"><div><h1>Licenses</h1><p class="sub">Record what you bought and see how many people actually use it. Seats are counted per user with at least one active style.</p></div>
      <span class="spacer"></span><button class="btn primary" id="new">New license</button></div>
    ${lics.length ? `<table class="t"><thead><tr><th>License</th><th>Vendor</th><th>Seats used</th><th class="num">Styles</th><th class="num">Devices</th><th></th></tr></thead><tbody>
      ${lics.map((l) => `<tr><td><b>${esc(l.name)}</b>${l.notes ? `<div class="small muted">${esc(l.notes)}</div>` : ''}</td><td>${esc(l.vendor)}</td>
        <td>${meter(l.users, l.seats)}${l.seats && l.users > l.seats ? ' <span class="badge warn">over</span>' : ''}</td>
        <td class="num">${l.fonts}</td><td class="num">${l.devices}</td>
        <td style="text-align:right"><button class="btn sm" data-edit="${l.id}">Edit</button> <button class="btn sm danger" data-del="${l.id}">Delete</button></td></tr>`).join('')}
    </tbody></table>` : '<p class="muted">No licenses recorded. Create one, then assign it to fonts in the Library (select fonts → Assign license).</p>'}
    <h2>Usage by family</h2>
    <table class="t"><thead><tr><th>Family</th><th>License</th><th class="num">Styles</th><th>Active users</th><th class="num">Devices</th></tr></thead><tbody>
    ${usage.map((u) => `<tr><td><b>${esc(u.family)}</b></td><td>${u.license ? esc(u.license) : '<span class="muted small">unassigned</span>'}</td>
      <td class="num">${u.faces}</td><td title="${esc(u.user_names)}">${u.users}${u.user_names ? ` <span class="small muted">${esc(u.user_names.split(',').slice(0, 4).join(', '))}${u.user_names.split(',').length > 4 ? '…' : ''}</span>` : ''}</td>
      <td class="num">${u.devices}</td></tr>`).join('') || '<tr><td colspan="5" class="muted">Library is empty.</td></tr>'}
    </tbody></table>`;
  const edit = (l) => dialog(l ? 'Edit license' : 'New license', `
    <label class="field">Name<input type="text" name="name" required value="${esc(l?.name)}" placeholder="e.g. Brand family – desktop, 10 seats"></label>
    <label class="field">Vendor / foundry<input type="text" name="vendor" value="${esc(l?.vendor)}"></label>
    <label class="field">Seats (0 = unlimited)<input type="number" name="seats" min="0" value="${l?.seats ?? 0}"></label>
    <label class="field">Notes<textarea name="notes" rows="3" placeholder="Invoice number, renewal date, restrictions…">${esc(l?.notes)}</textarea></label>`,
  {
    onSubmit: async (fd) => {
      const body = { name: fd.get('name'), vendor: fd.get('vendor'), seats: +fd.get('seats') || 0, notes: fd.get('notes') };
      if (l) await api('PATCH', `/api/licenses/${l.id}`, body); else await api('POST', '/api/licenses', body);
      S.lib.loaded = false;
      renderLicenses(page);
    },
  });
  $('#new', page).onclick = () => edit(null);
  $$('[data-edit]', page).forEach((b) => b.onclick = () => edit(lics.find((l) => l.id === +b.dataset.edit)));
  $$('[data-del]', page).forEach((b) => b.onclick = () => {
    const l = lics.find((x) => x.id === +b.dataset.del);
    confirmDialog('Delete license', `Delete <b>${esc(l.name)}</b>? Fonts stay in the library and become unassigned.`,
      async () => { await api('DELETE', `/api/licenses/${l.id}`); S.lib.loaded = false; renderLicenses(page); });
  });
}

// ---------------------------------------------------------------------------
// activity (admin)
// ---------------------------------------------------------------------------
async function renderActivity(page) {
  page.innerHTML = `<h1>Activity</h1><p class="sub">Sign-ins, uploads, activations, enrollments and admin changes.</p>
    <table class="t"><thead><tr><th>When</th><th>Who</th><th>Action</th><th>Details</th></tr></thead><tbody id="log"></tbody></table>
    <p><button class="btn" id="more">Load more</button></p>`;
  let before = 0;
  const more = async () => {
    const rows = await api('GET', `/api/audit?limit=100${before ? `&before=${before}` : ''}`);
    $('#log', page).insertAdjacentHTML('beforeend', rows.map((e) =>
      `<tr><td class="small" title="${fmtDate(e.ts)}">${ago(e.ts)}</td><td>${esc(e.actor)}</td><td class="mono">${esc(e.action)}</td><td class="small">${esc(e.detail)}</td></tr>`).join(''));
    if (rows.length) before = rows[rows.length - 1].id;
    if (rows.length < 100) $('#more', page).remove();
  };
  $('#more', page).onclick = () => more().catch(fail);
  await more();
}

boot();

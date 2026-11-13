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
// Preview size in points (CSS pt: 1/72 inch, as design apps show text at
// 100 %), remembered per browser.
const SIZE_MIN = 4, SIZE_MAX = 400;
function savedSize() {
  try { const v = parseFloat(localStorage.getItem('typehaus.size')); if (v >= SIZE_MIN && v <= SIZE_MAX) return v; } catch {}
  return 30;
}

const S = {
  me: null,
  auth: null,
  lib: {
    fonts: [], collections: [], licenses: [], loaded: false,
    q: '', coll: 'all', preview: '', size: savedSize(), expanded: new Set(), selected: new Set(),
  },
};

const ROUTES = {
  library: { title: 'Library', render: renderLibrary },
  devices: { title: 'Devices', render: renderDevices },
  collections: { title: 'Collections', render: renderCollections, admin: true },
  people: { title: 'People', render: renderPeople, admin: true },
  licenses: { title: 'Licenses', render: renderLicenses, admin: true },
  requests: { title: 'Requests', render: renderRequests, admin: true },
  activity: { title: 'Activity', render: renderActivity, admin: true },
};

function route() {
  const r = location.hash.replace(/^#\/?/, '').split('?')[0] || 'library';
  return ROUTES[r] ? r : 'library';
}
window.addEventListener('hashchange', () => render());

// Theme switch: System → Light → Dark, remembered per browser (theme.js).
const THEMES = [
  { v: '', label: 'System theme', svg: '<circle cx="8" cy="8" r="6"/><path d="M8 2v12a6 6 0 0 0 0-12z" fill="currentColor"/>' },
  { v: 'light', label: 'Light theme', svg: '<circle cx="8" cy="8" r="3"/><path d="M8 1v2M8 13v2M1 8h2M13 8h2M3 3l1.4 1.4M11.6 11.6L13 13M3 13l1.4-1.4M11.6 4.4L13 3"/>' },
  { v: 'dark', label: 'Dark theme', svg: '<path d="M13 9.5A5.5 5.5 0 0 1 6.5 3a5.5 5.5 0 1 0 6.5 6.5z"/>' },
];
function setupThemeButton(btn) {
  const T = window.typehausTheme || { get: () => '', set: () => {} };
  const show = () => {
    const t = THEMES.find((x) => x.v === T.get()) || THEMES[0];
    btn.innerHTML = `<svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round">${t.svg}</svg>`;
    btn.title = `${t.label} (click to switch)`;
    btn.setAttribute('aria-label', btn.title);
  };
  btn.onclick = () => {
    const i = THEMES.findIndex((x) => x.v === T.get());
    T.set(THEMES[(i + 1) % THEMES.length].v);
    show();
  };
  show();
}

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
          <button class="btn ghost sm theme-btn" id="theme"></button>
          <button class="btn ghost sm" id="acct">Account</button>
          <button class="btn ghost sm" id="logout">Log out</button>
        </div>
      </header>
      <main id="page"></main>
    </div>`;
  setupThemeButton($('#theme'));
  $('#logout').onclick = async () => { await api('POST', '/api/auth/logout').catch(() => {}); S.me = null; S.lib.loaded = false; render(); };
  $('#acct').onclick = accountDialog;
  document.title = `${def.title} · typehaus`;
  updateNavDot();
  updateRequestsCount();
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
function dialog(title, body, { submit = 'Save', danger = false, onSubmit, cancel = true, wide = false } = {}) {
  const d = document.createElement('dialog');
  if (wide) d.classList.add('wide');
  d.innerHTML = `<form method="dialog"><h3>${esc(title)}</h3>${body}
    <div class="err"></div>
    <div class="actions">${cancel ? '<button type="button" class="btn ghost" value="cancel">Cancel</button>' : ''}
    <button type="submit" class="btn ${danger ? 'accent' : 'primary'}">${esc(submit)}</button></div></form>`;
  document.body.append(d);
  const form = $('form', d);
  const c = $('[value=cancel]', d);
  if (c) c.onclick = () => d.close();
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
  const [fonts, colls, lics, reqs] = await Promise.all([api('GET', '/api/fonts'), api('GET', '/api/collections'), api('GET', '/api/licenses'),
    api('GET', S.me.is_admin ? '/api/requests' : '/api/requests?mine=1')]);
  L.fonts = fonts; L.collections = colls; L.licenses = lics || []; L.loaded = true;
  L.requests = reqs || [];
  updateRequestsCount();
  updateNavDot();
  const ids = new Set(fonts.map((f) => f.id));
  for (const id of [...L.selected]) if (!ids.has(id)) L.selected.delete(id);
}

const KIND_LABEL = { type1: 'Type 1', winfnt: 'Windows bitmap', pcf: 'X11 bitmap', bdf: 'X11 bitmap' };
function platformNames(p) {
  const n = { windows: 'Windows', darwin: 'macOS', linux: 'Linux' };
  return p.split(',').map((x) => n[x] || x).join(' & ');
}

// Families are grouped ignoring case and spacing. Optical sizes are named as
// separate families in the font files ("DM Sans 18pt", "Minion Pro Display");
// they are listed under their base family when that family is in the library,
// and their style rows show the size (f.opsz).
const OPSZ = /\s+(\d+(?:[.,]\d+)?\s?pt|caption|text|display|subhead|deck|poster|banner|micro|small|fine|headline)$/i;
const famKey = (n) => n.toLowerCase().replace(/\s+/g, ' ').trim();
function groupFamilies(fonts) {
  const all = new Map(); // key -> spelling, over the whole library so a filter never splits a family
  for (const f of S.lib.fonts || fonts) if (!all.has(famKey(f.family))) all.set(famKey(f.family), f.family.trim());
  const m = new Map();
  for (const f of fonts) {
    let key = famKey(f.family);
    f.opsz = '';
    const sz = f.names_manual ? null : key.match(OPSZ);
    if (sz && all.has(key.slice(0, sz.index))) {
      f.opsz = f.family.trim().slice(sz.index).trim();
      key = key.slice(0, sz.index);
    }
    if (!m.has(key)) m.set(key, []);
    m.get(key).push(f);
  }
  return [...m.entries()].map(([key, faces]) => {
    const rep = [...faces].sort((a, b) => (!!a.opsz - !!b.opsz) || (a.italic - b.italic) || (Math.abs(a.weight - 400) - Math.abs(b.weight - 400)))[0];
    return { name: all.get(key) || rep.family, faces, rep, active: faces.filter(isOn).length, required: faces.filter((f) => f.required).length };
  });
}

function filteredFonts() {
  const L = S.lib;
  const q = L.q.trim().toLowerCase();
  return L.fonts.filter((f) => {
    if (L.coll === 'active' && !isOn(f)) return false;
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
  const activeCount = L.fonts.filter(isOn).length;
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
        <h4>Missing a font?</h4>
        <a href="#" id="reqfont">Request a font${myAnswers().length ? ` <span class="n"><span class="badge on">${myAnswers().length} new answer${myAnswers().length > 1 ? 's' : ''}</span></span>` : ''}</a>
      </aside>
      <section>
        ${isAdmin ? `<div class="drop" id="drop">
          <b>Drop fonts here</b> or click to upload. Any format: TrueType, OpenType, WOFF/WOFF2, Mac suitcases, PostScript Type 1 (with .pfm/.afm), Windows .fon, X11 bitmap fonts – or ZIP archives and whole font folders.
          ${!['all', 'active'].includes(String(L.coll)) ? `<br><span class="small muted">Uploads are added to the selected collection.</span>` : ''}
          <input type="file" id="file" multiple hidden></div>` : ''}
        <div class="toolbar">
          <input type="search" id="q" placeholder="Search fonts…" value="${esc(L.q)}">
          <input type="text" class="preview-text" id="pv" placeholder="Type to preview…" value="${esc(L.preview)}">
          <span class="sizectl" title="Preview size in points">
            <input type="range" id="sz" min="6" max="96" step="1" value="${Math.min(96, Math.max(6, Math.round(L.size)))}" aria-label="Preview size">
            <label class="pt"><input type="number" id="szn" min="${SIZE_MIN}" max="${SIZE_MAX}" step="0.5" inputmode="decimal" value="${L.size}" aria-label="Preview size in points"><span>pt</span></label>
          </span>
          <button class="btn sm" id="expandall">${L.expanded.size ? 'Collapse all' : 'Expand all'}</button>
        </div>
        <div id="bulk"></div>
        <div id="families"></div>
      </section>
    </div>`;

  $('#reqfont', page).onclick = (e) => { e.preventDefault(); requestFont(); };
  $$('.side a[data-coll]', page).forEach((a) => a.onclick = (e) => { e.preventDefault(); L.coll = a.dataset.coll; L.selected.clear(); L.shown = 0; renderLibrary(page); });
  const q = $('#q', page);
  q.oninput = () => { L.q = q.value; L.shown = 0; drawFamilies(); };
  const pv = $('#pv', page);
  pv.oninput = () => { L.preview = pv.value; $$('.specimen:not(.nopreview)', page).forEach((el) => el.textContent = L.preview || el.dataset.def); };
  const sz = $('#sz', page), szn = $('#szn', page);
  const setSize = (v, from) => {
    v = Math.round(v * 2) / 2;
    if (!(v >= SIZE_MIN && v <= SIZE_MAX)) return false;
    L.size = v;
    $$('.family > .specimen, .style-row .specimen', page).forEach((el) => el.style.fontSize = v + 'pt');
    if (from !== sz) sz.value = Math.min(96, Math.max(6, Math.round(v)));
    if (from !== szn) szn.value = v;
    try { localStorage.setItem('typehaus.size', v); } catch {}
    return true;
  };
  sz.oninput = () => setSize(+sz.value, sz);
  // typing: apply valid values right away; on leaving the field, clamp
  szn.oninput = () => setSize(parseFloat(szn.value.replace(',', '.')), szn);
  szn.onchange = () => {
    const v = parseFloat(szn.value.replace(',', '.'));
    setSize(Number.isFinite(v) ? Math.min(SIZE_MAX, Math.max(SIZE_MIN, v)) : L.size);
  };
  szn.onkeydown = (e) => { if (e.key === 'Enter') szn.blur(); };
  $('#expandall', page).onclick = () => {
    if (L.expanded.size) L.expanded.clear();
    else groupFamilies(filteredFonts()).forEach((fam) => L.expanded.add(fam.name));
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
  // Render in pages so large libraries stay fast; more on scroll.
  const PAGE = 60;
  L.shown = Math.max(L.shown || PAGE, PAGE);
  const visibleFams = fams.slice(0, L.shown);
  box.innerHTML = visibleFams.map((fam) => {
    const open = L.expanded.has(fam.name);
    const allSel = fam.faces.every((f) => L.selected.has(f.id));
    const lic = fam.rep.license_id ? L.licenses.find((l) => l.id === fam.rep.license_id) : null;
    const restricted = fam.faces.some((f) => f.fs_type & 0x2 && !(f.fs_type & 0xc));
    const conflict = fam.faces.some((f) => f.conflict);
    const kinds = [...new Set(fam.faces.map((f) => f.kind))].filter((k) => k && k !== 'sfnt');
    const plats = [...new Set(fam.faces.map((f) => f.platforms))].filter(Boolean);
    return `<div class="family" data-fam="${esc(fam.name)}">
      <div class="family-head">
        ${admin ? `<input type="checkbox" class="selfam" ${allSel ? 'checked' : ''} title="Select family">` : ''}
        ${fam.required === fam.faces.length
          ? `<label class="toggle locked" title="${esc(requiredTitle(fam.faces[0]))}"><input type="checkbox" checked disabled><span></span></label>`
          : `<label class="toggle" title="Activate all styles"><input type="checkbox" class="actfam" ${fam.active === fam.faces.length ? 'checked' : ''}><span></span></label>`}
        <span class="name">${esc(fam.name)}</span>
        <span class="meta">${plural(fam.faces.length, 'style')}${fam.required ? ` · <span class="badge inst" title="${esc(requiredTitle(fam.faces.find((f) => f.required)))}">installed by ${esc(fam.faces.find((f) => f.required).required_by.join(', '))}</span>` : ''}${fam.active > fam.required ? ` · <span class="badge on">${fam.active - fam.required} active</span>` : ''}${lic ? ` · ${licenseBadge(lic)}` : ''}${restricted ? ' · <span class="badge warn" title="OS/2 fsType: restricted license embedding">restricted</span>' : ''}${conflict ? ' · <span class="badge warn" title="Another file in the library has the same PostScript name (e.g. another version). Only one of them can be active at a time – activating one switches the other off.">duplicate name</span>' : ''}${kinds.map((k) => ` · <span class="badge">${KIND_LABEL[k] || esc(k)}</span>`).join('')}${plats.map((p) => ` · <span class="badge warn" title="Only installed on computers running ${esc(platformNames(p))}">${esc(platformNames(p))} only</span>`).join('')}</span>
        <span class="spacer"></span>
        <span class="meta small">${esc(fam.rep.manufacturer || fam.rep.designer || '')}</span>
        <button class="expand info" title="Details and license">Info</button>
        ${S.me.downloads ? `<button class="expand dl" title="Download ${fam.faces.length > 1 ? 'all styles as ZIP' : 'the font file'} to install it yourself">Download</button>` : ''}
        <button class="expand">${open ? 'Hide styles ▴' : 'Styles ▾'}</button>
      </div>
      <div class="specimen loading" data-font="${fam.rep.id}" data-def="${esc(fam.name)}" style="font-size:${L.size}pt">${esc(L.preview || fam.name)}</div>
      ${open ? `<div class="styles">${fam.faces.map((f) => `
        <div class="style-row" data-id="${f.id}">
          ${admin ? `<input type="checkbox" class="selface" ${L.selected.has(f.id) ? 'checked' : ''}>` : '<span></span>'}
          <span class="sname" title="${esc(f.full_name)}${f.postscript_name ? ' · ' + esc(f.postscript_name) : ''}${f.version ? ' · v' + esc(f.version) : ''} · ${f.format.toUpperCase()}${f.source_format ? ' (converted from ' + esc(f.source_format.toUpperCase()) + ')' : ''} · ${fmtSize(f.size)}">${f.opsz ? `<span class="badge">${esc(f.opsz)}</span> ` : ''}${esc(f.style)}${f.names_manual ? ` <span class="badge" title="Named by hand. Detected from the file: ${esc(f.family_auto)} / ${esc(f.style_auto)}">manual</span>` : ''} <span class="muted">${f.weight}${f.conflict ? ` · v${esc(f.version)}` : ''}</span></span>
          <div class="specimen loading" data-font="${f.id}" data-def="${esc(f.full_name)}" style="font-size:${L.size}pt">${esc(L.preview || f.full_name)}</div>
          ${S.me.downloads ? `<button class="iconbtn dlface" title="Download ${esc(f.style)}" aria-label="Download ${esc(f.full_name)}">↓</button>` : '<span></span>'}
          ${f.required
            ? `<label class="toggle locked" title="${esc(requiredTitle(f))}"><input type="checkbox" checked disabled><span></span></label>`
            : `<label class="toggle"><input type="checkbox" class="actface" ${f.activated ? 'checked' : ''}><span></span></label>`}
        </div>`).join('')}</div>` : ''}
    </div>`;
  }).join('') + (fams.length > L.shown ? `<div class="more" id="more">Showing ${L.shown} of ${fams.length} families…</div>` : '');

  const more = $('#more', box);
  if (more) {
    const io = new IntersectionObserver((en) => {
      if (en.some((e) => e.isIntersecting)) { io.disconnect(); L.shown += PAGE; drawFamilies(); }
    }, { rootMargin: '600px' });
    io.observe(more);
  }

  $$('.actfam', box).forEach((cb) => {
    const fam = fams.find((x) => x.name === cb.closest('.family').dataset.fam);
    cb.indeterminate = fam.active > 0 && fam.active < fam.faces.length;
    // only the styles the user can switch themselves
    cb.onchange = () => setActive(fam.faces.filter((f) => !f.required).map((f) => f.id), fam.active < fam.faces.length);
  });
  $$('.actface', box).forEach((cb) => cb.onchange = () => setActive([+cb.closest('.style-row').dataset.id], cb.checked));
  $$('.info', box).forEach((b) => b.onclick = () => familyInfo(fams.find((x) => x.name === b.closest('.family').dataset.fam)));
  $$('.dl', box).forEach((b) => b.onclick = () => downloadFonts(fams.find((x) => x.name === b.closest('.family').dataset.fam).faces.map((f) => f.id)));
  $$('.dlface', box).forEach((b) => b.onclick = () => downloadFonts([+b.closest('.style-row').dataset.id]));
  $$('.expand:not(.info):not(.dl)', box).forEach((b) => b.onclick = () => {
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

// ---------------------------------------------------------------------------
// font requests
// ---------------------------------------------------------------------------
const myRequests = () => (S.lib.requests || []).filter((r) => r.user_id === S.me.id);
const myAnswers = () => myRequests().filter((r) => r.status !== 'open' && !r.seen);
const REQ_STATUS = { open: '<span class="badge">waiting</span>', done: '<span class="badge open">done</span>', declined: '<span class="badge warn">declined</span>' };

function updateRequestsCount() {
  const a = $('nav.main a[href="#/requests"]');
  if (!a) return;
  const n = (S.lib.requests || []).filter((r) => r.status === 'open').length;
  let c = $('.count', a);
  if (!n) { c?.remove(); return; }
  if (!c) { a.insertAdjacentHTML('beforeend', ' <span class="count"></span>'); c = $('.count', a); }
  c.textContent = n;
}

function requestFont() {
  const mine = myRequests();
  const d = dialog('Request a font', `
    <p class="small muted" style="margin:0">Tell your administrators which font you need. They decide whether to add it – depending on its license – and you'll see the answer here.</p>
    <label class="field">Font<input type="text" name="font" required maxlength="200" placeholder="e.g. Brandon Grotesque Medium"></label>
    <label class="field">What for? <span class="muted">(optional)</span><textarea name="purpose" rows="2" maxlength="2000" placeholder="Project, client, or a link to the font"></textarea></label>
    ${mine.length ? `<h4>Your requests</h4><div class="reqs">${mine.map((r) => `<div class="req ${r.status !== 'open' && !r.seen ? 'new' : ''}">
      <div><b>${esc(r.font)}</b> ${REQ_STATUS[r.status] || ''} <span class="small muted">${ago(r.resolved_at || r.created_at)}</span></div>
      ${r.answer ? `<div class="small">${esc(r.answer)}${r.resolved_by ? ` <span class="muted">– ${esc(r.resolved_by)}</span>` : ''}</div>` : ''}
      ${r.status === 'open' ? `<button type="button" class="btn sm ghost" data-withdraw="${r.id}">Withdraw</button>` : ''}</div>`).join('')}</div>` : ''}`,
  {
    submit: 'Send request',
    onSubmit: async (fd) => {
      await api('POST', '/api/requests', { font: fd.get('font'), purpose: fd.get('purpose') });
      toast('Request sent');
      S.lib.loaded = false;
      if (route() === 'library') renderLibrary($('#page'));
    },
  });
  $$('[data-withdraw]', d).forEach((b) => b.onclick = async () => {
    try { await api('DELETE', `/api/requests/${b.dataset.withdraw}`); b.closest('.req').remove(); toast('Request withdrawn'); S.lib.loaded = false; } catch (e) { fail(e); }
  });
  // answers are now seen
  const fresh = myAnswers();
  if (fresh.length) {
    Promise.all(fresh.map((r) => api('PATCH', `/api/requests/${r.id}`, { seen: true }).catch(() => {}))).then(() => { fresh.forEach((r) => { r.seen = true; }); });
  }
}

async function renderRequests(page) {
  const reqs = await api('GET', '/api/requests');
  S.lib.requests = reqs;
  updateRequestsCount();
  const open = reqs.filter((r) => r.status === 'open');
  const done = reqs.filter((r) => r.status !== 'open');
  const row = (r) => `<tr><td><b>${esc(r.font)}</b>${r.purpose ? `<div class="small muted pre">${esc(r.purpose)}</div>` : ''}</td>
    <td>${esc(r.user)}</td><td class="small">${ago(r.created_at)}</td>
    ${r.status === 'open'
      ? `<td style="text-align:right;white-space:nowrap"><button class="btn sm" data-answer="${r.id}" data-st="done">Done…</button> <button class="btn sm" data-answer="${r.id}" data-st="declined">Decline…</button></td>`
      : `<td>${REQ_STATUS[r.status]}${r.answer ? `<div class="small">${esc(r.answer)}</div>` : ''}<div class="small muted">${esc(r.resolved_by)} · ${ago(r.resolved_at)}</div></td>`}</tr>`;
  page.innerHTML = `
    <div class="row"><div><h1>Requests</h1><p class="sub">Fonts people asked for. Add the font, record its license and put it in a collection – then mark the request done so they know.</p></div></div>
    ${open.length ? `<table class="t"><thead><tr><th>Font</th><th>Requested by</th><th>When</th><th></th></tr></thead><tbody>${open.map(row).join('')}</tbody></table>`
      : '<div class="empty"><div class="big">No open requests.</div>People can request fonts from the library.</div>'}
    ${done.length ? `<h2>Answered</h2><table class="t"><thead><tr><th>Font</th><th>Requested by</th><th>When</th><th>Answer</th></tr></thead><tbody>${done.map(row).join('')}</tbody></table>` : ''}`;
  $$('[data-answer]', page).forEach((b) => b.onclick = () => {
    const r = reqs.find((x) => x.id === +b.dataset.answer);
    const st = b.dataset.st;
    dialog(st === 'done' ? `Done: ${r.font}` : `Decline: ${r.font}`, `
      <label class="field">Message to ${esc(r.user)} <span class="muted">(optional)</span>
        <textarea name="answer" rows="3" maxlength="2000" placeholder="${st === 'done' ? 'e.g. Added to the Brand collection – switch it on in the library.' : 'e.g. No license for web use – please use Inter instead.'}"></textarea></label>`,
    {
      submit: st === 'done' ? 'Mark as done' : 'Decline',
      onSubmit: async (fd) => {
        await api('PATCH', `/api/requests/${r.id}`, { status: st, answer: fd.get('answer') });
        toast(st === 'done' ? 'Marked as done' : 'Declined');
        renderRequests(page);
      },
    });
  });
}

// on: switched on by the user, or installed by the organisation
const isOn = (f) => f.activated || f.required;
const requiredTitle = (f) => `Installed for you by your organisation (collection ${f.required_by.join(', ')}). It can't be switched off here.`;

async function setActive(ids, on) {
  try {
    const r = await api(on ? 'POST' : 'DELETE', '/api/activations', { font_ids: ids });
    toast(on ? `Activated ${plural(ids.length, 'style')} – your devices sync shortly` : `Deactivated ${plural(ids.length, 'style')}`);
    if (!on && r.kept) toast(`${plural(r.kept, 'style')} stay installed – your organisation installs them for you`);
    if (on && r.replaced) toast(`${plural(r.replaced, 'style')} with the same name switched off – only one version can be active`);
    if (on && r.refused?.length) toast(`Some styles were not activated: ${r.refused.join('; ')}`, true);
    S.lib.loaded = false; // reload: conflicts may have switched other fonts off
  } catch (e) { fail(e); }
  const page = $('#page');
  if (page && route() === 'library') {
    const y = window.scrollY;
    await renderLibrary(page);
    window.scrollTo(0, y);
  }
}

// Direct download (single file, or a ZIP for families and Type 1 fonts).
async function downloadFonts(ids) {
  try {
    const r = await fetch(`/api/fonts/download?ids=${ids.join(',')}`, { credentials: 'same-origin' });
    if (!r.ok) {
      let msg = r.statusText;
      try { msg = (await r.json()).error || msg; } catch {}
      throw new Error(msg);
    }
    const skipped = r.headers.get('X-Typehaus-Skipped');
    const cd = r.headers.get('Content-Disposition') || '';
    const m = cd.match(/filename\*=utf-8''([^;]+)/i) || cd.match(/filename="?([^";]+)"?/i);
    const name = m ? decodeURIComponent(m[1]) : 'fonts.zip';
    const url = URL.createObjectURL(await r.blob());
    const a = Object.assign(document.createElement('a'), { href: url, download: name });
    document.body.append(a); a.click(); a.remove();
    setTimeout(() => URL.revokeObjectURL(url), 30000);
    if (skipped) toast(`Some fonts were left out: ${decodeMime(skipped)}`, true);
  } catch (e) { fail(e); }
}
// RFC 2047 "=?utf-8?q?…?=" (used for the skipped-fonts header)
function decodeMime(s) {
  return s.replace(/\?=\s+=\?utf-8\?q\?/gi, '').replace(/=\?utf-8\?q\?(.*?)\?=/gi, (_, q) => {
    const bytes = q.replace(/_/g, ' ').replace(/=([0-9A-F]{2})/gi, (__, h) => String.fromCharCode(parseInt(h, 16)));
    try { return decodeURIComponent(escape(bytes)); } catch { return bytes; }
  });
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
    <button class="btn sm" id="b-fam" title="Merge into another family or split off under a new name">Move to family…</button>
    ${L.fonts.some((f) => L.selected.has(f.id) && f.conflict) ? '<button class="btn sm" id="b-cur" title="Replace the other versions of these styles with the selected ones">Make current version</button>' : ''}
    ${L.fonts.some((f) => L.selected.has(f.id) && f.names_manual) ? '<button class="btn sm" id="b-auto" title="Use the family and style names detected from the files again">Use detected names</button>' : ''}
    <button class="btn sm" id="b-dl">Download</button>
    <button class="btn sm" id="b-del">Delete</button>
    <span class="spacer"></span>
    <button class="btn sm" id="b-clear">Clear selection</button></div>`;
  const ids = [...L.selected];
  const reload = async (msg) => { toast(msg); L.loaded = false; await renderLibrary($('#page')); };
  $('#b-clear').onclick = () => { L.selected.clear(); drawFamilies(); };
  $('#b-dl').onclick = () => downloadFonts(ids);
  $('#b-fam').onclick = () => moveToFamily(ids, reload);
  if ($('#b-cur')) $('#b-cur').onclick = () => {
    const sel = L.fonts.filter((f) => L.selected.has(f.id) && f.postscript_name);
    const pairs = sel.flatMap((n) => L.fonts.filter((o) => o.postscript_name === n.postscript_name && o.id !== n.id && !L.selected.has(o.id))
      .map((o) => ({ old_id: o.id, new_id: n.id, name: n.full_name, from: o.version, to: n.version })));
    if (!pairs.length) { toast('No other versions of the selected styles'); return; }
    confirmDialog('Make current version', `Replace ${plural(pairs.length, 'other version')} with the selected ${plural(sel.length, 'style')}?
      <ul class="small">${pairs.slice(0, 8).map((p) => `<li>${esc(p.name)}: v${esc(p.from)} → v${esc(p.to)}</li>`).join('')}${pairs.length > 8 ? '<li>…</li>' : ''}</ul>
      Collections and activations move to the selected styles, licenses too unless a style comes from another vendor; the other versions are removed.`,
    async () => {
      const r = await api('POST', '/api/fonts/replace', { pairs });
      L.selected.clear();
      await reload(`${plural(r.replaced, 'style')} updated` + (r.license_not_carried ? `; ${plural(r.license_not_carried, 'style')} from another vendor still need a license` : ''));
    }, 'Replace');
  };
  if ($('#b-auto')) $('#b-auto').onclick = async () => {
    try { await api('POST', '/api/fonts/family', { font_ids: ids, reset: true }); L.selected.clear(); await reload('Detected names restored'); } catch (ex) { fail(ex); }
  };
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

// Merge styles into a family or split them off under a new name. Automatic
// grouping is a guess; this is the manual override, and it sticks.
function moveToFamily(ids, reload) {
  const L = S.lib;
  const sel = L.fonts.filter((f) => ids.includes(f.id));
  const families = [...new Set(groupFamilies(L.fonts).map((fam) => fam.name))].sort((a, b) => a.localeCompare(b));
  const current = [...new Set(sel.map((f) => f.family))];
  const one = sel.length === 1 ? sel[0] : null;
  dialog('Move to family', `
    <p class="small muted">${plural(sel.length, 'style')} from ${current.map(esc).join(', ')}. Pick an existing family to merge them into it,
      or type a new name to split them off. Names set here are kept when fonts are re-read; <i>Use detected names</i> undoes it.</p>
    <label class="field">Family<input type="text" name="family" required maxlength="200" list="fam-list" value="${esc(current.length === 1 ? current[0] : '')}" autocomplete="off"></label>
    <datalist id="fam-list">${families.map((n) => `<option value="${esc(n)}">`).join('')}</datalist>
    ${one ? `<label class="field">Style<input type="text" name="style" maxlength="200" value="${esc(one.style)}"></label>` : ''}`, {
    submit: 'Move',
    onSubmit: async (fd) => {
      const body = { font_ids: ids, family: fd.get('family') };
      if (one && fd.get('style') && fd.get('style') !== one.style) body.style = fd.get('style');
      await api('POST', '/api/fonts/family', body);
      L.selected.clear();
      await reload('Moved to ' + fd.get('family'));
    },
  });
}

function setupUpload(page) {
  const drop = $('#drop', page);
  const input = $('#file', page);
  drop.onclick = () => input.click();
  input.onchange = () => upload([...input.files]);
  ['dragenter', 'dragover'].forEach((ev) => drop.addEventListener(ev, (e) => { e.preventDefault(); drop.classList.add('over'); }));
  ['dragleave', 'drop'].forEach((ev) => drop.addEventListener(ev, (e) => { e.preventDefault(); drop.classList.remove('over'); }));
  drop.addEventListener('drop', async (e) => {
    // folders: walk them with the entries API (dataTransfer.files would
    // only contain the folder itself)
    const entries = [...e.dataTransfer.items].map((i) => i.webkitGetAsEntry && i.webkitGetAsEntry()).filter(Boolean);
    if (entries.some((en) => en.isDirectory)) {
      drop.innerHTML = '<b>Reading folder…</b>';
      upload(await filesFromEntries(entries));
    } else {
      upload([...e.dataTransfer.files]);
    }
  });
}

async function filesFromEntries(entries) {
  const out = [];
  const walk = async (en, prefix) => {
    if (en.isFile) {
      const f = await new Promise((res, rej) => en.file(res, rej)).catch(() => null);
      if (f) out.push(new File([f], prefix + f.name, { type: f.type }));
    } else if (en.isDirectory) {
      const reader = en.createReader();
      for (;;) {
        const batch = await new Promise((res) => reader.readEntries(res, () => res([])));
        if (!batch.length) break;
        for (const c of batch) await walk(c, prefix + en.name + '/');
      }
    }
  };
  for (const en of entries) await walk(en, '');
  return out;
}

// Upload in batches of ~64 MB so whole font folders fit the server's request
// limit. Files sharing a base name (Type 1: .pfb + .pfm/.afm) stay together.
const BATCH_BYTES = 64 << 20;
function batches(files) {
  const groups = new Map();
  for (const f of files) {
    const base = f.name.toLowerCase().replace(/\.[^./]+$/, '');
    if (!groups.has(base)) groups.set(base, []);
    groups.get(base).push(f);
  }
  const out = [];
  let cur = [], size = 0;
  for (const g of groups.values()) {
    const gs = g.reduce((n, f) => n + f.size, 0);
    if (cur.length && size + gs > BATCH_BYTES) { out.push(cur); cur = []; size = 0; }
    cur.push(...g); size += gs;
  }
  if (cur.length) out.push(cur);
  return out;
}

async function upload(files, force = false) {
  files = files.filter((f) => f.size > 0);
  if (!files.length) return;
  const L = S.lib;
  const drop = $('#drop');
  const parts = batches(files);
  const all = [];
  for (let i = 0; i < parts.length; i++) {
    if (drop) drop.innerHTML = `<b>Uploading ${plural(files.length, 'file')}…</b>${parts.length > 1 ? ` <span class="muted">(${i + 1} of ${parts.length})</span>` : ''}`;
    const fd = new FormData();
    if (force) fd.append('force', '1');
    if (!['all', 'active'].includes(String(L.coll))) fd.append('collection_id', L.coll);
    parts[i].forEach((f) => fd.append('files', f, f.name));
    try {
      const res = await api('POST', '/api/fonts', fd);
      all.push(...(res.results || []));
    } catch (e) { fail(e); }
  }
  const added = all.filter((x) => x.status === 'added').length;
  const dup = all.filter((x) => x.status === 'duplicate').length;
  const errs = all.filter((x) => x.status === 'error');
  toast(`${added} added${dup ? `, ${dup} already in library` : ''}${errs.length ? `, ${errs.length} failed` : ''}`);
  errs.slice(0, 5).forEach((x) => toast(`${x.file}: ${x.message}`, true));
  all.filter((x) => x.status === 'added' && x.message && !x.updates).slice(0, 3).forEach((x) => toast(`${x.file}: ${x.message}`));
  L.loaded = false;
  renderLibrary($('#page'));
  // other versions of fonts already in the library, and files skipped as the
  // same font: the admin decides
  const updates = all.flatMap((x) => x.updates || []);
  const same = all.filter((x) => x.same_font).map((x) => ({ result: x, file: files.find((f) => x.file === f.name || x.file.startsWith(f.name + ' →')) }))
    .filter((x) => x.file);
  if (updates.length || same.length) versionDialog(updates, same);
}

// After an upload: replace older versions or keep both, and upload files that
// were skipped as the same font anyway (e.g. a corrected build that kept its
// version number).
function versionDialog(updates, same) {
  const vtag = (u) => u.newer ? 'newer' : (u.new_version === u.old_version ? 'same version' : 'older');
  // replace by default only for a newer version from the same vendor
  const isUpdate = (u) => u.newer && !u.other_vendor;
  const notes = (u) => [
    u.other_vendor ? `<span class="badge warn">other vendor</span> ${esc(u.old_vendor)} → ${esc(u.new_vendor)}: not an update but a different font with the same name; its license is not carried over` : '',
    u.license_changed ? '<span class="badge warn">license changed</span> the license notice in the file differs, check the terms' : '',
  ].filter(Boolean).map((n) => `<div class="small">${n}</div>`).join('');
  const d = dialog('Other versions', `
    ${updates.length ? `<p class="small muted">These styles share their PostScript name with fonts already in the library. Only one of them can be
      active at a time. <b>Replace</b> moves licenses, collections and everyone's activations to the uploaded version and removes the old one;
      computers switch over at their next sync.</p>
    <table class="t"><tbody>${updates.map((u, i) => `<tr><td><b>${esc(u.name)}</b>
      <div class="small muted">v${esc(u.old_version || '?')} → v${esc(u.new_version || '?')} · ${vtag(u)}</div></td>
      ${notes(u)}</td>
      <td><select name="u-${i}"><option value="replace" ${isUpdate(u) ? 'selected' : ''}>Replace the old version</option>
        <option value="keep" ${isUpdate(u) ? '' : 'selected'}>Keep both</option></select></td></tr>`).join('')}</tbody></table>` : ''}
    ${same.length ? `<h4>Skipped as the same font</h4><p class="small muted">Same names, version, vendor, license notice, characters and metrics as a font already in the
      library, in a different file. Tick a file to upload it anyway (for example a corrected build); you can then replace the old one.</p>
      ${same.map((x, i) => `<label class="check"><input type="checkbox" name="s-${i}"> <span>${esc(x.result.file)} <span class="small muted">${esc(x.result.message)}</span></span></label>`).join('')}` : ''}`, {
    submit: 'Apply',
    wide: true,
    onSubmit: async (fd) => {
      const pairs = updates.filter((u, i) => fd.get(`u-${i}`) === 'replace').map((u) => ({ old_id: u.old_id, new_id: u.new_id }));
      if (pairs.length) {
        const r = await api('POST', '/api/fonts/replace', { pairs });
        toast(`${plural(r.replaced, 'style')} updated to the new version` +
          (r.license_not_carried ? `; ${plural(r.license_not_carried, 'style')} from another vendor still need a license` : ''));
        S.lib.loaded = false;
        renderLibrary($('#page'));
      }
      const again = same.filter((x, i) => fd.get(`s-${i}`)).map((x) => x.file);
      if (again.length) setTimeout(() => upload(again, true), 0);
    },
  });
  return d;
}

// ---------------------------------------------------------------------------
// devices
function platformLabel(p) {
  const os = { windows: 'Windows', darwin: 'macOS', linux: 'Linux' };
  if (p && p.endsWith('-machine')) {
    return `${os[p.slice(0, -8)] || esc(p)} <span class="badge warn" title="Fonts are installed for every user of this computer">all users</span>`;
  }
  return os[p] || esc(p);
}
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
        <ol class="small steps">
          <li><b>Download typehaus</b> for your computer and install it.</li>
          <li>The <span class="tmark">t<i></i></span> icon appears in your taskbar or menu bar and asks for a server address and a code.</li>
          <li><b>Create a code</b> here and enter it. It works once and expires after 15 minutes.</li>
        </ol>
        <div id="dl" class="row" style="margin:12px 0"></div>
        <button class="btn primary" id="mkcode">Create enrollment code</button>
        <div id="codebox"></div>
      </div>
      <div class="card">
        <h2 style="margin-top:0">How it works</h2>
        <p class="small" style="color:var(--ink-2)">Turn fonts on in the <a href="#/library">Library</a>. Every enrolled computer picks up the change within about a minute and installs the fonts for your user. Turn them off and they disappear again. If access is revoked, the agent removes the fonts it installed.</p>
        <p class="small" style="color:var(--ink-2)">The <span class="tmark">t<i></i></span> icon shows the sync status: a solid square means up to date, an outlined square that the server can't be reached (fonts stay active), and an amber diamond that something needs attention.</p>
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
        ${showAll ? `<td>${esc(d.username)}</td>` : ''}<td>${platformLabel(d.platform)}</td>
        <td title="${d.last_seen ? fmtDate(d.last_seen) : ''}${d.last_ip ? ' · ' + esc(d.last_ip) : ''}">${ago(d.last_seen)}</td>
        <td class="num">${d.installed}</td><td class="mono">${esc(d.agent_version || '–')}</td>
        <td style="text-align:right"><button class="btn sm danger" data-revoke="${d.id}" data-name="${esc(d.name)}">Revoke</button></td></tr>`;
      }).join('')}</tbody></table>` : '<div class="empty">No devices enrolled yet.</div>'}`;

  const dl = $('#dl', page);
  const label = { windows: 'Windows', darwin: 'macOS', linux: 'Linux' };
  const archL = { amd64: 'Intel/AMD', arm64: 'ARM', universal: '' };
  const primary = downloads.filter((d) => !d.headless && (d.os !== 'darwin' || d.app || !downloads.some((x) => x.app)));
  const guessOS = /Win/.test(navigator.platform) ? 'windows' : /Mac/.test(navigator.platform) ? 'darwin' : 'linux';
  const installers = downloads.filter((d) => d.installer);
  if (installers.length) {
    // one installer per system; ARM Linux packages and the standalone
    // programs (servers, scripts) under "Other downloads"
    const main = [
      ['windows', 'exe', 'universal', 'Windows'], ['darwin', 'dmg', 'universal', 'macOS'],
      ['linux', 'deb', 'amd64', 'Linux (.deb)'], ['linux', 'rpm', 'amd64', 'Linux (.rpm)'],
    ].map(([os, fmt, arch, text]) => ({ d: installers.find((x) => x.os === os && x.format === fmt && x.arch === arch), os, text }))
      .filter((x) => x.d);
    main.sort((a, b) => (b.os === guessOS) - (a.os === guessOS));
    const shown = new Set(main.map((x) => x.d.name));
    const other = downloads.filter((d) => !shown.has(d.name) && !d.headless)
      .sort((a, b) => (b.installer - a.installer) || a.name.localeCompare(b.name));
    const otherLabel = (d) => {
      if (d.installer) return `${label[d.os]} ${archL[d.arch] || d.arch} (.${d.format})`;
      if (d.app) return 'macOS app (zip, no installer)';
      return `${label[d.os] || d.os} ${archL[d.arch] || d.arch} – standalone program${d.headless ? ', no console window' : ''}`;
    };
    dl.innerHTML = main.map((x, i) => `<a class="btn sm ${i === 0 && x.os === guessOS ? 'primary' : ''}" href="${esc(x.d.url)}" download>${x.text}</a>`).join('')
      + (other.length ? `<details class="otherdl"><summary class="small">Other downloads</summary><ul class="small">${other.map((d) =>
        `<li><a href="${esc(d.url)}" download>${esc(otherLabel(d))}</a> <span class="muted">${esc(d.name)} · ${fmtSize(d.size)}</span></li>`).join('')}</ul>
        <p class="small muted">The standalone programs need no installation – for servers, scripts and portable use.</p></details>` : '');
  } else if (primary.length) {
    primary.sort((a, b) => (b.os === guessOS) - (a.os === guessOS));
    dl.innerHTML = primary.map((d) => `<a class="btn sm ${d.os === guessOS && (d.arch === 'amd64' || d.app || d.os === 'darwin') ? 'primary' : ''}" href="${esc(d.url)}" download>${label[d.os] || esc(d.os)}${archL[d.arch] !== undefined ? (archL[d.arch] ? ' ' + archL[d.arch] : '') : ' ' + esc(d.arch)}</a>`).join('');
  } else {
    dl.innerHTML = '<span class="small muted">Get typehaus from the <a href="https://github.com/4nonX/typehaus/releases/latest" target="_blank" rel="noopener">releases page</a>.</span>';
  }

  $('#mkcode', page).onclick = async () => {
    try {
      const c = await api('POST', '/api/enroll-codes');
      const bin = { windows: '.\\typehaus-agent-windows-amd64.exe', darwin: './typehaus-agent-darwin-arm64', linux: './typehaus-agent-linux-amd64' };
      const inst = {
        windows: '& "$env:LOCALAPPDATA\\Programs\\typehaus\\typehaus-agent.exe"',
        darwin: '/Applications/typehaus.app/Contents/MacOS/typehaus-agent', linux: 'typehaus',
      };
      const cmds = installers.length ? {
        windows: `# after installing typehaus (PowerShell)\n${inst.windows} enroll --server ${c.server} --code ${c.code}`,
        darwin: `# after installing typehaus\n${inst.darwin} enroll --server ${c.server} --code ${c.code}`,
        linux: `# after installing the typehaus package\n${inst.linux} enroll --server ${c.server} --code ${c.code}\n${inst.linux} autostart enable`,
      } : {
        windows: `${bin.windows} enroll --server ${c.server} --code ${c.code}\n${bin.windows} autostart enable`,
        darwin: `chmod +x typehaus-agent-darwin-arm64\nxattr -d com.apple.quarantine typehaus-agent-darwin-arm64 2>/dev/null\n${bin.darwin} enroll --server ${c.server} --code ${c.code}\n${bin.darwin} autostart enable`,
        linux: `chmod +x typehaus-agent-linux-amd64\n${bin.linux} enroll --server ${c.server} --code ${c.code}\n${bin.linux} autostart enable`,
      };
      const box = $('#codebox', page);
      box.innerHTML = `<div class="enroll">
          <div class="kv"><span>Server address</span><b class="mono">${esc(c.server)}</b><button class="btn sm ghost" data-copy="${esc(c.server)}">Copy</button></div>
          <div class="kv"><span>Code</span><b class="enroll-code">${esc(c.code)}</b><button class="btn sm ghost" data-copy="${esc(c.code)}">Copy</button></div>
          <p class="small muted">Valid until ${new Date(c.expires_at * 1000).toLocaleTimeString()}.</p>
        </div>
        <details class="cli"><summary class="small">Prefer the command line?</summary>
          <div class="tabs">${['windows', 'darwin', 'linux'].map((o) => `<button data-os="${o}">${label[o]}</button>`).join('')}</div>
          <div class="code" id="cmd"></div>
          <div class="row" style="margin-top:8px"><button class="btn sm" id="copy">Copy commands</button><span class="small muted">${installers.length ? 'Quit the typehaus app first.' : 'Adjust the file name for your architecture.'}</span></div>
        </details>`;
      const show = (o) => { $('#cmd', box).textContent = cmds[o]; $$('.tabs button', box).forEach((b) => b.classList.toggle('active', b.dataset.os === o)); };
      $$('.tabs button', box).forEach((b) => b.onclick = () => show(b.dataset.os));
      $('#copy', box).onclick = () => navigator.clipboard.writeText($('#cmd', box).textContent).then(() => toast('Copied'), fail);
      $$('[data-copy]', box).forEach((b) => b.onclick = () => navigator.clipboard.writeText(b.dataset.copy).then(() => toast('Copied'), fail));
      show(guessOS);
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
    <div class="row"><div><h1>Collections</h1><p class="sub">Collections decide who gets which fonts: <b>available</b> to switch on when needed, or <b>installed automatically</b> on every computer of the people they apply to. Add fonts to collections from the Library.</p></div>
      <span class="spacer"></span><button class="btn primary" id="new">New collection</button></div>
    ${colls.length ? `<table class="t"><thead><tr><th>Name</th><th>For</th><th>Distribution</th><th class="num">Styles</th><th></th></tr></thead><tbody>
      ${colls.map((c) => `<tr><td><b>${esc(c.name)}</b>${c.description ? `<div class="small muted">${esc(c.description)}</div>` : ''}</td>
        <td>${c.everyone ? '<span class="badge on">Everyone</span>' : (c.group_ids.length ? c.group_ids.map((g) => `<span class="badge">${esc(gname(g))}</span>`).join(' ') : '<span class="muted small">Admins only</span>')}
          <div class="small muted">${plural(c.members, 'person', 'people')}</div></td>
        <td>${c.mode === 'installed' ? '<span class="badge inst">Installed automatically</span>' : '<span class="muted small">Available</span>'}</td>
        <td class="num"><a href="#/library" data-open="${c.id}">${c.font_count}</a></td>
        <td style="text-align:right;white-space:nowrap"><button class="btn sm" data-edit="${c.id}">Edit</button> <button class="btn sm danger" data-del="${c.id}">Delete</button></td></tr>`).join('')}
      </tbody></table>` : '<div class="empty"><div class="big">No collections yet.</div>Create one to share fonts with your team.</div>'}`;

  const edit = (c) => {
    const d = dialog(c ? `Edit ${c.name}` : 'New collection', `
    <label class="field">Name<input type="text" name="name" required value="${esc(c?.name)}"></label>
    <label class="field">Description<input type="text" name="description" value="${esc(c?.description)}"></label>
    <fieldset class="field"><legend>For whom</legend>
      <label class="check"><input type="checkbox" name="everyone" ${c?.everyone ? 'checked' : ''}> Everyone</label>
      <div class="small">Or these groups${groups.length ? '' : ' <span class="muted">(create groups under People)</span>'}
        <div class="checks">${groups.map((g) => `<label class="check"><input type="checkbox" name="g" value="${g.id}" ${c?.group_ids.includes(g.id) ? 'checked' : ''}> ${esc(g.name)}</label>`).join('') || '<span class="muted">No groups</span>'}</div></div>
    </fieldset>
    <fieldset class="field"><legend>Distribution</legend>
      <div class="modes">
        <label class="mode"><input type="radio" name="mode" value="available" ${c?.mode !== 'installed' ? 'checked' : ''}>
          <span><b>Available</b><span class="small muted">People see these fonts in the library and switch on what they need.</span></span></label>
        <label class="mode"><input type="radio" name="mode" value="installed" ${c?.mode === 'installed' ? 'checked' : ''}>
          <span><b>Installed automatically</b><span class="small muted">Installed on every computer of these people, without them doing anything. They can't switch them off. Joining or leaving a group adds or removes them.</span></span></label>
      </div>
    </fieldset>
    <div class="impact" hidden></div>`,
    {
      onSubmit: async (fd) => {
        const body = { name: fd.get('name'), description: fd.get('description'), everyone: fd.get('everyone') === 'on',
          group_ids: fd.getAll('g').map(Number), mode: fd.get('mode') };
        if (c) await api('PATCH', `/api/collections/${c.id}`, body); else await api('POST', '/api/collections', body);
        S.lib.loaded = false;
        renderCollections(page);
      },
    });
    // live preview of what automatic installation means for seats
    const box = $('.impact', d);
    const submit = $('[type=submit]', d);
    let seq = 0;
    const check = async () => {
      const fd = new FormData($('form', d));
      const mode = fd.get('mode');
      if (mode !== 'installed') { box.hidden = true; submit.disabled = false; return; }
      const my = ++seq;
      let r;
      try {
        r = await api('POST', '/api/collections/impact', { id: c?.id || 0, mode, everyone: fd.get('everyone') === 'on', group_ids: fd.getAll('g').map(Number) });
      } catch (e) { return; }
      if (my !== seq) return;
      const blocked = r.licenses.filter((l) => l.enforce && l.over);
      const over = r.licenses.filter((l) => !l.enforce && l.over);
      box.hidden = false;
      box.className = 'impact ' + (blocked.length ? 'bad' : over.length || r.unlicensed_families ? 'warn' : 'ok');
      box.innerHTML = `<div><b>${c ? plural(r.fonts, 'style') : 'The fonts you add'}</b> will be installed for <b>${plural(r.members, 'person', 'people')}</b>${r.devices ? ` on ${plural(r.devices, 'computer')} so far` : ''}.</div>
        ${r.licenses.length ? `<table class="t small"><thead><tr><th>License</th><th class="num">Seats</th><th class="num">In use now</th><th class="num">After</th><th></th></tr></thead><tbody>
          ${r.licenses.map((l) => `<tr><td>${esc(l.name)}</td><td class="num">${l.seats || '∞'} <span class="muted">${l.unit}</span></td><td class="num">${l.now}</td><td class="num"><b>${l.after}</b></td>
            <td>${l.over ? (l.enforce ? '<span class="badge danger">blocked – not enough seats</span>' : '<span class="badge warn">exceeds seats</span>') : '<span class="badge open">covered</span>'}</td></tr>`).join('')}
        </tbody></table>` : ''}
        ${blocked.length ? '<div>Enforced licenses don\'t have enough seats. Buy more seats, narrow the groups, or remove those fonts from the collection.</div>' : ''}
        ${over.length ? '<div>Some licenses would be exceeded. They aren\'t enforced, so typehaus will install anyway – make sure you are covered.</div>' : ''}
        ${r.unlicensed_families ? `<div>${plural(r.unlicensed_families, 'family', 'families')} in this collection ${r.unlicensed_families === 1 ? 'has' : 'have'} no license recorded.</div>` : ''}`;
      submit.disabled = blocked.length > 0;
    };
    $('form', d).addEventListener('change', check);
    check();
    return d;
  };
  $('#new', page).onclick = () => edit(null);
  $$('[data-edit]', page).forEach((b) => b.onclick = () => edit(colls.find((c) => c.id === +b.dataset.edit)));
  $$('[data-open]', page).forEach((a) => a.onclick = () => { S.lib.coll = a.dataset.open; });
  $$('[data-del]', page).forEach((b) => b.onclick = () => {
    const c = colls.find((x) => x.id === +b.dataset.del);
    confirmDialog('Delete collection', `Delete <b>${esc(c.name)}</b>? The fonts stay in the library, but people who only had them through this collection lose them${c.mode === 'installed' ? ' – they are removed from their computers' : ''}.`,
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
      <td>${u.group_ids.map((g) => `<span class="badge" ${u.sso_group_ids.includes(g) ? 'title="From single sign-on"' : ''}>${esc(gname(g))}${u.sso_group_ids.includes(g) ? ' ↻' : ''}</span>`).join(' ') || '<span class="muted small">–</span>'}</td>
      <td class="small">${[u.has_password && 'password', u.sso && 'SSO'].filter(Boolean).join(' + ') || '<span class="muted">none</span>'}</td>
      <td class="num">${u.active_fonts}</td><td class="num">${u.devices}</td>
      <td style="text-align:right"><button class="btn sm" data-edit="${u.id}">Edit</button>${u.id !== S.me.id ? ` <button class="btn sm danger" data-del="${u.id}">Delete</button>` : ''}</td></tr>`).join('')}
    </tbody></table>
    <h2>Groups</h2>
    ${groups.length ? `<table class="t"><thead><tr><th>Group</th><th>Description</th><th class="num">Members</th><th></th></tr></thead><tbody>
      ${groups.map((g) => `<tr><td><b>${esc(g.name)}</b></td><td class="muted">${esc(g.description)}</td><td class="num">${g.members}</td>
      <td style="text-align:right"><button class="btn sm" data-gedit="${g.id}">Edit</button> <button class="btn sm danger" data-gdel="${g.id}">Delete</button></td></tr>`).join('')}
    </tbody></table>` : '<p class="muted">No groups yet.</p>'}
    ${S.auth?.oidc ? '<p class="small muted">With TYPEHAUS_OIDC_GROUPS_CLAIM set, SSO users are placed into groups whose names match their identity provider groups at every login (marked ↻). Groups you assign by hand are kept.</p>' : ''}`;

  const editUser = (u) => dialog(u ? `Edit ${u.username}` : 'New user', `
    <label class="field">Username<input type="text" name="username" required value="${esc(u?.username)}" autocomplete="off"></label>
    <label class="field">Display name<input type="text" name="display_name" value="${esc(u?.display_name)}"></label>
    <label class="field">Email<input type="email" name="email" value="${esc(u?.email)}"></label>
    <label class="field">${u ? 'New password (leave empty to keep)' : 'Password (min. 10 characters; leave empty for SSO-only)'}<input type="password" name="password" minlength="10" autocomplete="new-password"></label>
    <div class="field small">Groups<div class="checks">${groups.map((g) => `<label class="check"><input type="checkbox" name="g" value="${g.id}" ${u?.group_ids.includes(g.id) ? 'checked' : ''}> ${esc(g.name)}</label>`).join('') || '<span class="muted">No groups yet</span>'}</div></div>
    <label class="check"><input type="checkbox" name="is_admin" ${u?.is_admin ? 'checked' : ''} ${u?.id === S.me.id ? 'disabled' : ''}> Administrator</label>
    ${u ? `<label class="check"><input type="checkbox" name="disabled" ${u.disabled ? 'checked' : ''} ${u.id === S.me.id ? 'disabled' : ''}> Disabled – signs out; their computers remove typehaus fonts until the account is enabled again</label>` : ''}`,
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

// shared license helpers (library, info panel, licenses page)
const RIGHTS = {
  desktop: 'Desktop – print & documents', web: 'Web – @font-face', app: 'Apps & software',
  ebook: 'E-books & PDFs', video: 'Video & broadcast', logo: 'Logos & trademarks',
};

function licenseAttention() {
  if (!S.me?.is_admin) return [];
  return S.lib.licenses.filter((l) => l.expired || l.over || sharedInstalls(l) || (l.expires_in_days != null && l.expires_in_days <= 30));
}

// the warning dot on the Licenses tab
function updateNavDot() {
  const a = $('nav.main a[href="#/licenses"]');
  if (!a) return;
  const want = licenseAttention().length > 0;
  const dot = $('.dot', a);
  if (want && !dot) a.insertAdjacentHTML('beforeend', ' <span class="dot warn" title="Licenses need attention"></span>');
  if (!want && dot) dot.remove();
}

function termText(l) {
  if (!l.expires_on) return 'Perpetual';
  if (l.expired) return `Expired on ${esc(l.expires_on)}`;
  return `Valid until ${esc(l.expires_on)} <span class="muted">(${plural(l.expires_in_days, 'day')} left)</span>`;
}

function seatText(l) {
  const unit = l.seat_unit === 'devices' ? 'device' : 'user';
  if (!l.seats) return `Unlimited${l.used ? ` · ${plural(l.used, unit)} using it` : ''}`;
  return `${l.used} of ${plural(l.seats, unit + ' seat')} in use`;
}

// badge shown next to a family in the library
function licenseBadge(l) {
  const tip = esc(`${l.name}${l.vendor ? ' · ' + l.vendor : ''}`);
  if (l.expired) return `<span class="badge warn" title="${tip}">license expired${l.enforce ? ' – not installed' : ''}</span>`;
  if (l.enforce && l.full && !l.holder) return `<span class="badge warn" title="${tip}: all seats are in use">no free seat</span>`;
  if (S.me.is_admin && l.over) return `<span class="badge warn" title="${tip}: more ${l.seat_unit} than seats">${esc(l.name)} · over seats</span>`;
  return `<span class="badge ${l.kind === 'open' ? 'open' : ''}" title="${tip}">${esc(l.name)}</span>`;
}

// a per-user license installed for all users of some computers
const sharedInstalls = (l) => l.seat_unit === 'users' && l.seats > 0 && l.machine_devices > 0;

function statusBadges(l) {
  const b = [];
  if (sharedInstalls(l)) b.push(`<span class="badge warn" title="Installed for all users on ${plural(l.machine_devices, 'computer')} – who uses it there can't be counted per person">all-users installs</span>`);
  if (l.expired) b.push('<span class="badge danger">expired</span>');
  else if (l.expires_in_days != null && l.expires_in_days <= 30) b.push(`<span class="badge warn">expires in ${plural(l.expires_in_days, 'day')}</span>`);
  if (l.over) b.push('<span class="badge danger">over seats</span>');
  else if (l.peak_over) b.push('<span class="badge warn" title="More in use than seats at some point in the last 12 months">over seats in the last year</span>');
  else if (l.full) b.push('<span class="badge warn">all seats in use</span>');
  if (l.enforce) b.push('<span class="badge" title="Seats and term are enforced: no activations beyond the seats, nothing installed after expiry">enforced</span>');
  return b.join(' ');
}

const rightsSummary = (l) => l.rights.length === Object.keys(RIGHTS).length ? 'All uses'
  : l.rights.map((r) => (RIGHTS[r] || r).split(' – ')[0]).join(', ');

const docURL = (l, d, dl) => `/api/licenses/${l.id}/documents/${d.id}${dl ? '?download=1' : ''}`;

// read-only summary of a license, used in the info panel and license details
function licenseCard(l) {
  return `<div class="lcard">
    <div class="lhead"><b>${esc(l.name)}</b> <span class="badge ${l.kind === 'open' ? 'open' : ''}">${l.kind === 'open' ? 'Open source' : 'Commercial'}</span> ${statusBadges(l)}</div>
    ${l.vendor ? `<div class="small muted">${esc(l.vendor)}</div>` : ''}
    <dl class="kv2">
      <dt>Allowed use</dt><dd>${l.rights.length ? `<div class="chips">${l.rights.map((r) => `<span class="chip">${esc(RIGHTS[r] || r)}</span>`).join('')}</div>` : '<span class="muted">Not recorded – check the license terms</span>'}</dd>
      <dt>Download</dt><dd>${l.downloadable ? 'Font files may be downloaded directly' : 'Only through the typehaus app – no direct download'}</dd>
      ${l.lease_days ? `<dt>Offline use</dt><dd>Up to ${plural(l.lease_days, 'day')} without contact to the server, then the fonts pause until the computer is back online</dd>` : ''}
      <dt>Seats</dt><dd>${seatText(l)}${l.holder ? ' <span class="muted small">· you hold one</span>' : ''}</dd>
      <dt>Term</dt><dd>${termText(l)}${l.purchased_on ? ` <span class="muted small">· purchased ${esc(l.purchased_on)}</span>` : ''}</dd>
      ${l.reference ? `<dt>Reference</dt><dd>${esc(l.reference)}</dd>` : ''}
      ${l.url ? `<dt>Terms</dt><dd><a href="${esc(l.url)}" target="_blank" rel="noopener noreferrer">${esc(l.url.replace(/^https?:\/\//, ''))} ↗</a></dd>` : ''}
      ${l.documents.length ? `<dt>Documents</dt><dd>${l.documents.map((d) => `<a href="${docURL(l, d)}" target="_blank" rel="noopener">${esc(d.filename)}</a>`).join('<br>')}</dd>` : ''}
      ${l.notes ? `<dt>Notes</dt><dd>${richText(l.notes)}</dd>` : ''}
    </dl></div>`;
}

function embeddingText(fs) {
  let t;
  if (fs & 0x2 && !(fs & 0xc)) t = 'Restricted – must not be embedded in documents or PDFs';
  else if (fs & 0x4) t = 'Preview & print – may be embedded read-only';
  else if (fs & 0x8) t = 'Editable – may be embedded for editing';
  else t = 'Installable – no embedding restrictions';
  if (fs & 0x100) t += ' · no subsetting';
  if (fs & 0x200) t += ' · bitmap embedding only';
  return t;
}

// ---------------------------------------------------------------------------
// family info panel
// ---------------------------------------------------------------------------
async function familyInfo(fam) {
  const L = S.lib;
  const r = fam.rep;
  let detail = {};
  try { detail = await api('GET', `/api/fonts/${r.id}`); } catch (e) { fail(e); return; }
  const lic = r.license_id ? L.licenses.find((l) => l.id === r.license_id) : null;
  const mixed = new Set(fam.faces.map((f) => f.license_id ?? 0)).size > 1;
  const formats = [...new Set(fam.faces.map((f) => f.format.toUpperCase() + (f.source_format ? ` (from ${f.source_format.toUpperCase()})` : '')))];
  const fsTypes = [...new Set(fam.faces.map((f) => embeddingText(f.fs_type)))];
  const admin = S.me.is_admin;
  const d = dialog(fam.name, `
    <dl class="kv2">
      ${r.designer ? `<dt>Designer</dt><dd>${esc(r.designer)}</dd>` : ''}
      ${r.manufacturer ? `<dt>Foundry</dt><dd>${esc(r.manufacturer)}</dd>` : ''}
      ${r.version ? `<dt>Version</dt><dd>${esc(r.version)}</dd>` : ''}
      <dt>Styles</dt><dd>${fam.faces.map((f) => esc((f.opsz ? f.opsz + ' ' : '') + f.style)).join(', ')}</dd>
      <dt>Format</dt><dd>${formats.map(esc).join(', ')}</dd>
    </dl>
    <h4>License</h4>
    ${mixed ? '<p class="small muted">The styles of this family have different licenses; showing the license of the regular style.</p>' : ''}
    ${lic ? licenseCard(lic) : `<p class="muted">${admin ? 'No license recorded for this family.' : 'No license information recorded. Ask an administrator before using this font commercially.'}</p>`}
    ${admin ? `<label class="field">Assign license to all ${plural(fam.faces.length, 'style')}
      <select id="fi-lic"><option value="">— keep —</option><option value="0">No license</option>${L.licenses.map((l) => `<option value="${l.id}" ${lic && l.id === lic.id && !mixed ? 'selected' : ''}>${esc(l.name)}</option>`).join('')}</select></label>
      ${fam.faces.length > 1 ? `<details class="perstyle" ${mixed ? 'open' : ''}><summary>License per style</summary>
        <table class="t"><tbody>${fam.faces.map((f) => `<tr><td>${esc((f.opsz ? f.opsz + ' ' : '') + f.style)}</td><td><select data-face="${f.id}">
          <option value="0" ${f.license_id ? '' : 'selected'}>No license</option>
          ${L.licenses.map((l) => `<option value="${l.id}" ${f.license_id === l.id ? 'selected' : ''}>${esc(l.name)}</option>`).join('')}</select></td></tr>`).join('')}
        </tbody></table></details>` : ''}` : ''}
    <h4>Embedding permission <span class="muted small">(from the font file)</span></h4>
    <p class="small">${fsTypes.map(esc).join('<br>')}</p>
    <details class="embedded"><summary>What the font file says (foundry, copyright, license)</summary>${fontEvidence(detail)}</details>`,
  {
    submit: admin ? 'Save' : 'Close', cancel: admin, wide: true,
    onSubmit: async () => {
      if (!admin) return;
      const v = $('#fi-lic', d).value;
      if (v !== '') {
        await api('PATCH', '/api/fonts', { font_ids: fam.faces.map((f) => f.id), license_id: +v });
      } else {
        // per-style choices, grouped by license
        const groups = {};
        $$('[data-face]', d).forEach((sel) => {
          const face = fam.faces.find((f) => f.id === +sel.dataset.face);
          if (+sel.value !== (face.license_id || 0)) (groups[sel.value] ||= []).push(face.id);
        });
        if (!Object.keys(groups).length) return;
        for (const [lid, ids] of Object.entries(groups)) await api('PATCH', '/api/fonts', { font_ids: ids, license_id: +lid });
      }
      toast('License assigned');
      L.loaded = false;
      if (route() === 'library') await renderLibrary($('#page'));
    },
  });
}

// ---------------------------------------------------------------------------
// licenses (admin)
// ---------------------------------------------------------------------------
async function renderLicenses(page) {
  const [lics, usage, inv] = await Promise.all([api('GET', '/api/licenses'), api('GET', '/api/reports/usage'), api('GET', '/api/inventory')]);
  S.lib.licenses = lics;
  updateNavDot();
  const meter = (l) => l.seats
    ? `<span class="meter ${l.over ? 'over' : l.full ? 'full' : ''}"><i style="width:${Math.min(100, l.used / l.seats * 100)}%"></i></span> ${l.used} / ${l.seats}`
    : `${l.used} / ∞`;
  const unassigned = usage.filter((u) => !u.license);
  const attention = licenseAttention();
  page.innerHTML = `
    <div class="row"><div><h1>Licenses</h1><p class="sub">What you bought, what it allows, who uses it – and the documents that prove it.</p></div>
      <span class="spacer"></span>
      <button class="btn" id="detect" title="Assign open-source licenses (OFL, Apache, …) named in the fonts, then suggest commercial licenses by foundry">Detect licenses</button>
      <a class="btn" href="/api/licenses/export.csv" download title="Licenses, seats, usage, peaks and documents">Export CSV</a>
      <a class="btn" href="/api/licenses/history.csv" download title="Daily usage per license, last 12 months">Usage history</a>
      <button class="btn primary" id="new">New license</button></div>
    ${attention.length ? `<div class="alert warnbox">${attention.map((l) => `<div><b>${esc(l.name)}</b>: ${[
      l.expired && `expired on ${esc(l.expires_on)}`,
      !l.expired && l.expires_in_days != null && l.expires_in_days <= 30 && `expires in ${plural(l.expires_in_days, 'day')} (${esc(l.expires_on)})`,
      l.over && `${l.used} ${l.seat_unit} for ${plural(l.seats, 'seat')}`,
      sharedInstalls(l) && `installed for all users on ${plural(l.machine_devices, 'computer')} – per-person seats can't be counted there (enforce the license to keep it off those computers)`].filter(Boolean).join(', ')}</div>`).join('')}</div>` : ''}
    ${lics.length ? `<table class="t lics"><thead><tr><th>License</th><th>Seats</th><th>Term</th><th class="num">Docs</th><th class="num">Families</th><th></th></tr></thead><tbody>
      ${lics.map((l) => `<tr>
        <td><a href="#" class="lname" data-open="${l.id}">${esc(l.name)}</a> ${l.kind === 'open' ? '<span class="badge open">open source</span>' : ''} ${statusBadges(l)}
          <div class="small muted">${[l.vendor, rightsSummary(l)].filter(Boolean).map(esc).join(' · ')}</div></td>
        <td class="nowrap">${meter(l)} <span class="small muted">${l.seat_unit}</span>
          <div class="small ${l.peak_over ? 'warnt' : 'muted'}">peak 12 mo: ${l.seat_unit === 'devices' ? l.peak_devices : l.peak_users}</div></td>
        <td class="small">${termText(l)}</td>
        <td class="num">${l.documents.length || '<span class="muted">–</span>'}</td>
        <td class="num">${l.families}</td>
        <td style="text-align:right;white-space:nowrap"><button class="btn sm" data-edit="${l.id}">Edit</button> <button class="btn sm danger" data-del="${l.id}">Delete</button></td></tr>`).join('')}
    </tbody></table>` : '<p class="muted">No licenses recorded yet. Create one, then assign it to fonts in the Library (select fonts → Assign license, or Info on a family). Open-source fonts can be detected automatically.</p>'}
    ${unassigned.length ? `<p class="small muted">${plural(unassigned.length, 'family', 'families')} without a license: ${unassigned.slice(0, 12).map((u) => esc(u.family)).join(', ')}${unassigned.length > 12 ? '…' : ''}</p>` : ''}
    <h2>Usage by family</h2>
    <table class="t"><thead><tr><th>Family</th><th>License</th><th class="num">Styles</th><th>Active users</th><th class="num">Devices</th></tr></thead><tbody>
    ${usage.map((u) => `<tr><td><b>${esc(u.family)}</b></td><td>${u.license ? esc(u.license) : '<span class="muted small">unassigned</span>'}</td>
      <td class="num">${u.faces}</td><td title="${esc(u.user_names)}">${u.users}${u.uncounted_admins ? ` <span class="small muted" title="Administrators with it active whose license does not count admins">+${plural(u.uncounted_admins, 'admin')}</span>` : ''}${u.user_names ? ` <span class="small muted">${esc(u.user_names.split(',').slice(0, 4).join(', '))}${u.user_names.split(',').length > 4 ? '…' : ''}</span>` : ''}</td>
      <td class="num">${u.devices}</td></tr>`).join('') || '<tr><td colspan="5" class="muted">Library is empty.</td></tr>'}
    </tbody></table>
    ${inventorySection(inv)}`;
  const refresh = () => { S.lib.loaded = false; return renderLicenses(page).catch(fail); };
  $('#new', page).onclick = () => editLicense(null, refresh);
  $('#detect', page).onclick = async () => {
    try {
      const r = await api('POST', '/api/licenses/detect');
      if (r.assigned) toast(`${plural(r.assigned, 'style')} assigned to open-source licenses${r.created.length ? ' · created ' + r.created.join(', ') : ''}`);
      const sugs = await api('GET', '/api/licenses/suggestions');
      if (!sugs.length) {
        if (!r.assigned) toast('No unassigned fonts with a recognisable license');
        refresh();
        return;
      }
      commercialSuggestions(sugs, refresh);
    } catch (e) { fail(e); }
  };
  $$('[data-open]', page).forEach((a) => a.onclick = (e) => { e.preventDefault(); licenseDetail(+a.dataset.open, refresh); });
  $$('[data-edit]', page).forEach((b) => b.onclick = () => editLicense(lics.find((l) => l.id === +b.dataset.edit), refresh));
  $$('[data-del]', page).forEach((b) => b.onclick = () => {
    const l = lics.find((x) => x.id === +b.dataset.del);
    confirmDialog('Delete license', `Delete <b>${esc(l.name)}</b> and its ${plural(l.documents.length, 'document')}? Fonts stay in the library and become unassigned.`,
      async () => { await api('DELETE', `/api/licenses/${l.id}`); refresh(); });
  });
}

// Fonts the computers reported as installed outside typehaus (opt-in scan).
function inventorySection(inv) {
  const head = '<h2>Installed outside typehaus</h2>';
  if (!inv.enabled) {
    return `${head}<p class="small muted">Off. With <code>TYPEHAUS_INVENTORY=true</code> on the server, the typehaus app on each computer reports once a day which fonts were installed by other means – names and checksums only, never the files. Copies of licensed library fonts then show up here.</p>`;
  }
  if (!inv.devices_scanned) return `${head}<p class="small muted">Waiting for the first computers to report – the typehaus app scans once a day.</p>`;
  const sub = `<p class="small muted">${plural(inv.devices_scanned, 'computer')} reported · ${plural(inv.unmanaged_files, 'font')} installed outside typehaus. Listed below: copies of library families that are not open source – they use a license without being counted.</p>`;
  if (!inv.findings.length) return `${head}${sub}<p><span class="badge open">none found</span> No copies of licensed library fonts.</p>`;
  return `${head}${sub}
    <table class="t inv"><thead><tr><th>Family</th><th>License</th><th class="num">Computers</th><th>Where</th></tr></thead><tbody>
    ${inv.findings.map((f) => `<tr><td><b>${esc(f.family)}</b></td>
      <td>${f.license_id ? `<a href="#" data-open="${f.license_id}">${esc(f.license)}</a>` : '<span class="muted small">unassigned</span>'}</td>
      <td class="num">${f.devices}</td>
      <td>${f.copies.map((c) => `<div class="copy"><b>${esc(c.device)}</b> <span class="muted small">${esc(c.user)} · ${c.scope === 'computer' ? 'all users' : 'this user'} · scanned ${ago(c.scanned_at)}</span>
        ${c.exact ? '<span class="badge warn" title="Byte-identical to the file in the typehaus library">exact copy</span>' : `<span class="badge" title="Same family name, different file – another version or a different cut">other file${c.style ? ' · ' + esc(c.style) : ''}</span>`}
        <div class="path" title="${esc(c.path)}">${esc(c.path)}</div></div>`).join('')}</td></tr>`).join('')}
    </tbody></table>`;
}

// Commercial fonts are only suggested: the file tells who made the font, not
// what was bought. Ticked families go to the vendor's license (or a new one
// that asks for seats, term and invoice).
function commercialSuggestions(sugs, done) {
  const lics = S.lib.licenses || [];
  // per family: the suggestion, any existing license, or leave it alone
  const pick = (s, i) => `<select name="lic-${i}">
      <option value="">Leave unassigned</option>
      <option value="new" ${s.license_id ? '' : 'selected'}>New: ${esc(s.license_name)}</option>
      ${lics.map((l) => `<option value="${l.id}" ${s.license_id === l.id ? 'selected' : ''}>${esc(l.name)}</option>`).join('')}
    </select>`;
  const d = dialog('Commercial fonts', `
    <p class="small muted">These families name a commercial foundry in their copyright, trademark or vendor fields – a guess,
      not proof. Check the evidence, then choose the license each family belongs to. New license records ask for the seats,
      term and invoice afterwards.</p>
    <table class="t"><tbody>${sugs.map((s, i) => `<tr>
      <td><b>${esc(s.family)}</b> <span class="small muted">${plural(s.font_ids.length, 'style')}</span>
        <div class="small muted">${esc(s.reason)} · <a href="#" data-ev="${i}">evidence</a></div>
        <div class="ev small" id="ev-${i}" hidden></div></td>
      <td>${pick(s, i)}</td></tr>`).join('')}
    </tbody></table>`, {
    submit: 'Assign',
    wide: true,
    onSubmit: async (fd) => {
      const create = [], existing = {};
      sugs.forEach((s, i) => {
        const v = fd.get(`lic-${i}`);
        if (v === 'new') create.push({ font_ids: s.font_ids, vendor: s.vendor });
        else if (v) (existing[v] ||= []).push(...s.font_ids);
      });
      let n = 0, created = [];
      if (create.length) {
        const r = await api('POST', '/api/licenses/suggestions', { items: create });
        n += r.assigned; created = r.created;
      }
      for (const [lid, ids] of Object.entries(existing)) {
        await api('PATCH', '/api/fonts', { font_ids: ids, license_id: +lid });
        n += ids.length;
      }
      toast(n ? `${plural(n, 'style')} assigned${created.length ? ' · created ' + created.join(', ') : ''}` : 'Nothing assigned');
      done();
    },
  });
  $$('[data-ev]', d).forEach((a) => a.onclick = async (e) => {
    e.preventDefault();
    const i = +a.dataset.ev, box = $(`#ev-${i}`, d);
    if (!box.hidden) { box.hidden = true; return; }
    try { box.innerHTML = fontEvidence(await api('GET', `/api/fonts/${sugs[i].font_ids[0]}`)); box.hidden = false; } catch (ex) { fail(ex); }
  });
}

// What a font file says about who made it and under which terms.
function fontEvidence(detail) {
  const f = detail.font || {};
  const rows = [
    ['Foundry', f.manufacturer], ['Vendor ID', detail.vendor_id], ['Copyright', detail.copyright],
    ['Trademark', detail.trademark], ['License URL', detail.license_url],
  ].filter(([, v]) => v);
  const val = (k, v) => k === 'License URL' && /^https?:\/\//i.test(v)
    ? `<a href="${esc(v)}" target="_blank" rel="noopener noreferrer">${esc(v)} ↗</a>` : esc(v);
  return `<dl class="kv2">${rows.map(([k, v]) => `<dt>${k}</dt><dd>${val(k, v)}</dd>`).join('')}</dl>
    ${detail.license_text ? `<div class="lic">${richText(detail.license_text, 0)}</div>` : ''}
    ${rows.length || detail.license_text ? '' : '<p class="muted">The font file names no foundry and no license.</p>'}`;
}

// Multi-line text pasted from license terms (EULAs, notes): every line keeps
// its indentation as a real indent, so wrapped lines stay aligned and nested
// sections stay readable; blank lines separate paragraphs. Long texts start
// collapsed (clampAt characters; 0 = never, e.g. inside a scrolling box).
function richText(text, clampAt = 600) {
  const lines = String(text).replace(/\r\n?/g, '\n').replace(/\u2028|\u2029/g, '\n').split('\n');
  let html = '', gap = false;
  for (const line of lines) {
    if (!line.trim()) { gap = html !== ''; continue; }
    const lead = line.match(/^[ \t\u00a0]*/)[0].replace(/\t/g, '    ').length;
    const level = Math.min(Math.round(lead / 4), 6);
    html += `<div class="rt-l${level ? ' i' + level : ''}${gap ? ' gap' : ''}">${esc(line.trim())}</div>`;
    gap = false;
  }
  if (!clampAt || text.length <= clampAt) return `<div class="rt">${html}</div>`;
  return `<div class="rt clamp">${html}</div><button type="button" class="linkbtn rt-more">Show all</button>`;
}
document.addEventListener('click', (e) => {
  const b = e.target.closest('.rt-more');
  if (!b) return;
  const box = b.previousElementSibling;
  b.textContent = box.classList.toggle('clamp') ? 'Show all' : 'Show less';
});

function editLicense(l, done) {
  const rights = l ? l.rights : ['desktop'];
  const d = dialog(l ? 'Edit license' : 'New license', `
    <label class="field">Name<input type="text" name="name" required maxlength="200" value="${esc(l?.name)}" placeholder="e.g. Brand Sans – desktop, 10 users"></label>
    <div class="two">
      <label class="field">Type<select name="kind"><option value="commercial">Commercial</option><option value="open" ${l?.kind === 'open' ? 'selected' : ''}>Open source</option></select></label>
      <label class="field">Vendor / foundry<input type="text" name="vendor" maxlength="200" value="${esc(l?.vendor)}"></label>
    </div>
    <fieldset class="field"><legend>Allowed use</legend><div class="chips pick">
      ${Object.entries(RIGHTS).map(([k, v]) => `<label class="chip"><input type="checkbox" name="rights" value="${k}" ${rights.includes(k) ? 'checked' : ''}> ${esc(v)}</label>`).join('')}
    </div></fieldset>
    <div class="two">
      <label class="field">Seats (0 = unlimited)<input type="number" name="seats" min="0" value="${l?.seats ?? 0}"></label>
      <label class="field">Counted per<select name="seat_unit"><option value="users">User (anyone with a style active)</option><option value="devices" ${l?.seat_unit === 'devices' ? 'selected' : ''}>Device (computers with it installed)</option></select></label>
    </div>
    <div class="two">
      <label class="field">Purchased<input type="date" name="purchased_on" value="${esc(l?.purchased_on)}"></label>
      <label class="field">Valid until (empty = perpetual)<input type="date" name="expires_on" value="${esc(l?.expires_on)}"></label>
    </div>
    <div class="two">
      <label class="field">Offline use (days, 0 = no limit)<input type="number" name="lease_days" min="0" max="365" value="${l?.lease_days ?? 0}"></label>
      <p class="small muted hint">For licenses that allow use only while connected: after this many days without reaching the server, a computer removes these fonts. They return automatically once it is back online.</p>
    </div>
    <label class="check"><input type="checkbox" name="downloadable" ${l ? (l.downloadable ? 'checked' : '') : ''}>
      <span><b>Allow direct download</b> – users may download the font files to install them themselves. Off: fonts are only installed through the typehaus app, which tracks every seat.</span></label>
    <label class="check"><input type="checkbox" name="enforce" ${l?.enforce ? 'checked' : ''}>
      <span><b>Enforce</b> – refuse activations when all seats are taken and remove the fonts from computers when the license has expired. Off: typehaus only warns.</span></label>
    <label class="check"><input type="checkbox" name="count_admins" ${!l || l.count_admins ? 'checked' : ''}>
      <span><b>Count administrators</b> – admins take seats and are held to this license's limits and download setting like everyone else. Off: admins use the fonts without taking a seat.</span></label>
    <div class="two">
      <label class="field">Order / invoice reference<input type="text" name="reference" maxlength="200" value="${esc(l?.reference)}"></label>
      <label class="field">License terms URL<input type="url" name="url" placeholder="https://" value="${esc(l?.url)}"></label>
    </div>
    <label class="field">Notes<textarea name="notes" rows="${l?.notes && l.notes.length > 300 ? 12 : 3}" placeholder="Restrictions, contact at the foundry, renewal terms…">${esc(l?.notes)}</textarea></label>
    ${l ? '' : '<p class="small muted">Attach the EULA, invoice or certificate after saving – open the license from the list.</p>'}`,
  {
    wide: true,
    onSubmit: async (fd) => {
      const body = {
        name: fd.get('name'), kind: fd.get('kind'),
        vendor: fd.get('vendor'), rights: fd.getAll('rights'), lease_days: +fd.get('lease_days') || 0,
        seats: +fd.get('seats') || 0, seat_unit: fd.get('seat_unit'), enforce: fd.get('enforce') === 'on', downloadable: fd.get('downloadable') === 'on',
        count_admins: fd.get('count_admins') === 'on',
        purchased_on: fd.get('purchased_on'), expires_on: fd.get('expires_on'),
        reference: fd.get('reference'), url: fd.get('url'), notes: fd.get('notes'),
      };
      if (l) await api('PATCH', `/api/licenses/${l.id}`, body);
      else {
        const r = await api('POST', '/api/licenses', body);
        setTimeout(() => licenseDetail(r.id, done), 0);
      }
      toast('License saved');
      done();
    },
  });
  // new licenses: open-source fonts may be downloaded, commercial ones not
  // unless the EULA allows it
  if (!l) {
    const kind = $('[name=kind]', d), dl = $('[name=downloadable]', d);
    let touched = false;
    dl.addEventListener('change', () => { touched = true; });
    kind.addEventListener('change', () => { if (!touched) dl.checked = kind.value === 'open'; });
  }
  return d;
}

// Daily usage over the last year as a step line, with the seats as a
// reference. One series: no legend, the heading names it.
function usageChart(points, l) {
  const unit = l.seat_unit === 'devices' ? 'devices' : 'users';
  const vals = points.map((p) => ({ day: p.day, v: p[unit] }));
  if (!vals.length) return '<p class="small muted">No usage recorded yet – typehaus records it from now on, daily.</p>';
  const W = 600, H = 150, P = { l: 30, r: 10, t: 10, b: 20 };
  const t0 = Date.parse(vals[0].day), t1 = Math.max(Date.parse(vals[vals.length - 1].day), t0 + 86400000);
  const ymax = Math.max(1, l.seats || 0, ...vals.map((p) => p.v));
  const x = (d) => P.l + (Date.parse(d) - t0) / (t1 - t0) * (W - P.l - P.r);
  const y = (v) => H - P.b - v / ymax * (H - P.t - P.b);
  let path = '';
  vals.forEach((p, i) => { path += i ? ` H${x(p.day).toFixed(1)} V${y(p.v).toFixed(1)}` : `M${x(p.day).toFixed(1)} ${y(p.v).toFixed(1)}`; });
  path += ` H${(W - P.r).toFixed(1)}`;
  const ticks = [0, Math.round(ymax / 2), ymax].filter((v, i, a) => a.indexOf(v) === i);
  const fmt = (d) => new Date(d).toLocaleDateString(undefined, { month: 'short', year: '2-digit' });
  return `<div class="uchart" data-unit="${unit}">
    <svg viewBox="0 0 ${W} ${H}" role="img" aria-label="Daily ${unit} using this license">
      ${ticks.map((v) => `<line class="grid" x1="${P.l}" x2="${W - P.r}" y1="${y(v)}" y2="${y(v)}"/><text class="ax" x="${P.l - 6}" y="${y(v) + 4}" text-anchor="end">${v}</text>`).join('')}
      <text class="ax" x="${P.l}" y="${H - 4}">${fmt(vals[0].day)}</text>
      <text class="ax" x="${W - P.r}" y="${H - 4}" text-anchor="end">${fmt(vals[vals.length - 1].day)}</text>
      ${l.seats ? `<line class="seats" x1="${P.l}" x2="${W - P.r}" y1="${y(l.seats)}" y2="${y(l.seats)}"/><text class="ax" x="${P.l + 4}" y="${y(l.seats) - 4}">${l.seats} seats</text>` : ''}
      <path class="use" d="${path}"/>
      <circle class="dot" r="4" cx="-10" cy="-10"/>
      <rect class="hit" x="${P.l}" y="0" width="${W - P.l - P.r}" height="${H}"/>
    </svg><div class="tip" hidden></div></div>`;
}

function wireUsageChart(box, points, l) {
  const el = $('.uchart', box);
  if (!el) return;
  const unit = el.dataset.unit, svg = $('svg', el), tip = $('.tip', el), dot = $('.dot', el);
  const vals = points.map((p) => ({ day: p.day, v: p[unit] }));
  const W = 600, Pl = 30, Pr = 10;
  const t0 = Date.parse(vals[0].day), t1 = Math.max(Date.parse(vals[vals.length - 1].day), t0 + 86400000);
  $('.hit', el).addEventListener('mousemove', (e) => {
    const r = svg.getBoundingClientRect();
    const sx = (e.clientX - r.left) / r.width * W;
    const t = t0 + (sx - Pl) / (W - Pl - Pr) * (t1 - t0);
    let p = vals[0];
    for (const q of vals) { if (Date.parse(q.day) <= t) p = q; }
    const cx = Pl + (Date.parse(p.day) - t0) / (t1 - t0) * (W - Pl - Pr);
    const ymax = Math.max(1, l.seats || 0, ...vals.map((q) => q.v));
    dot.setAttribute('cx', cx); dot.setAttribute('cy', 150 - 20 - p.v / ymax * 120);
    tip.hidden = false;
    tip.innerHTML = `<b>${p.v}</b> ${unit} <span class="muted">· ${new Date(p.day).toLocaleDateString()}</span>`;
    tip.style.left = `${Math.min(r.width - 140, Math.max(0, cx / W * r.width - 60))}px`;
  });
  $('.hit', el).addEventListener('mouseleave', () => { tip.hidden = true; dot.setAttribute('cx', -10); });
}

async function licenseDetail(id, done) {
  let r, hist = [];
  try {
    [r, hist] = await Promise.all([api('GET', `/api/licenses/${id}`), api('GET', `/api/licenses/${id}/history`)]);
  } catch (e) { fail(e); return; }
  const l = r.license;
  const d = dialog(l.name, `
    ${licenseCard({ ...l, documents: [] })}
    <h4>Documents</h4>
    <p class="small muted">EULA, invoice, license certificate… Documents marked <i>visible to users</i> can be opened by everyone who can see these fonts; the others only by admins.</p>
    <div class="docs">${l.documents.map((doc) => `<div class="doc" data-doc="${doc.id}">
      <a href="${docURL(l, doc)}" target="_blank" rel="noopener">${esc(doc.filename)}</a>
      <span class="small muted">${fmtSize(doc.size)} · ${ago(doc.created_at)}</span>
      <span class="spacer"></span>
      <label class="small"><input type="checkbox" class="doc-shared" ${doc.shared ? 'checked' : ''}> visible to users</label>
      <a class="small" href="${docURL(l, doc, true)}">Download</a>
      <button type="button" class="btn sm danger doc-del">Remove</button></div>`).join('') || '<p class="muted small">No documents attached yet.</p>'}</div>
    <div class="docup"><button type="button" class="btn sm" id="doc-add">Attach files…</button>
      <label class="small"><input type="checkbox" id="doc-sharednew" checked> visible to users</label>
      <input type="file" id="doc-file" multiple hidden></div>
    <h4>Usage, last 12 months <span class="muted small">peak ${l.seat_unit === 'devices' ? l.peak_devices : l.peak_users} ${l.seat_unit}</span></h4>
    ${usageChart(hist, l)}
    <h4>Fonts <span class="muted small">${plural(r.families.length, 'family', 'families')}</span></h4>
    <p class="small">${r.families.map((f) => `${esc(f.family)} <span class="muted">(${f.styles})</span>`).join(', ') || '<span class="muted">None assigned – select fonts in the Library and choose “Assign license”.</span>'}</p>
    <h4>${l.seat_unit === 'devices' ? 'Devices' : 'People'} holding a seat <span class="muted small">${seatText(l)}</span></h4>
    <p class="small">${(r.holders || []).map((h) => `${esc(h.name)}${h.extra ? ` <span class="muted">(${esc(h.extra)})</span>` : ''}`).join(', ') || '<span class="muted">Nobody yet.</span>'}</p>`,
  { submit: 'Close', cancel: false, wide: true, onSubmit: async () => {} });
  if (hist.length) wireUsageChart(d, hist, l);
  const reopen = () => { d.close(); done(); licenseDetail(id, done); };
  $('#doc-add', d).onclick = () => $('#doc-file', d).click();
  $('#doc-file', d).onchange = async (e) => {
    const files = [...e.target.files];
    if (!files.length) return;
    const fd = new FormData();
    fd.append('shared', $('#doc-sharednew', d).checked ? '1' : '0');
    files.forEach((f) => fd.append('files', f, f.name));
    try { await api('POST', `/api/licenses/${id}/documents`, fd); toast(`${plural(files.length, 'document')} attached`); reopen(); } catch (ex) { fail(ex); }
  };
  $$('.doc', d).forEach((row) => {
    const did = row.dataset.doc;
    $('.doc-shared', row).onchange = async (e) => {
      try { await api('PATCH', `/api/licenses/${id}/documents/${did}`, { shared: e.target.checked }); toast(e.target.checked ? 'Visible to users' : 'Admins only'); } catch (ex) { fail(ex); }
    };
    $('.doc-del', row).onclick = async () => {
      try { await api('DELETE', `/api/licenses/${id}/documents/${did}`); toast('Document removed'); reopen(); } catch (ex) { fail(ex); }
    };
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

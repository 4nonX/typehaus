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

// list or grid of families, remembered per browser (Library and Find fonts)
function savedView() {
  try { return localStorage.getItem('typehaus.view') === 'grid' ? 'grid' : 'list'; } catch { return 'list'; }
}
function setView(v) {
  S.view = v;
  try { localStorage.setItem('typehaus.view', v); } catch {}
}
const PANGRAM = 'Sphinx of black quartz, judge my vow.';
const VIEW_ICONS = {
  grid: '<svg viewBox="0 0 16 16" width="15" height="15" fill="none" stroke="currentColor" stroke-width="1.4"><rect x="2" y="2" width="5" height="5" rx="1"/><rect x="9" y="2" width="5" height="5" rx="1"/><rect x="2" y="9" width="5" height="5" rx="1"/><rect x="9" y="9" width="5" height="5" rx="1"/></svg>',
  list: '<svg viewBox="0 0 16 16" width="15" height="15" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round"><path d="M5 4h9M5 8h9M5 12h9"/><circle cx="2.3" cy="4" r=".6" fill="currentColor"/><circle cx="2.3" cy="8" r=".6" fill="currentColor"/><circle cx="2.3" cy="12" r=".6" fill="currentColor"/></svg>',
};
function viewToggle() {
  return `<span class="viewtg" role="group" aria-label="Display">${['grid', 'list'].map((v) =>
    `<button type="button" class="${S.view === v ? 'active' : ''}" data-view="${v}" title="${v === 'grid' ? 'Grid' : 'List'}" aria-label="${v === 'grid' ? 'Grid' : 'List'}" aria-pressed="${S.view === v}">${VIEW_ICONS[v]}</button>`).join('')}</span>`;
}
function wireViewToggle(el, redraw) {
  $$('[data-view]', el).forEach((b) => b.onclick = () => {
    setView(b.dataset.view);
    $$('[data-view]', el).forEach((x) => { x.classList.toggle('active', x.dataset.view === S.view); x.setAttribute('aria-pressed', x.dataset.view === S.view); });
    redraw();
  });
}

const S = {
  me: null,
  auth: null,
  view: savedView(),
  lib: {
    fonts: [], collections: [], licenses: [], loaded: false,
    q: '', coll: 'all', preview: '', size: savedSize(), expanded: new Set(), selected: new Set(),
  },
};

const ROUTES = {
  library: { title: 'Library', render: renderLibrary },
  find: { title: 'Find fonts', render: renderFind },
  devices: { title: 'Devices', render: renderDevices },
  collections: { title: 'Collections', render: renderCollections, admin: true },
  people: { title: 'People', render: renderPeople, admin: true },
  licenses: { title: 'Licenses', render: renderLicenses, admin: true },
  requests: { title: 'Requests', render: renderRequests, admin: true },
  activity: { title: 'Activity', render: renderActivity, admin: true },
  integrations: { title: 'Integrations', render: renderIntegrations, admin: true },
};

function route() {
  let r = location.hash.replace(/^#\/?/, '').split('?')[0] || 'library';
  if (r === 'api') r = 'integrations';
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
          <span class="whoname" title="${esc(S.me.display_name || S.me.username)}">${esc(S.me.display_name || S.me.username)}${S.me.is_admin ? ' <span class="badge">admin</span>' : ''}</span>
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
function dialog(title, body, { submit = 'Save', danger = false, onSubmit, cancel = true, cancelLabel = 'Cancel', extra = null, wide = false } = {}) {
  const d = document.createElement('dialog');
  if (wide) d.classList.add('wide');
  d.innerHTML = `<form method="dialog"><h3>${esc(title)}</h3>${body}
    <div class="err"></div>
    <div class="actions">${cancel ? `<button type="button" class="btn ghost" value="cancel">${esc(cancelLabel)}</button>` : ''}
    ${extra ? `<button type="button" class="btn ${extra.danger ? 'danger' : ''}" value="extra">${esc(extra.label)}</button>` : ''}
    <button type="submit" class="btn ${danger ? 'accent' : 'primary'}">${esc(submit)}</button></div></form>`;
  document.body.append(d);
  const form = $('form', d);
  // Closing (Cancel, Esc, a click outside) asks first when something was
  // typed or changed that only Save would keep. Controls inside
  // [data-instant] save by themselves or only change the view.
  let dirty = false;
  const touched = (e) => { if (!e.target.closest('[data-instant]')) dirty = true; };
  form.addEventListener('input', touched);
  form.addEventListener('change', touched);
  const requestClose = () => {
    if (!dirty) { d.close(); return; }
    dialog('Unsaved changes', '<p>You changed something that isn\'t saved yet.</p>', {
      submit: 'Save changes', cancelLabel: 'Keep editing',
      extra: { label: 'Discard changes', danger: true, onClick: () => d.close() },
      // save exactly like the form's own button: validation, errors and all
      onSubmit: () => { setTimeout(() => form.requestSubmit(), 0); },
    });
  };
  const c = $('[value=cancel]', d);
  if (c) c.onclick = requestClose;
  const x = $('[value=extra]', d);
  if (x) x.onclick = () => { d.close(); extra.onClick(); };
  d.addEventListener('cancel', (e) => { e.preventDefault(); requestClose(); }); // Esc
  // a click on the backdrop: pressed and released outside the dialog box
  // (not a text selection that ended outside)
  const outside = (e) => {
    if (e.target !== d) return false;
    const r = d.getBoundingClientRect();
    return e.clientX < r.left || e.clientX > r.right || e.clientY < r.top || e.clientY > r.bottom;
  };
  let downOutside = false;
  d.addEventListener('mousedown', (e) => { downOutside = outside(e); });
  d.addEventListener('click', (e) => { if (downOutside && outside(e)) requestClose(); downOutside = false; });
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
    if (q && !matchesQuery(f, q)) return false;
    return true;
  });
}
const matchesQuery = (f, q) => `${f.family} ${f.full_name} ${f.postscript_name} ${f.designer} ${f.manufacturer}`.toLowerCase().includes(q);

async function renderLibrary(page) {
  const L = S.lib;
  const hq = new URLSearchParams(location.hash.split('?')[1] || '').get('q');
  if (hq !== null) { L.q = hq; L.coll = 'all'; history.replaceState(null, '', '#/library'); }
  if (!L.loaded) {
    page.innerHTML = '<div class="empty">Loading library…</div>';
    await loadLibrary();
  }
  const isAdmin = S.me.is_admin;
  const activeCount = famCount(L.fonts.filter(isOn));
  page.innerHTML = `
    <div class="lib">
      <aside class="side">
        <h1 style="font-size:28px">Library</h1>
        <p class="muted small" style="margin:0">${plural(groupFamilies(L.fonts).length, 'family', 'families')} · ${plural(L.fonts.length, 'style')}</p>
        <h4>Show</h4>
        <a href="#" data-coll="all" class="${L.coll === 'all' ? 'active' : ''}">All fonts <span class="n">${famCount(L.fonts)}</span></a>
        <a href="#" data-coll="active" class="${L.coll === 'active' ? 'active' : ''}">Active for me <span class="n">${activeCount}</span></a>
        <h4>Collections</h4>
        ${L.collections.map((c) => `<a href="#" data-coll="${c.id}" class="${String(L.coll) === String(c.id) ? 'active' : ''}">${esc(c.name)} <span class="n">${famCount(inCollection(c.id))}</span></a>`).join('') || '<p class="muted small" style="padding:0 10px">None yet</p>'}
        ${isAdmin ? '<p style="padding:0 10px"><a href="#/collections" class="small" style="padding:0">Manage collections →</a></p>' : ''}
        <h4>Missing a font?</h4>
        <a href="#/find">Find free fonts</a>
        <a href="#/find?tab=identify">Identify a font from an image</a>
        <a href="#" id="reqfont">Request a font${myAnswers().length ? ` <span class="n"><span class="badge on">${myAnswers().length} new answer${myAnswers().length > 1 ? 's' : ''}</span></span>` : ''}</a>
      </aside>
      <section>
        ${isAdmin ? `<div class="drop" id="drop">
          <b>Drop fonts here</b> or click to upload. Any format: TrueType, OpenType, WOFF/WOFF2, Mac suitcases, PostScript Type 1 (with .pfm/.afm), Windows .fon, X11 bitmap fonts – or ZIP archives and whole font folders.
          ${!['all', 'active'].includes(String(L.coll)) ? `<br><span class="small muted">Uploads are added to the selected collection.</span>` : ''}
          <input type="file" id="file" multiple hidden></div>` : ''}
        <div class="toolbar">
          <input type="search" id="q" placeholder="Search fonts…  /" value="${esc(L.q)}" title="Search (press / to jump here, Esc to clear)">
          <span class="pvctl">
            <input type="text" class="preview-text" id="pv" placeholder="Type to preview…" value="${esc(L.preview)}" title="Preview text (Esc clears)">
            <select id="pvs" aria-label="Sample text" title="Sample texts"><option value="">Samples</option>${SAMPLES.map((s, i) => `<option value="${i}">${esc(s.name)}</option>`).join('')}</select>
          </span>
          <span class="sizectl" title="Preview size in points">
            <input type="range" id="sz" min="6" max="96" step="1" value="${Math.min(96, Math.max(6, Math.round(L.size)))}" aria-label="Preview size">
            <label class="pt"><input type="number" id="szn" min="${SIZE_MIN}" max="${SIZE_MAX}" step="0.5" inputmode="decimal" value="${L.size}" aria-label="Preview size in points"><span>pt</span></label>
          </span>
          <button class="btn sm" id="expandall" ${S.view === 'grid' ? 'hidden' : ''}>${L.expanded.size ? 'Collapse all' : 'Expand all'}</button>
          ${viewToggle()}
        </div>
        <div id="allrow"></div>
        <div id="bulk"></div>
        <div id="families"></div>
      </section>
    </div>`;

  $('#reqfont', page).onclick = (e) => { e.preventDefault(); requestFont(); };
  $$('.side a[data-coll]', page).forEach((a) => a.onclick = (e) => { e.preventDefault(); L.coll = a.dataset.coll; L.selected.clear(); L.shown = 0; renderLibrary(page); });
  const q = $('#q', page);
  q.oninput = () => { L.q = q.value; L.shown = 0; drawFamilies(); };
  escClears(q, () => { L.q = ''; L.shown = 0; drawFamilies(); });
  const pv = $('#pv', page);
  const setPreview = (t) => { L.preview = t; $$('.specimen:not(.nopreview)', page).forEach((el) => el.textContent = L.preview || el.dataset.def); };
  pv.oninput = () => setPreview(pv.value);
  const pvs = $('#pvs', page);
  pvs.onchange = () => {
    if (pvs.value === '') return;
    pv.value = SAMPLES[+pvs.value].text;
    setPreview(pv.value);
    pvs.value = '';
  };
  escClears(pv, () => setPreview(''));
  const sz = $('#sz', page), szn = $('#szn', page);
  const setSize = (v, from) => {
    v = Math.round(v * 2) / 2;
    if (!(v >= SIZE_MIN && v <= SIZE_MAX)) return false;
    L.size = v;
    $$('.family > .specimen, .style-row .specimen, .tile > .specimen', page).forEach((el) => el.style.fontSize = v + 'pt');
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
  wireViewToggle(page, () => { $('#expandall', page).hidden = S.view === 'grid'; drawFamilies(); });
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
  drawAllRow();
  if (!fams.length) {
    const q = L.q.trim();
    if (q && L.fonts.length) {
      // a search that finds nothing: the font may simply not be here yet
      const elsewhere = String(L.coll) !== 'all' && groupFamilies(L.fonts.filter((f) => matchesQuery(f, q.toLowerCase()))).length;
      box.innerHTML = `<div class="empty"><div class="big">No font matches “${esc(q)}”.</div>
        ${elsewhere ? `<p><a href="#" id="searchall">Search all fonts</a> – it's in another collection.</p>` : ''}
        <p>${S.me.is_admin ? 'Upload it above, or note it as a request.' : 'Not in the library yet? Ask your administrators to add it.'}</p>
        <button class="btn" id="reqq">Request “${esc(q)}”</button></div>`;
      $('#reqq', box).onclick = () => requestFont(q);
      const sa = $('#searchall', box);
      if (sa) sa.onclick = (e) => { e.preventDefault(); L.coll = 'all'; L.shown = 0; renderLibrary($('#page')); };
    } else {
      box.innerHTML = `<div class="empty"><div class="big">Nothing here yet.</div>${
        L.fonts.length ? 'No fonts match this view.' : (S.me.is_admin ? 'Upload fonts above to build your library.' : 'Ask an administrator to give you access to a collection.')}</div>`;
    }
    drawBulk();
    return;
  }
  const admin = S.me.is_admin;
  // Render in pages so large libraries stay fast; more on scroll.
  const PAGE = 60;
  L.shown = Math.max(L.shown || PAGE, PAGE);
  const visibleFams = fams.slice(0, L.shown);
  if (S.view === 'grid') {
    drawFamilyGrid(box, fams, visibleFams);
    return;
  }
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
          : namedOut(lic) && !fam.active ? `<label class="toggle locked" title="${esc(NAMED_TIP)}"><input type="checkbox" disabled><span></span></label>`
          : `<label class="toggle" title="Activate all styles"><input type="checkbox" class="actfam" ${fam.active === fam.faces.length ? 'checked' : ''}><span></span></label>`}
        <span class="name">${esc(fam.name)}</span>
        <span class="meta">${plural(fam.faces.length, 'style')}${fam.required ? ` · <span class="badge inst" title="${esc(requiredTitle(fam.faces.find((f) => f.required)))}">installed by ${esc(fam.faces.find((f) => f.required).required_by.join(', '))}</span>` : ''}${fam.active > fam.required ? ` · <span class="badge on">${fam.active - fam.required} active</span>` : ''}${lic ? ` · ${licenseBadge(lic)}` : ''}${restricted ? ' · <span class="badge warn" title="OS/2 fsType: restricted license embedding">restricted</span>' : ''}${conflict ? ' · <span class="badge warn" title="Another file in the library has the same PostScript name (e.g. another version). Only one of them can be active at a time – activating one switches the other off.">duplicate name</span>' : ''}${kinds.map((k) => ` · <span class="badge">${KIND_LABEL[k] || esc(k)}</span>`).join('')}${plats.map((p) => ` · <span class="badge warn" title="Only installed on computers running ${esc(platformNames(p))}">${esc(platformNames(p))} only</span>`).join('')}</span>
        <span class="spacer"></span>
        <span class="meta small">${esc(fam.rep.manufacturer || fam.rep.designer || '')}</span>
        <button class="expand info" title="Details and license">Info</button>
        ${namedOut(lic) ? `<button class="expand" data-askfont="${esc(fam.name)}" title="${esc(NAMED_TIP)}">Request</button>`
          : S.me.downloads ? `<button class="expand dl" title="Download ${fam.faces.length > 1 ? 'all styles as ZIP' : 'the font file'} to install it yourself">Download</button>` : ''}
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
            : namedOut(lic) && !f.activated ? `<label class="toggle locked" title="${esc(NAMED_TIP)}"><input type="checkbox" disabled><span></span></label>`
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

// drawFamilyGrid shows the families as tiles: the text in the font, the name,
// switching on and the details.
function drawFamilyGrid(box, fams, visibleFams) {
  const L = S.lib;
  const admin = S.me.is_admin;
  box.innerHTML = `<div class="fgrid">${visibleFams.map((fam) => {
    const lic = fam.rep.license_id ? L.licenses.find((l) => l.id === fam.rep.license_id) : null;
    const allSel = fam.faces.every((f) => L.selected.has(f.id));
    const locked = fam.required === fam.faces.length;
    return `<div class="tile ${fam.active ? 'on' : ''}" data-fam="${esc(fam.name)}">
      ${admin ? `<input type="checkbox" class="selfam tsel" ${allSel ? 'checked' : ''} title="Select family" aria-label="Select ${esc(fam.name)}">` : ''}
      <div class="specimen loading" data-font="${fam.rep.id}" data-def="${esc(PANGRAM)}" style="font-size:${L.size}pt">${esc(L.preview || PANGRAM)}</div>
      <div class="tile-foot">
        <button class="tname info" title="Details and license">${esc(fam.name)}</button>
        <span class="meta">${plural(fam.faces.length, 'style')}${lic && lic.kind !== 'open' ? ` · ${esc(lic.name)}` : ''}</span>
        <span class="spacer"></span>
        ${locked ? `<label class="toggle locked" title="${esc(requiredTitle(fam.faces[0]))}"><input type="checkbox" checked disabled><span></span></label>`
          : namedOut(lic) && !fam.active ? `<button class="btn sm" data-askfont="${esc(fam.name)}" title="${esc(NAMED_TIP)}">Request</button>`
          : `<label class="toggle" title="Activate all styles"><input type="checkbox" class="actfam" ${fam.active === fam.faces.length ? 'checked' : ''}><span></span></label>`}
      </div>
    </div>`;
  }).join('')}</div>` + (fams.length > L.shown ? `<div class="more" id="more">Showing ${L.shown} of ${fams.length} families…</div>` : '');
  const more = $('#more', box);
  if (more) {
    const io = new IntersectionObserver((en) => {
      if (en.some((e) => e.isIntersecting)) { io.disconnect(); L.shown += 60; drawFamilies(); }
    }, { rootMargin: '600px' });
    io.observe(more);
  }
  const famOf = (el) => fams.find((x) => x.name === el.closest('.tile').dataset.fam);
  $$('.actfam', box).forEach((cb) => {
    const fam = famOf(cb);
    cb.indeterminate = fam.active > 0 && fam.active < fam.faces.length;
    cb.onchange = () => setActive(fam.faces.filter((f) => !f.required).map((f) => f.id), fam.active < fam.faces.length);
  });
  $$('.info', box).forEach((b) => b.onclick = () => familyInfo(famOf(b)));
  $$('.selfam', box).forEach((cb) => cb.onchange = () => {
    famOf(cb).faces.forEach((f) => cb.checked ? L.selected.add(f.id) : L.selected.delete(f.id));
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

function requestFont(prefill = '') {
  const mine = myRequests();
  const d = dialog('Request a font', `
    <p class="small muted" style="margin:0">Tell your administrators which font you need. They decide whether to add it – depending on its license – and you'll see the answer here.</p>
    <label class="field">Font<input type="text" name="font" required maxlength="200" value="${esc(prefill)}" placeholder="e.g. Brandon Grotesque Medium"></label>
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
    const what = fontsLabel(fontsByIds(ids)) || plural(ids.length, 'style');
    toast(on ? `Activated ${what} – your devices sync shortly` : `Deactivated ${what}`);
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

// Fonts are counted by family (by name), like the library lists them; the
// number of style files follows where it differs.
const famCount = (fonts) => groupFamilies(fonts).length;
function fontsLabel(fonts) {
  const fams = groupFamilies(fonts);
  const n = fonts.length;
  if (fams.length === 1) {
    if (n === 1) return `${fams[0].name} ${fonts[0].style}`.trim();
    return `${fams[0].name} (${plural(n, 'style')})`;
  }
  return plural(fams.length, 'family', 'families') + (n !== fams.length ? ` (${plural(n, 'style')})` : '');
}
const fontsByIds = (ids) => { const s = new Set(ids); return S.lib.fonts.filter((f) => s.has(f.id)); };
const inCollection = (id) => S.lib.fonts.filter((f) => f.collections.includes(id));

// Esc in a field clears it; a second Esc leaves the field.
function escClears(el, apply) {
  el.addEventListener('keydown', (e) => {
    if (e.key !== 'Escape') return;
    e.preventDefault();
    if (el.value) { el.value = ''; apply(); } else el.blur();
  });
}

// "/" jumps to the library search from anywhere on the page.
document.addEventListener('keydown', (e) => {
  if (e.key !== '/' || e.ctrlKey || e.metaKey || e.altKey || route() !== 'library') return;
  const t = e.target;
  if (t.closest('input, textarea, select, [contenteditable]') || document.querySelector('dialog[open]')) return;
  const q = $('#q');
  if (!q) return;
  e.preventDefault();
  q.focus();
  q.select();
});

// Sizes of the waterfall in a family's Info, in points.
const WATERFALL = [8, 9, 10, 12, 14, 18, 24, 36, 48, 72];

// Sample texts for the preview field. The running texts are our own.
const SAMPLES = [
  { name: 'Pangram (English)', text: 'The quick brown fox jumps over the lazy dog' },
  { name: 'Pangram (German)', text: 'Victor jagt zwölf Boxkämpfer quer über den großen Sylter Deich' },
  { name: 'Alphabet & umlauts', text: 'ABCDEFGHIJKLMNOPQRSTUVWXYZ abcdefghijklmnopqrstuvwxyz ÄÖÜ äöü ß ẞ' },
  { name: 'Figures & currency', text: '0123456789 · 1.234,56 € · $1,234.56 · £ ¥ ¢ · % ‰ · ½ ¼ ¾ · + − × ÷ = ± · #@&' },
  { name: 'Punctuation & quotes', text: '„Deutsch“ ‚einfach‘ »Guillemets« “English” ‘single’ – — … ! ? ¡ ¿ ( ) [ ] { } / \\ · • § ¶ © ® ™' },
  { name: 'Running text (English)', text: 'Good typography goes unnoticed. The reader simply follows the text, line after line, without stumbling over a cramped letter, a loose word space or a figure that sits too high – and only notices the typeface once it is missing.' },
  { name: 'Running text (German)', text: 'Gute Typografie fällt nicht auf. Man liest einfach weiter, Zeile für Zeile, ohne über einen zu engen Buchstaben, einen zu weiten Wortabstand oder eine zu hoch stehende Ziffer zu stolpern – und bemerkt die Schrift erst, wenn sie fehlt.' },
  { name: 'Hamburgefonstiv', text: 'Hamburgefonstiv HAMBURGEFONSTIV' },
];

// One switch for every font in the current view (search and collection
// applied; with "All fonts" and no search that is the whole library). Fonts
// the organisation installs are always on and not touched.
function drawAllRow() {
  const L = S.lib;
  const row = $('#allrow');
  if (!row) return;
  const shown = filteredFonts();
  if (!shown.length) { row.innerHTML = ''; return; }
  const whole = String(L.coll) === 'all' && !L.q.trim();
  const own = shown.filter((f) => f.activated && !f.required);
  const req = shown.filter((f) => f.required).length;
  const off = shown.filter((f) => !isOn(f));
  const on = shown.length - off.length;
  const shownFams = groupFamilies(shown);
  const famsOn = shownFams.filter((fm) => fm.faces.every(isOn)).length;
  const famsPart = shownFams.filter((fm) => fm.faces.some(isOn) && !fm.faces.every(isOn)).length;
  const label = whole ? 'All fonts' : String(L.coll) === 'active' && !L.q.trim() ? 'All fonts active for me' : `All ${plural(shownFams.length, 'family', 'families')} shown`;
  row.innerHTML = `<div class="allrow${S.me.is_admin ? " sel" : ""}">
    <label class="toggle" title="${off.length ? 'Switch on' : 'Switch off'} ${whole ? 'every font in the library' : 'every font in this view'}"><input type="checkbox" id="allon" ${off.length ? '' : 'checked'}><span></span></label>
    <b>${label}</b>
    <span class="muted small">${famsOn} of ${plural(shownFams.length, 'family', 'families')} on${famsPart ? ` · ${famsPart} partly` : ''}${req ? ` · ${plural(famCount(shown.filter((f) => f.required)), 'family', 'families')} installed by your organisation` : ''}</span></div>`;
  const cb = $('#allon', row);
  cb.indeterminate = on > 0 && off.length > 0;
  cb.onchange = () => {
    const turnOn = off.length > 0; // partly on: the click completes it
    cb.checked = !turnOn; cb.indeterminate = on > 0 && off.length > 0; // until confirmed
    const ids = (turnOn ? off : own).map((f) => f.id);
    if (!ids.length) {
      toast(req ? 'These fonts are installed by your organisation and stay on' : 'Nothing to switch');
      return;
    }
    const where = whole ? '' : ' in this view';
    const go = () => setActive(ids, turnOn);
    if (ids.length <= 10) { go(); return; }
    if (turnOn) {
      dialog('Switch on all fonts', `<p>Switch on <b>${esc(fontsLabel(fontsByIds(ids)))}</b>${where}? They are installed on all your computers within a minute.</p>
        <p class="small muted">Fonts under licenses without free seats are left off.</p>`, { submit: 'Switch on', onSubmit: go });
    } else {
      dialog('Switch off all fonts', `<p>Switch off <b>${esc(fontsLabel(fontsByIds(ids)))}</b>${where}? They are removed from your computers at their next sync.</p>
        ${req ? `<p class="small muted">${esc(fontsLabel(shown.filter((f) => f.required)))} installed by your organisation ${req === 1 ? 'stays' : 'stay'} on.</p>` : ''}`, { submit: 'Switch off', onSubmit: go });
    }
  };
}

function drawBulk() {
  const L = S.lib;
  const box = $('#bulk');
  if (!box || !S.me.is_admin) return;
  const n = L.selected.size;
  if (!n) { box.innerHTML = ''; return; }
  const inColl = !['all', 'active'].includes(String(L.coll));
  box.innerHTML = `<div class="bulk">
    <b>${esc(fontsLabel(fontsByIds([...L.selected])))} selected</b>
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
    `Permanently delete ${esc(fontsLabel(fontsByIds(ids)))} from the library? They will be removed from every device on its next sync.`,
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
  const [devices, downloads, plugins] = await Promise.all([
    api('GET', '/api/devices' + (showAll ? '?all=1' : '')),
    api('GET', '/api/downloads').catch(() => []),
    api('GET', '/api/downloads/plugins').catch(() => []),
  ]);
  const PLUGIN_NEEDS = { indesign: 'InDesign 2023 (18.5) or later', photoshop: 'Photoshop 2023 (24.2) or later', illustrator: 'Illustrator 2020 or later' };
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
    <div class="card dplugins">
      <h2 style="margin-top:0">Plugins for design apps</h2>
      <p class="small" style="color:var(--ink-2)">See which fonts a document is missing and switch them on without leaving the app. The plugins work through the typehaus app on the same computer – install and connect it first.</p>
      <div class="plist">${plugins.map((p) => `<div class="pitem"><b>${esc(p.app)}</b><span class="small muted">${PLUGIN_NEEDS[p.id] || ''}</span>
        ${p.available ? `<a class="btn sm" href="${esc(p.url)}" download>Download .${esc(p.format)}</a>` : '<span class="small muted">in the release builds</span>'}</div>`).join('')}
        <div class="pitem"><b>Affinity</b><span class="small muted">3.3 or later</span><span class="small">No plugin needed</span></div></div>
      <p class="small muted">Install: open the typehaus app's window and choose <b>Install panel</b> next to the app – or double-click a downloaded .ccx file and Creative Cloud installs it
        (it may point out that the plugin isn't from the Adobe Marketplace). Affinity: turn on <b>Settings → Model Context Protocol → Enable MCP server</b> and restart Affinity; typehaus connects by itself.</p>
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
  const [colls, groups] = await Promise.all([api('GET', '/api/collections'), api('GET', '/api/groups'), S.lib.loaded ? null : loadLibrary()]);
  const gname = (id) => groups.find((g) => g.id === id)?.name || '?';
  page.innerHTML = `
    <div class="row"><div><h1>Collections</h1><p class="sub">Collections decide who gets which fonts: <b>available</b> to switch on when needed, or <b>installed automatically</b> on every computer of the people they apply to. Add fonts to collections from the Library.</p></div>
      <span class="spacer"></span><button class="btn primary" id="new">New collection</button></div>
    ${colls.length ? `<table class="t"><thead><tr><th>Name</th><th>For</th><th>Distribution</th><th class="num">Fonts</th><th></th></tr></thead><tbody>
      ${colls.map((c) => `<tr><td><b>${esc(c.name)}</b>${c.description ? `<div class="small muted">${esc(c.description)}</div>` : ''}</td>
        <td>${c.everyone ? '<span class="badge on">Everyone</span>' : (c.group_ids.length ? c.group_ids.map((g) => `<span class="badge">${esc(gname(g))}</span>`).join(' ') : '<span class="muted small">Admins only</span>')}
          <div class="small muted">${plural(c.members, 'person', 'people')}</div></td>
        <td>${c.mode === 'installed' ? '<span class="badge inst">Installed automatically</span>' : '<span class="muted small">Available</span>'}</td>
        <td class="num"><a href="#/library" data-open="${c.id}" title="${plural(c.font_count, 'style')}">${plural(famCount(inCollection(c.id)), 'family', 'families')}</a></td>
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
      box.innerHTML = `<div><b>${c ? (S.lib.loaded ? esc(fontsLabel(inCollection(c.id))) : plural(r.fonts, 'style')) : 'The fonts you add'}</b> will be installed for <b>${plural(r.members, 'person', 'people')}</b>${r.devices ? ` on ${plural(r.devices, 'computer')} so far` : ''}.</div>
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
  return S.lib.licenses.filter((l) => l.expired || l.over || l.web_over || sharedInstalls(l) || (l.expires_in_days != null && l.expires_in_days <= 30));
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

// a license for named people that doesn't name the signed-in person
const namedOut = (l) => !!l && l.named_users && !l.licensed_to_me && !(S.me.is_admin && !l.count_admins);
const NAMED_TIP = 'The license for this font names the people who may use it, and you are not one of them. Request it to ask an administrator.';
document.addEventListener('click', (e) => {
  const b = e.target.closest('[data-askfont]');
  if (b) requestFont(`${b.dataset.askfont} (licensed to named people)`);
});

// badge shown next to a family in the library
function licenseBadge(l) {
  const tip = esc(`${l.name}${l.vendor ? ' · ' + l.vendor : ''}`);
  if (namedOut(l)) return `<span class="badge warn" title="${esc(NAMED_TIP)}">licensed to named people</span>`;
  if (l.expired) return `<span class="badge warn" title="${tip}">license expired${l.enforce ? ' – not installed' : ''}</span>`;
  if (l.enforce && l.full && !l.holder) return `<span class="badge warn" title="${tip}: all seats are in use">no free seat</span>`;
  if (l.named_users && l.licensed_to_me) return `<span class="badge" title="${tip}: licensed to you by name">${esc(l.name)} · licensed to you</span>`;
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
  if (l.web_over) b.push('<span class="badge danger" title="Websites using these fonts went over the monthly page views">over web page views</span>');
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
      ${l.web_pageviews || l.web_views ? `<dt>Web page views</dt><dd>${l.web_views.toLocaleString()} this month${l.web_pageviews ? ` of ${l.web_pageviews.toLocaleString()}` : ''}${l.web_over ? ' <span class="badge danger">over</span>' : ''}</dd>` : ''}
      ${l.lease_days ? `<dt>Offline use</dt><dd>Up to ${plural(l.lease_days, 'day')} without contact to the server, then the fonts pause until the computer is back online</dd>` : ''}
      <dt>Seats</dt><dd>${seatText(l)}${l.holder ? ' <span class="muted small">· you hold one</span>' : ''}</dd>
      <dt>Term</dt><dd>${termText(l)}${l.purchased_on ? ` <span class="muted small">· purchased ${esc(l.purchased_on)}</span>` : ''}</dd>
      ${l.reference ? `<dt>Reference</dt><dd>${esc(l.reference)}</dd>` : ''}
      ${l.url ? `<dt>Terms</dt><dd><a href="${esc(l.url)}" target="_blank" rel="noopener noreferrer">${esc(l.url.replace(/^https?:\/\//, ''))} ↗</a></dd>` : ''}
      ${l.documents.length ? `<dt>Documents</dt><dd>${l.documents.map((d) => `<span class="muted small">${DOC_KINDS[d.kind] || 'Document'}:</span> <a href="${docURL(l, d)}" target="_blank" rel="noopener">${esc(d.filename)}</a>`).join('<br>')}</dd>` : ''}
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
    <div class="row wf-head" data-instant><h4>Waterfall</h4><span class="spacer"></span>
      ${fam.faces.length > 1 ? `<select id="wf-face" aria-label="Style">${fam.faces.map((f) => `<option value="${f.id}" ${f.id === r.id ? 'selected' : ''}>${esc((f.opsz ? f.opsz + ' ' : '') + f.style)}</option>`).join('')}</select>` : ''}</div>
    <div class="waterfall" id="wf">${WATERFALL.map((pt) => `<div class="wf-row"><span class="wf-sz">${pt} pt</span><div class="wf-txt loading" style="font-size:${pt}pt">${esc(L.preview || SAMPLES[0].text)}</div></div>`).join('')}</div>
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
  const showFace = (id) => loadFace(id).then((ff) => $$('.wf-txt', d).forEach((el) => {
    el.classList.remove('loading');
    if (ff) el.style.fontFamily = `"${ff}", "Adobe NotDef", monospace`;
    else { el.classList.add('nopreview'); el.textContent = 'No browser preview for this format'; }
  }));
  showFace(r.id);
  const wfFace = $('#wf-face', d);
  if (wfFace) wfFace.onchange = () => { $$('.wf-txt', d).forEach((el) => el.classList.add('loading')); showFace(+wfFace.value); };
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
          <div class="small muted">${[l.vendor, rightsSummary(l)].filter(Boolean).map(esc).join(' · ')}</div>
          ${l.named_users && l.licensed_to.length ? `<div class="small" title="Only these people may use the fonts">Licensed to ${l.licensed_to.map((p) => esc(p.display_name || p.username)).join(', ')}</div>` : ''}</td>
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
  const sub = `<p class="small muted">${plural(inv.devices_scanned, 'computer')} reported · ${plural(inv.unmanaged_files, 'font file')} installed outside typehaus. Listed below: copies of library families that are not open source – they use a license without being counted.</p>`;
  if (!inv.findings.length) return `${head}${sub}<p><span class="badge open">none found</span> No copies of licensed library fonts.</p>`;
  return `<div class="row"><h2>Installed outside typehaus</h2><span class="spacer"></span>
      <a class="btn sm" href="/api/inventory.csv" download title="Every copy found, one row per file – for audits">Export CSV</a></div>${sub}
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

// peoplePicker: chosen people as chips, a search field to add more.
function peoplePicker(box, users, selected) {
  const label = (u) => u.display_name ? `${u.display_name} (${u.username})` : u.username;
  const chosen = new Set(selected);
  const byLabel = new Map(users.map((u) => [label(u).toLowerCase(), u]));
  const listID = 'pp-' + Math.random().toString(36).slice(2);
  const draw = () => {
    const sel = users.filter((u) => chosen.has(u.id));
    box.innerHTML = `<div class="chips">${sel.map((u) => `<span class="chip on">${esc(label(u))}<button type="button" class="chip-x" data-rm="${u.id}" aria-label="Remove ${esc(label(u))}">×</button></span>`).join('')}
      <input type="text" class="pp-add" list="${listID}" placeholder="${sel.length ? 'Add someone…' : 'Everyone who can see the fonts – type a name to limit it'}" autocomplete="off"></div>
      <datalist id="${listID}">${users.filter((u) => !chosen.has(u.id) && !u.disabled).map((u) => `<option value="${esc(label(u))}">`).join('')}</datalist>`;
    $$('[data-rm]', box).forEach((b) => b.onclick = () => { chosen.delete(+b.dataset.rm); draw(); box.dispatchEvent(new Event('input', { bubbles: true })); $('.pp-add', box).focus(); });
    const inp = $('.pp-add', box);
    const take = () => {
      const u = byLabel.get(inp.value.trim().toLowerCase()) || users.find((x) => x.username.toLowerCase() === inp.value.trim().toLowerCase());
      if (u) { chosen.add(u.id); draw(); box.dispatchEvent(new Event('input', { bubbles: true })); $('.pp-add', box).focus(); }
    };
    inp.addEventListener('input', () => { if (byLabel.has(inp.value.trim().toLowerCase())) take(); });
    inp.addEventListener('keydown', (e) => { if (e.key === 'Enter') { e.preventDefault(); take(); } });
  };
  draw();
  return { ids: () => [...chosen] };
}

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
    <div class="field"><span>Licensed to <span class="muted">(optional – named people)</span></span>
      <div id="lic-people" class="people-pick"><span class="small muted">Loading people…</span></div>
      <p class="small muted hint" style="margin:6px 0 0">Only these people may switch the fonts on, download them or have them installed by a collection; everyone else sees the fonts locked and can request them. Leave empty for a license that covers everyone who can see the fonts.</p></div>
    <div class="two">
      <label class="field">Purchased<input type="date" name="purchased_on" value="${esc(l?.purchased_on)}"></label>
      <label class="field">Valid until (empty = perpetual)<input type="date" name="expires_on" value="${esc(l?.expires_on)}"></label>
    </div>
    <div class="two">
      <label class="field">Web page views per month (0 = no limit)<input type="number" name="web_pageviews" min="0" step="1000" value="${l?.web_pageviews ?? 0}"></label>
      <p class="small muted hint">For web font licenses priced by traffic: page views of websites using these fonts through a web kit (Integrations → Web fonts) count against this. Going over doesn't break sites – typehaus flags it.</p>
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
    ${l ? `<h4>Documents</h4><div id="docbox" data-instant>${docsBlock(l)}</div>`
    : `<label class="field">Documents <span class="muted">(EULA, invoice, certificate… – optional)</span><input type="file" name="docs" multiple></label>
      <label class="check"><input type="checkbox" name="docs_shared" checked><span>Documents visible to users who can see these fonts</span></label>`}`,
  {
    wide: true,
    onSubmit: async (fd) => {
      const body = {
        name: fd.get('name'), kind: fd.get('kind'),
        vendor: fd.get('vendor'), rights: fd.getAll('rights'), lease_days: +fd.get('lease_days') || 0, web_pageviews: +fd.get('web_pageviews') || 0,
        seats: +fd.get('seats') || 0, seat_unit: fd.get('seat_unit'), enforce: fd.get('enforce') === 'on', downloadable: fd.get('downloadable') === 'on',
        count_admins: fd.get('count_admins') === 'on',
        purchased_on: fd.get('purchased_on'), expires_on: fd.get('expires_on'),
        reference: fd.get('reference'), url: fd.get('url'), notes: fd.get('notes'),
      };
      if (people) body.licensed_to = people.ids();
      if (l) await api('PATCH', `/api/licenses/${l.id}`, body);
      else {
        const r = await api('POST', '/api/licenses', body);
        const files = fd.getAll('docs').filter((f) => f && f.size);
        if (files.length) {
          try { await uploadDocs(r.id, files, fd.get('docs_shared') === 'on'); } catch (e) { fail(e); }
        }
        setTimeout(() => licenseDetail(r.id, done), 0);
      }
      toast('License saved');
      done();
    },
  });
  let people = null;
  api('GET', '/api/users').then((users) => {
    people = peoplePicker($('#lic-people', d), users, (l?.licensed_to || []).map((p) => p.id));
  }).catch(() => { $('#lic-people', d).innerHTML = '<span class="small warnt">Couldn\'t load the people list.</span>'; });
  if (l) wireDocs($('#docbox', d), l, done);
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

// ---- license documents: EULA, invoice, certificate… ----------------------
const DOC_KINDS = { eula: 'EULA', invoice: 'Invoice', certificate: 'Certificate', other: 'Document' };

function docsBlock(l) {
  return `<p class="small muted">EULA, invoice, license certificate… Drop files here or attach them. Documents marked <i>visible to users</i>
      can be opened by everyone who can see these fonts; the others only by admins. Changes here are saved right away.</p>
    <div class="docs">${l.documents.map((doc) => `<div class="doc" data-doc="${doc.id}">
      <select class="doc-kind" aria-label="Type">${Object.entries(DOC_KINDS).map(([k, v]) => `<option value="${k}" ${doc.kind === k ? 'selected' : ''}>${v}</option>`).join('')}</select>
      <div class="doc-main">
        <a class="doc-name" href="${docURL(l, doc)}" target="_blank" rel="noopener">${esc(doc.filename)}</a>
        <div class="small muted">${fmtSize(doc.size)} · ${doc.updated_at ? `replaced ${ago(doc.updated_at)}` : `added ${ago(doc.created_at)}`}${doc.uploaded_by ? ` by ${esc(doc.uploaded_by)}` : ''}</div>
      </div>
      <label class="small"><input type="checkbox" class="doc-shared" ${doc.shared ? 'checked' : ''}> visible to users</label>
      <span class="doc-acts">
        <button type="button" class="linkbtn doc-ren">Rename</button>
        <button type="button" class="linkbtn doc-rep" title="Upload a new file for this document (e.g. a renewed invoice); type and visibility stay">Replace</button>
        <a class="small" href="${docURL(l, doc, true)}">Download</a>
        <button type="button" class="btn sm danger doc-del">Remove</button>
      </span></div>`).join('') || '<p class="muted small doc-none">No documents attached yet.</p>'}</div>
    <div class="docup"><button type="button" class="btn sm" id="doc-add">Attach files…</button>
      <label class="small"><input type="checkbox" id="doc-sharednew" checked> visible to users</label>
      <span class="small muted">Type is taken from the file name and can be changed.</span>
      <input type="file" id="doc-file" multiple hidden><input type="file" id="doc-repfile" hidden></div>`;
}

async function uploadDocs(lid, files, shared) {
  const fd = new FormData();
  fd.append('shared', shared ? '1' : '0');
  files.forEach((f) => fd.append('files', f, f.name));
  await api('POST', `/api/licenses/${lid}/documents`, fd);
}

// wireDocs makes a docsBlock editable in place; changed() tells the page
// behind the dialog (counts, list) to refresh.
function wireDocs(box, l, changed = () => {}) {
  const refresh = async (msg) => {
    if (msg) toast(msg);
    try {
      const r = await api('GET', `/api/licenses/${l.id}`);
      l.documents = r.license.documents;
      box.innerHTML = docsBlock(l);
      wireDocs(box, l, changed);
    } catch (e) { fail(e); }
    changed();
  };
  const path = (did) => `/api/licenses/${l.id}/documents/${did}`;
  const add = async (files) => {
    if (!files.length) return;
    try { await uploadDocs(l.id, files, $('#doc-sharednew', box).checked); refresh(`${plural(files.length, 'document')} attached`); } catch (e) { fail(e); }
  };
  $('#doc-add', box).onclick = () => $('#doc-file', box).click();
  $('#doc-file', box).onchange = (e) => add([...e.target.files]);
  // drag & drop onto the documents area
  box.ondragover = (e) => { if (e.dataTransfer?.types.includes('Files')) { e.preventDefault(); box.classList.add('dragging'); } };
  box.ondragleave = (e) => { if (!box.contains(e.relatedTarget)) box.classList.remove('dragging'); };
  box.ondrop = (e) => { e.preventDefault(); box.classList.remove('dragging'); add([...e.dataTransfer.files]); };
  let replacing = null;
  $('#doc-repfile', box).onchange = async (e) => {
    const f = e.target.files[0];
    if (!f || !replacing) return;
    const fd = new FormData();
    fd.append('file', f, f.name);
    try { await api('PUT', path(replacing), fd); refresh('Document replaced'); } catch (ex) { fail(ex); }
  };
  $$('.doc', box).forEach((row) => {
    const did = row.dataset.doc;
    const doc = l.documents.find((x) => String(x.id) === did);
    $('.doc-kind', row).onchange = async (e) => {
      try { await api('PATCH', path(did), { kind: e.target.value }); doc.kind = e.target.value; toast(`Marked as ${DOC_KINDS[e.target.value]}`); changed(); } catch (ex) { fail(ex); }
    };
    $('.doc-shared', row).onchange = async (e) => {
      try { await api('PATCH', path(did), { shared: e.target.checked }); doc.shared = e.target.checked; toast(e.target.checked ? 'Visible to users' : 'Admins only'); changed(); } catch (ex) { fail(ex); }
    };
    $('.doc-rep', row).onclick = () => { replacing = did; const inp = $('#doc-repfile', box); inp.value = ''; inp.click(); };
    $('.doc-ren', row).onclick = () => {
      const link = $('.doc-name', row);
      const inp = document.createElement('input');
      inp.type = 'text'; inp.className = 'doc-rename'; inp.value = doc.filename; inp.maxLength = 200;
      link.replaceWith(inp);
      inp.focus();
      const dot = doc.filename.lastIndexOf('.');
      inp.setSelectionRange(0, dot > 0 ? dot : doc.filename.length); // keep the extension
      let done = false;
      const finish = async (save) => {
        if (done) return;
        done = true;
        const v = inp.value.trim();
        if (save && v && v !== doc.filename) {
          try { await api('PATCH', path(did), { filename: v }); refresh('Renamed'); return; } catch (ex) { fail(ex); }
        }
        inp.replaceWith(link);
      };
      inp.onkeydown = (e) => {
        if (e.key === 'Enter') { e.preventDefault(); finish(true); }
        if (e.key === 'Escape') { e.preventDefault(); e.stopPropagation(); finish(false); }
      };
      inp.onblur = () => finish(true);
    };
    $('.doc-del', row).onclick = () => confirmDialog('Remove document', `Remove <b>${esc(doc.filename)}</b> from this license? The file is deleted from typehaus.`,
      async () => { await api('DELETE', path(did)); refresh('Document removed'); }, 'Remove');
  });
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
    <div id="docbox" data-instant>${docsBlock(l)}</div>
    <h4>Usage, last 12 months <span class="muted small">peak ${l.seat_unit === 'devices' ? l.peak_devices : l.peak_users} ${l.seat_unit}</span></h4>
    ${usageChart(hist, l)}
    <h4>Fonts <span class="muted small">${plural(r.families.length, 'family', 'families')}</span></h4>
    <p class="small">${r.families.map((f) => `${esc(f.family)} <span class="muted">(${f.styles})</span>`).join(', ') || '<span class="muted">None assigned – select fonts in the Library and choose “Assign license”.</span>'}</p>
    ${l.named_users && l.licensed_to.length ? `<h4>Licensed to <span class="muted small">only these people may use the fonts</span></h4>
    <p class="small">${l.licensed_to.map((p) => esc(p.display_name ? `${p.display_name} (${p.username})` : p.username)).join(', ')}</p>` : ''}
    <h4>${l.seat_unit === 'devices' ? 'Devices' : 'People'} holding a seat <span class="muted small">${seatText(l)}</span></h4>
    <p class="small">${(r.holders || []).map((h) => `${esc(h.name)}${h.extra ? ` <span class="muted">(${esc(h.extra)})</span>` : ''}`).join(', ') || '<span class="muted">Nobody yet.</span>'}</p>`,
  { submit: 'Close', cancel: false, wide: true, onSubmit: async () => {} });
  if (hist.length) wireUsageChart(d, hist, l);
  wireDocs($('#docbox', d), l, done);
}

// ---------------------------------------------------------------------------
// API tokens (admin)
// ---------------------------------------------------------------------------
const INTEGRATION_TABS = { tokens: 'API tokens', webhooks: 'Webhooks', webfonts: 'Web fonts', services: 'Services' };
async function renderIntegrations(page) {
  let tab = new URLSearchParams(location.hash.split('?')[1] || '').get('tab');
  if (!INTEGRATION_TABS[tab]) tab = 'tokens';
  page.innerHTML = `<h1>Integrations</h1>
    <p class="sub">Connect other tools through the API, and the outside services typehaus uses.</p>
    <div class="tabs">${Object.entries(INTEGRATION_TABS).map(([k, v]) => `<button data-tab="${k}" class="${k === tab ? 'active' : ''}">${v}</button>`).join('')}</div>
    <div id="intbox"></div>`;
  $$('.tabs button', page).forEach((b) => b.onclick = () => { location.hash = `#/integrations?tab=${b.dataset.tab}`; });
  const box = $('#intbox', page);
  if (tab === 'services') await renderServices(box);
  else if (tab === 'webhooks') await renderWebhooks(box);
  else if (tab === 'webfonts') await renderWebfonts(box);
  else await renderAPI(box);
}

// ---- webhooks ----------------------------------------------------------------
const HOOK_STATUS = { delivered: '<span class="badge open">delivered</span>', failed: '<span class="badge danger">failed</span>', pending: '<span class="badge">pending</span>' };
async function renderWebhooks(box) {
  const [hooks, events] = await Promise.all([api('GET', '/api/webhooks'), api('GET', '/api/webhook-events')]);
  const refresh = () => renderWebhooks(box).catch(fail);
  box.innerHTML = `<div class="row"><p class="sub" style="margin:0">typehaus sends a signed POST to your URL when something happens – a font is added, a license expires, someone requests a font.
      Failed deliveries are retried for about a day.</p><span class="spacer"></span><button class="btn primary" id="newhook">New webhook</button></div>
    ${hooks.length ? `<table class="t" style="margin-top:16px"><thead><tr><th>Name</th><th>Events</th><th>Last delivery</th><th></th></tr></thead><tbody>
      ${hooks.map((h) => `<tr><td><b>${esc(h.name)}</b>${h.active ? '' : ' <span class="badge">paused</span>'}<div class="small muted mono">${esc(h.url)}</div></td>
        <td class="small">${h.events.includes('*') ? 'All events' : h.events.map(esc).join(', ')}</td>
        <td class="small">${h.last_status ? `${HOOK_STATUS[h.last_status] || esc(h.last_status)} ${h.last_at ? ago(h.last_at) : ''}` : '<span class="muted">none yet</span>'}
          ${h.failed_recent ? `<div class="warnt">${plural(h.failed_recent, 'failure')} in 7 days</div>` : ''}</td>
        <td style="text-align:right" class="nowrap"><button class="btn sm" data-test="${h.id}">Send test</button> <button class="btn sm" data-log="${h.id}">Deliveries</button>
          <button class="btn sm" data-edit="${h.id}">Edit</button></td></tr>`).join('')}</tbody></table>`
    : '<div class="empty"><div class="big">No webhooks yet.</div>Connect a chat, a ticket system or your own service to typehaus events.</div>'}
    <details class="hookhelp"><summary>Verifying deliveries</summary>
      <p class="small">Each delivery carries <code>X-Typehaus-Event</code>, <code>X-Typehaus-Delivery</code>, <code>X-Typehaus-Timestamp</code> and
      <code>X-Typehaus-Signature: v1=&lt;hex&gt;</code> – the HMAC-SHA256 of <code>&lt;timestamp&gt;.&lt;body&gt;</code> with the webhook's secret.
      Reject a delivery if the signature doesn't match or the timestamp is older than five minutes. Answer with 2xx within 10 seconds.</p></details>`;
  const form = (h) => `
    <label class="field">Name<input type="text" name="name" required maxlength="100" value="${esc(h?.name)}" placeholder="e.g. Design team chat"></label>
    <label class="field">URL<input type="url" name="url" required placeholder="https://" value="${esc(h?.url)}"></label>
    <fieldset class="field"><legend>Events</legend>
      <label class="check"><input type="checkbox" name="all" ${!h || h.events.includes('*') ? 'checked' : ''}><span><b>All events</b>, including ones added in later versions</span></label>
      <div class="checks evlist">${events.map((e) => `<label class="check"><input type="checkbox" name="ev" value="${esc(e.type)}" ${h && h.events.includes(e.type) ? 'checked' : ''}>
        <span><span class="mono">${esc(e.type)}</span> <span class="small muted">${esc(e.description)}</span></span></label>`).join('')}</div></fieldset>
    ${h ? `<label class="check"><input type="checkbox" name="active" ${h.active ? 'checked' : ''}><span><b>Active</b> – paused webhooks get no deliveries</span></label>` : ''}`;
  const wireForm = (d) => {
    const all = $('[name=all]', d);
    const sync = () => $$('[name=ev]', d).forEach((c) => { c.disabled = all.checked; });
    all.onchange = sync; sync();
  };
  const body = (fd) => ({ name: fd.get('name'), url: fd.get('url'), events: fd.get('all') ? ['*'] : fd.getAll('ev') });
  $('#newhook', box).onclick = () => {
    const d = dialog('New webhook', form(null), { submit: 'Create webhook', wide: true, onSubmit: async (fd) => {
      const b = body(fd);
      if (!b.events.length) throw new Error('Choose at least one event');
      const r = await api('POST', '/api/webhooks', b);
      refresh();
      showSecret(fd.get('name'), r.secret);
    } });
    wireForm(d);
  };
  $$('[data-edit]', box).forEach((btn) => btn.onclick = () => {
    const h = hooks.find((x) => x.id === +btn.dataset.edit);
    const d = dialog(`Webhook “${h.name}”`, form(h), { wide: true,
      extra: { label: 'Delete', danger: true, onClick: () => confirmDialog('Delete webhook', `Delete <b>${esc(h.name)}</b>? Pending deliveries are dropped.`,
        async () => { await api('DELETE', `/api/webhooks/${h.id}`); toast('Webhook deleted'); refresh(); }) },
      onSubmit: async (fd) => {
        const b = body(fd);
        if (!b.events.length) throw new Error('Choose at least one event');
        await api('PATCH', `/api/webhooks/${h.id}`, { ...b, active: fd.get('active') === 'on' });
        toast('Webhook saved'); refresh();
      } });
    wireForm(d);
    const row = document.createElement('p');
    row.className = 'small';
    row.innerHTML = '<button type="button" class="linkbtn">Create a new signing secret…</button> The old one stops working at once.';
    $('form', d)?.insertBefore(row, $('form .actions', d) || null);
    row.querySelector('button').onclick = () => confirmDialog('New signing secret', 'The receiver must switch to the new secret – deliveries signed with it fail verification until then.', async () => {
      const r = await api('POST', `/api/webhooks/${h.id}/secret`);
      showSecret(h.name, r.secret);
    }, 'Create new secret');
  });
  $$('[data-test]', box).forEach((btn) => btn.onclick = async () => {
    try { await api('POST', `/api/webhooks/${btn.dataset.test}/test`); toast('Test event sent'); setTimeout(refresh, 1500); } catch (e) { fail(e); }
  });
  $$('[data-log]', box).forEach((btn) => btn.onclick = () => hookLog(hooks.find((x) => x.id === +btn.dataset.log), refresh));
}

function showSecret(name, secret) {
  const d = dialog(`Signing secret for “${name}”`, `
    <p>Copy the secret now – typehaus shows it only once. Your receiver uses it to check the <code>X-Typehaus-Signature</code> header.</p>
    <div class="tokbox"><code id="tokval">${esc(secret)}</code><button type="button" class="btn sm" id="tokcopy">Copy</button></div>`,
  { submit: 'Done', cancel: false, onSubmit: async () => {} });
  $('#tokcopy', d).onclick = () => navigator.clipboard.writeText(secret).then(() => toast('Secret copied'), () => toast('Press Ctrl+C to copy'));
}

async function hookLog(h, done) {
  const rows = await api('GET', `/api/webhooks/${h.id}/deliveries?limit=50`);
  const d = dialog(`Deliveries – ${h.name}`, rows.length ? `<table class="t"><thead><tr><th>When</th><th>Event</th><th>Result</th><th></th></tr></thead><tbody>
    ${rows.map((x) => `<tr><td class="small" title="${fmtDate(x.created_at)}">${ago(x.created_at)}</td><td class="mono">${esc(x.event)}</td>
      <td class="small">${HOOK_STATUS[x.status] || esc(x.status)} ${x.last_code ? `HTTP ${x.last_code}` : ''} ${x.attempts > 1 ? `· ${x.attempts} attempts` : ''}
        ${x.last_error ? `<div class="muted">${esc(x.last_error)}</div>` : ''}${x.status === 'pending' && x.next_at ? `<div class="muted">next try ${new Date(x.next_at * 1000).toLocaleTimeString()}</div>` : ''}</td>
      <td style="text-align:right">${x.status === 'failed' || x.status === 'delivered' ? `<button type="button" class="btn sm" data-redo="${x.id}">Send again</button>` : ''}</td></tr>`).join('')}</tbody></table>`
    : '<p class="muted">No deliveries in the last 30 days.</p>', { submit: 'Close', cancel: false, wide: true, onSubmit: async () => done() });
  $$('[data-redo]', d).forEach((b) => b.onclick = async () => {
    try { await api('POST', `/api/webhooks/${h.id}/deliveries/${b.dataset.redo}/redeliver`); b.replaceWith(Object.assign(document.createElement('span'), { className: 'badge', textContent: 'queued' })); } catch (e) { fail(e); }
  });
}

// ---- web fonts ---------------------------------------------------------------
async function renderWebfonts(box) {
  if (!S.lib.loaded) await loadLibrary();
  const kits = await api('GET', '/api/webkits');
  const refresh = () => renderWebfonts(box).catch(fail);
  box.innerHTML = `<div class="row"><p class="sub" style="margin:0">Serve library fonts to your websites. A web kit is one stylesheet a site links to; typehaus serves only fonts whose license
      includes web use, only to the domains you list, and counts page views against the licenses.</p><span class="spacer"></span><button class="btn primary" id="newkit">New web kit</button></div>
    ${kits.length ? kits.map((k) => {
      const ok = k.fonts.filter((f) => f.served);
      const out = k.fonts.filter((f) => !f.served);
      const fams = [...new Set(ok.map((f) => f.family))];
      const tag = `<link rel="stylesheet" href="${k.css_url}">`;
      return `<div class="card kit">
        <div class="row"><h3 style="margin:0">${esc(k.name)}</h3>${k.enabled ? '' : '<span class="badge">off</span>'}
          <span class="small muted">${plural(fams.length, 'family', 'families')} · ${plural(ok.length, 'style')} · ${k.views_this_month === 1 ? "1 page view" : `${k.views_this_month.toLocaleString()} page views`} this month</span>
          <span class="spacer"></span><button class="btn sm" data-kedit="${k.id}">Edit</button></div>
        <div class="small">${k.domains.length ? `Allowed on ${k.domains.map((d) => `<span class="chip">${esc(d)}</span>`).join(' ')}` : '<span class="warnt">No domains yet – no website can use this kit.</span>'}</div>
        ${out.length ? `<details class="small"><summary class="warnt">${plural(out.length, 'style')} left out</summary><ul>${out.map((f) => `<li><b>${esc(f.family)} ${esc(f.style)}</b>: ${esc(f.problem)}</li>`).join('')}</ul></details>` : ''}
        <div class="embed"><span class="small muted">Add to the <code>&lt;head&gt;</code> of every page:</span>
          <div class="tokbox"><code>${esc(tag)}</code><button type="button" class="btn sm" data-copy="${esc(tag)}">Copy</button></div>
          ${fams.length ? `<span class="small muted">Then use ${fams.map((f) => `<code>font-family: "${esc(f)}"</code>`).join(', ')}.</span>` : ''}</div>
      </div>`;
    }).join('') : '<div class="empty"><div class="big">No web kits yet.</div>Create one for each website or brand portal.</div>'}`;
  $$('[data-copy]', box).forEach((b) => b.onclick = () => navigator.clipboard.writeText(b.dataset.copy).then(() => toast('Copied'), () => toast('Press Ctrl+C to copy')));
  const lic = new Map(S.lib.licenses.map((l) => [l.id, l]));
  const webOK = (f) => {
    if (f.kind !== 'sfnt') return 'not a TrueType/OpenType font';
    const l = lic.get(f.license_id);
    if (!l) return 'no license';
    if (!l.rights.includes('web')) return `“${l.name}” doesn't include web use`;
    if (l.expired) return `“${l.name}” has expired`;
    return '';
  };
  const form = (k) => {
    const chosen = new Set((k?.fonts || []).map((f) => f.id));
    const fams = groupFamilies(S.lib.fonts);
    return `<label class="field">Name<input type="text" name="name" required maxlength="200" value="${esc(k?.name)}" placeholder="e.g. Company website"></label>
      <label class="field">Domains <span class="muted">(one per line – *.example.com includes subdomains)</span>
        <textarea name="domains" rows="3" placeholder="www.example.com&#10;*.example.com">${esc((k?.domains || []).join('\n'))}</textarea></label>
      <fieldset class="field"><legend>Fonts</legend><input type="search" class="kfilter" placeholder="Filter families">
        <div class="checks kitfonts">${fams.map((fam) => {
          const probs = fam.faces.map(webOK);
          const usable = probs.filter((p) => !p).length;
          const ids = fam.faces.map((f) => f.id);
          const on = ids.some((id) => chosen.has(id));
          return `<label class="check" data-fam="${esc(fam.name.toLowerCase())}"><input type="checkbox" name="fam" value="${ids.join(',')}" ${on ? 'checked' : ''} ${usable || on ? '' : 'disabled'}>
            <span><b>${esc(fam.name)}</b> <span class="small muted">${usable === fam.faces.length ? plural(usable, 'style') : usable ? `${usable} of ${plural(fam.faces.length, 'style')} licensed for web use` : esc(probs[0])}</span></span></label>`;
        }).join('')}</div></fieldset>
      ${k ? `<label class="check"><input type="checkbox" name="enabled" ${k.enabled ? 'checked' : ''}><span><b>On</b> – off: websites get no fonts from this kit</span></label>` : ''}`;
  };
  const body = (fd) => ({
    name: fd.get('name'),
    domains: String(fd.get('domains') || '').split(/[\s,]+/).filter(Boolean),
    font_ids: fd.getAll('fam').flatMap((v) => v.split(',').map(Number)),
  });
  const wire = (d) => {
    const f = $('.kfilter', d);
    f.oninput = () => $$('[data-fam]', d).forEach((l) => { l.hidden = !l.dataset.fam.includes(f.value.trim().toLowerCase()); });
  };
  $('#newkit', box).onclick = () => {
    const d = dialog('New web kit', form(null), { submit: 'Create web kit', wide: true, onSubmit: async (fd) => {
      await api('POST', '/api/webkits', body(fd)); toast('Web kit created'); refresh();
    } });
    wire(d);
  };
  $$('[data-kedit]', box).forEach((btn) => btn.onclick = () => {
    const k = kits.find((x) => x.id === +btn.dataset.kedit);
    const d = dialog(`Web kit “${k.name}”`, form(k), { wide: true,
      extra: { label: 'Delete', danger: true, onClick: () => confirmDialog('Delete web kit', `Delete <b>${esc(k.name)}</b>? Websites using it lose these fonts at once.`,
        async () => { await api('DELETE', `/api/webkits/${k.id}`); toast('Web kit deleted'); refresh(); }) },
      onSubmit: async (fd) => { await api('PATCH', `/api/webkits/${k.id}`, { ...body(fd), enabled: fd.get('enabled') === 'on' }); toast('Web kit saved'); refresh(); } });
    wire(d);
  });
}

async function renderServices(box) {
  const svcs = await api('GET', '/api/services');
  box.innerHTML = `<p class="small muted">Font identification sends an image to these services and matches the answers against your library and the free catalog.
      Enter a key for each service you want to use – they are asked together. Keys are kept on this server and never shown again;
      a key set through an environment variable (e.g. <code>TYPEHAUS_WHATFONTIS_KEY</code>) takes precedence and can only be changed there.</p>
    ${svcs.some((x) => !x.enabled) ? '<div class="alert">Identification is switched off on this server (TYPEHAUS_IDENTIFY=false); keys entered here are kept but not used.</div>' : ''}
    <div class="grid2">${svcs.map((x) => `<form class="card svc" data-svc="${esc(x.id)}">
      <div class="row"><h3 style="margin:0">${esc(x.name)}</h3>${x.configured ? '<span class="badge open">ready</span>' : '<span class="badge">no key</span>'}
        <span class="spacer"></span>${x.signup ? `<a class="small" href="${esc(x.signup)}" target="_blank" rel="noopener noreferrer">Get a key ↗</a>` : ''}</div>
      <p class="small">${esc(x.about)}</p>
      ${x.privacy ? `<p class="small muted">${esc(x.privacy)}</p>` : ''}
      ${x.fields.map((f) => `<label class="field">${esc(f.label)}
        ${f.from_env ? `<input type="text" disabled value="set by ${esc(f.env)}${f.hint ? ` (${esc(f.hint)})` : ''}">`
          : `<input type="${f.secret ? 'password' : 'text'}" name="${esc(f.name)}" autocomplete="off" spellcheck="false" maxlength="500"
            placeholder="${f.set ? (f.secret ? `saved${f.hint ? ` (${esc(f.hint)})` : ''} – type to replace` : '') : 'not set'}" value="${!f.secret && f.hint ? esc(f.hint) : ''}">`}</label>`).join('')}
      <div class="row">${x.fields.every((f) => f.from_env) ? '' : `<button class="btn sm primary">Save</button>
        ${x.fields.some((f) => f.set && !f.from_env) ? '<button type="button" class="btn sm danger" data-clear>Remove key</button>' : ''}`}</div>
    </form>`).join('')}</div>`;
  const refresh = () => renderServices(box).catch(fail);
  $$('form.svc', box).forEach((form) => {
    const id = form.dataset.svc;
    const x = svcs.find((v) => v.id === id);
    form.onsubmit = async (e) => {
      e.preventDefault();
      const body = {};
      for (const f of x.fields) {
        const inp = form.elements[f.name];
        if (!inp) continue;
        const v = inp.value.trim();
        if (f.secret && !v) continue; // keep the saved secret
        body[f.name] = v;
      }
      if (!Object.keys(body).length) { toast('Nothing to save'); return; }
      try { await api('PUT', `/api/services/${id}`, body); toast(`${x.name} saved`); refresh(); } catch (err) { fail(err); }
    };
    const clr = $('[data-clear]', form);
    if (clr) clr.onclick = () => confirmDialog(`Remove ${x.name} key`, `Identification stops asking ${esc(x.name)}.`, async () => {
      const body = {};
      x.fields.filter((f) => !f.from_env).forEach((f) => { body[f.name] = ''; });
      await api('PUT', `/api/services/${id}`, body);
      toast('Key removed'); refresh();
    }, 'Remove');
  });
}

async function renderAPI(page) {
  const toks = await api('GET', '/api/tokens');
  const base = location.origin;
  page.innerHTML = `
    <div class="row"><p class="sub" style="margin:0">Connect other tools – ticket systems, document servers, brand portals, scripts – through the typehaus API.</p>
      <span class="spacer"></span>
      <a class="btn" href="/api/v1/openapi.json" target="_blank" rel="noopener" title="OpenAPI 3.1 description of every endpoint">API description</a>
      <button class="btn primary" id="newtok">New token</button></div>
    <div class="apiintro">
      <p>Send a token with every request to <code>${esc(base)}/api/v1/…</code>:</p>
      <pre class="code">curl -H "Authorization: Bearer thk_…" ${esc(base)}/api/v1/licenses</pre>
      <p class="small muted">A token acts for your organisation, not for a person: <b>read</b> tokens can only look things up;
        <b>read &amp; write</b> tokens can change everything an administrator can – fonts, collections, licenses, people – except tokens.
        Font downloads through a token always follow the license rules. Every change is logged under the token's name.
        A token stops working when it is revoked, expires, or the administrator who created it loses admin rights.</p>
    </div>
    <h2>Tokens</h2>
    ${toks.length ? `<table class="t"><thead><tr><th>Name</th><th>Access</th><th>Created</th><th>Last used</th><th>Expires</th><th></th></tr></thead><tbody>
      ${toks.map((t) => {
        const expired = t.expires_at && t.expires_at * 1000 < Date.now();
        return `<tr><td><b>${esc(t.name)}</b> <span class="small muted mono">${esc(t.prefix)}…</span></td>
        <td>${t.scope === 'write' ? '<span class="badge warn">read &amp; write</span>' : '<span class="badge">read</span>'}</td>
        <td class="small">${ago(t.created_at)} <span class="muted">by ${esc(t.created_by)}</span></td>
        <td class="small">${t.last_used_at ? `${ago(t.last_used_at)}${t.last_ip ? ` <span class="muted">from ${esc(t.last_ip)}</span>` : ''}` : '<span class="muted">never</span>'}</td>
        <td class="small">${t.expires_at ? (expired ? '<span class="badge danger">expired</span>' : new Date(t.expires_at * 1000).toLocaleDateString()) : '<span class="muted">never</span>'}</td>
        <td style="text-align:right"><button class="btn sm danger" data-revoke="${t.id}">Revoke</button></td></tr>`;
      }).join('')}</tbody></table>`
    : '<div class="empty"><div class="big">No tokens yet.</div>Create one for each tool you connect, so you can revoke them separately.</div>'}`;
  const refresh = () => renderAPI(page).catch(fail);
  $('#newtok', page).onclick = () => dialog('New API token', `
    <label class="field">Name<input type="text" name="name" required maxlength="100" placeholder="e.g. Jira, Document server, Brand portal"></label>
    <fieldset class="field"><legend>Access</legend><div class="modes">
      <label class="mode"><input type="radio" name="scope" value="read" checked><span><b>Read</b><span class="small muted">Look up fonts, licenses, usage, reports. Can't change anything.</span></span></label>
      <label class="mode"><input type="radio" name="scope" value="write"><span><b>Read &amp; write</b><span class="small muted">Also upload fonts, manage collections, licenses and people – everything an administrator can, except tokens.</span></span></label>
    </div></fieldset>
    <label class="field">Expires<select name="expires_days"><option value="30">in 30 days</option><option value="90">in 90 days</option><option value="365" selected>in 1 year</option><option value="0">never</option></select></label>`,
  { submit: 'Create token', onSubmit: async (fd) => {
    const r = await api('POST', '/api/tokens', { name: fd.get('name'), scope: fd.get('scope'), expires_days: +fd.get('expires_days') });
    refresh();
    showToken(fd.get('name'), r.token);
  } });
  $$('[data-revoke]', page).forEach((b) => b.onclick = () => {
    const t = toks.find((x) => x.id === +b.dataset.revoke);
    confirmDialog('Revoke token', `Revoke <b>${esc(t.name)}</b>? Tools using it stop working immediately.`,
      async () => { await api('DELETE', `/api/tokens/${t.id}`); toast('Token revoked'); refresh(); }, 'Revoke');
  });
}

// The secret exists only in this dialog: typehaus stores a hash.
function showToken(name, tok) {
  const d = dialog(`Token “${name}”`, `
    <p>Copy the token now – typehaus doesn't keep it and can't show it again.</p>
    <div class="tokbox"><code id="tokval">${esc(tok)}</code><button type="button" class="btn sm" id="tokcopy">Copy</button></div>
    <p class="small muted">Store it like a password, e.g. in the tool's secret settings. If it gets lost, revoke it and create a new one.</p>`,
  { submit: 'Done', cancel: false, onSubmit: async () => {} });
  $('#tokcopy', d).onclick = async () => {
    try { await navigator.clipboard.writeText(tok); toast('Token copied'); } catch {
      const r = document.createRange(); r.selectNodeContents($('#tokval', d)); const s = getSelection(); s.removeAllRanges(); s.addRange(r);
      toast('Press Ctrl+C to copy');
    }
  };
}

// ---------------------------------------------------------------------------
// find fonts: the free catalog and font identification
// ---------------------------------------------------------------------------
const CAT_SOURCES = { google: 'Google Fonts', fontsquirrel: 'Font Squirrel', fontsource: 'Fontsource' };
const CAT_CATEGORIES = { 'sans-serif': 'Sans serif', serif: 'Serif', display: 'Display', handwriting: 'Handwriting', monospace: 'Monospace' };
const catFaces = new Map();
function loadCatalogFace(id) {
  if (!catFaces.has(id)) {
    const fam = `thc${catFaces.size}`;
    const ff = new FontFace(fam, `url(/api/catalog/preview/${encodeURIComponent(id)})`);
    catFaces.set(id, ff.load().then((f) => { document.fonts.add(f); return fam; }).catch(() => null));
  }
  return catFaces.get(id);
}
// Font Squirrel previews are images Font Squirrel only hands out slowly, so
// they load one after the other (and never hold up the rest of the page).
const imgQueue = [];
let imgBusy = false;
function queueImagePreview(el) { imgQueue.push(el); pumpImages(); }
function pumpImages() {
  if (imgBusy) return;
  const el = imgQueue.shift();
  if (!el) return;
  if (!document.body.contains(el)) { pumpImages(); return; }
  imgBusy = true;
  const img = new Image();
  img.alt = '';
  const next = () => { imgBusy = false; pumpImages(); };
  img.onload = () => { el.classList.remove('loading'); el.textContent = ''; el.append(img); next(); };
  img.onerror = () => { el.classList.remove('loading'); el.classList.add('nopreview'); el.textContent = 'No preview'; next(); };
  img.src = `/api/catalog/preview/${encodeURIComponent(el.dataset.img)}`;
}
const catalogObserver = new IntersectionObserver((entries) => {
  for (const en of entries) {
    if (!en.isIntersecting) continue;
    const el = en.target;
    catalogObserver.unobserve(el);
    if (el.dataset.img) { queueImagePreview(el); continue; }
    const face = el.dataset.font ? loadFace(+el.dataset.font) : loadCatalogFace(el.dataset.cat);
    face.then((fam) => {
      el.classList.remove('loading');
      if (fam) el.style.fontFamily = `"${fam}", "Adobe NotDef", sans-serif`;
      else { el.classList.add('nopreview'); el.textContent = 'No preview'; }
    });
  }
}, { rootMargin: '300px' });

// specimenFor shows a name in its own font: from the library if typehaus
// has it, else the catalog preview (an image for Font Squirrel).
function specimenFor(text, lib, cat, cls = 'specimen') {
  if (lib) return `<div class="${cls} loading" data-font="${lib.font_id}">${esc(text)}</div>`;
  if (cat && cat.source === 'fontsquirrel') return `<div class="${cls} loading catimg" data-img="${esc(cat.id)}">${esc(text)}</div>`;
  if (cat && cat.source) return `<div class="${cls} loading" data-cat="${esc(cat.id)}">${esc(text)}</div>`;
  return `<div class="${cls} plain">${esc(text)}</div>`;
}
function observeSpecimens(box) { $$('[data-font].loading, [data-cat].loading, [data-img].loading', box).forEach((el) => catalogObserver.observe(el)); }

async function renderFind(page) {
  const tab = new URLSearchParams(location.hash.split('?')[1] || '').get('tab') === 'identify' ? 'identify' : 'catalog';
  page.innerHTML = `<h1>Find fonts</h1>
    <p class="sub">Search free fonts from Google Fonts and Font Squirrel${S.me.is_admin ? ' and add them to the library' : ''}, or find out which font an image uses.</p>
    <div class="tabs"><button data-tab="catalog">Free fonts</button><button data-tab="identify">Identify a font</button></div>
    <div id="findbox"></div>`;
  $$('.tabs button', page).forEach((b) => {
    b.classList.toggle('active', b.dataset.tab === tab);
    b.onclick = () => { location.hash = `#/find${b.dataset.tab === 'identify' ? '?tab=identify' : ''}`; };
  });
  const box = $('#findbox', page);
  if (tab === 'identify') await renderIdentify(box);
  else await renderCatalog(box);
}

const CAT = { q: '', category: '', source: '', text: '', shown: 40, selected: new Set() };
async function renderCatalog(box) {
  box.innerHTML = `<div class="toolbar">
      <input type="search" id="cq" placeholder="Search families or foundries" value="${esc(CAT.q)}">
      <select id="ccat"><option value="">All styles</option>${Object.entries(CAT_CATEGORIES).map(([k, v]) => `<option value="${k}" ${CAT.category === k ? 'selected' : ''}>${v}</option>`).join('')}</select>
      <select id="csrc"><option value="">All sources</option>${Object.entries(CAT_SOURCES).map(([k, v]) => `<option value="${k}" ${CAT.source === k ? 'selected' : ''}>${v}</option>`).join('')}</select>
      <input type="text" class="preview-text" id="ctext" placeholder="Type to preview" value="${esc(CAT.text)}">
      ${viewToggle()}
    </div>
    <div id="cbulk"></div><div id="clist"><div class="more">Loading the catalog…</div></div>`;
  let list = [];
  const load = async () => {
    const p = new URLSearchParams();
    if (CAT.q) p.set('q', CAT.q);
    if (CAT.category) p.set('category', CAT.category);
    if (CAT.source) p.set('source', CAT.source);
    try {
      list = await api('GET', `/api/catalog?${p}`);
    } catch (e) {
      $('#clist', box).innerHTML = e.status === 404
        ? '<div class="empty"><div class="big">The free catalog is switched off.</div>This server runs without it (TYPEHAUS_CATALOG=false), e.g. because it has no internet access.</div>'
        : `<div class="empty"><div class="big">The catalog can't be reached.</div>${esc(e.message)}</div>`;
      return;
    }
    draw();
  };
  const draw = () => {
    const el = $('#clist', box);
    if (!list.length) { el.innerHTML = '<div class="empty"><div class="big">Nothing found.</div>Try fewer words, or another style or source.</div>'; drawBulk(); return; }
    const shown = list.slice(0, CAT.shown);
    const grid = S.view === 'grid';
    const sample = (f) => CAT.text || (grid ? PANGRAM : f.family);
    if (grid) {
      el.innerHTML = `<p class="small muted">${plural(list.length, 'family', 'families')}</p><div class="fgrid">` + shown.map((f) => {
        const sel = S.me.is_admin && f.importable && !f.in_library;
        return `<div class="tile cat" data-id="${esc(f.id)}">
          ${sel ? `<input type="checkbox" class="tsel" data-sel="${esc(f.id)}" ${CAT.selected.has(f.id) ? 'checked' : ''} aria-label="Select ${esc(f.family)}">` : ''}
          ${specimenFor(sample(f), null, f, 'specimen cspec')}
          <div class="tile-foot"><a class="tname" href="${esc(f.page)}" target="_blank" rel="noopener noreferrer" title="${esc(CAT_SOURCES[f.source] || f.source)} ↗">${esc(f.family)}</a>
            <span class="meta">${f.variable ? 'Variable' : f.styles ? plural(f.styles, 'style') : ''}</span><span class="spacer"></span>
            ${f.in_library ? '<span class="badge open">in library</span>'
              : S.me.is_admin && f.importable ? `<button class="btn sm" data-import="${esc(f.id)}">Add</button>`
              : !S.me.is_admin && f.importable ? `<button class="btn sm" data-ask="${esc(f.family)}">Request</button>`
              : `<span class="badge" title="${esc(f.reason)}">web only</span>`}</div>
          <div class="tile-sub small muted">${esc(CAT_SOURCES[f.source] || f.source)} · ${esc(f.license)}</div>
        </div>`;
      }).join('') + '</div>' + (list.length > CAT.shown ? '<div class="more"><button class="btn" id="cmore">Show more</button></div>' : '');
    } else el.innerHTML = `<p class="small muted">${plural(list.length, 'family', 'families')}</p>` + shown.map((f) => {
      const sel = S.me.is_admin && f.importable && !f.in_library;
      return `<div class="cfam">
        <div class="family-head">
          ${sel ? `<input type="checkbox" data-sel="${esc(f.id)}" ${CAT.selected.has(f.id) ? 'checked' : ''} aria-label="Select ${esc(f.family)}">` : '<span class="cbspace"></span>'}
          <span class="name">${esc(f.family)}</span>
          <span class="meta">${esc(CAT_SOURCES[f.source] || f.source)}${f.foundry ? ` · ${esc(f.foundry)}` : ''} · ${esc(CAT_CATEGORIES[f.category] || f.category)}${f.styles ? ` · ${plural(f.styles, 'style')}` : ''}${f.variable ? ' · variable' : ''}</span>
          <span class="badge">${esc(f.license)}</span>
          ${f.in_library ? '<span class="badge open">in library</span>' : ''}
          <span class="spacer"></span>
          <a class="btn sm ghost" href="${esc(f.page)}" target="_blank" rel="noopener noreferrer">${esc(CAT_SOURCES[f.source] || 'Source')} ↗</a>
          ${S.me.is_admin && f.importable && !f.in_library ? `<button class="btn sm" data-import="${esc(f.id)}">Add to library</button>` : ''}
          ${!S.me.is_admin && f.importable && !f.in_library ? `<button class="btn sm" data-ask="${esc(f.family)}">Request</button>` : ''}
        </div>
        ${specimenFor(sample(f), null, f, 'specimen cspec')}
        ${f.reason ? `<p class="small muted cnote">${esc(f.reason)}</p>` : ''}
      </div>`;
    }).join('') + (list.length > CAT.shown ? '<div class="more"><button class="btn" id="cmore">Show more</button></div>' : '');
    observeSpecimens(el);
    $$('[data-sel]', el).forEach((c) => c.onchange = () => { c.checked ? CAT.selected.add(c.dataset.sel) : CAT.selected.delete(c.dataset.sel); drawBulk(); });
    $$('[data-import]', el).forEach((b) => b.onclick = () => importCatalog([b.dataset.import], list, load));
    $$('[data-ask]', el).forEach((b) => b.onclick = () => requestFont(`${b.dataset.ask} (free, ${location.origin}/#/find)`));
    const more = $('#cmore', el);
    if (more) more.onclick = () => { CAT.shown += 60; draw(); };
    drawBulk();
  };
  const drawBulk = () => {
    const b = $('#cbulk', box);
    if (!CAT.selected.size) { b.innerHTML = ''; return; }
    b.innerHTML = `<div class="bulk"><b>${plural(CAT.selected.size, 'family', 'families')} selected</b>
      <button class="btn sm primary" id="cimp">Add to library</button><button class="btn sm ghost" id="cclear">Clear</button></div>`;
    $('#cimp', b).onclick = () => importCatalog([...CAT.selected], list, () => { CAT.selected.clear(); return load(); });
    $('#cclear', b).onclick = () => { CAT.selected.clear(); draw(); };
  };
  let t;
  $('#cq', box).oninput = (e) => { clearTimeout(t); t = setTimeout(() => { CAT.q = e.target.value.trim(); CAT.shown = 40; load(); }, 250); };
  escClears($('#cq', box), () => { CAT.q = ''; load(); });
  $('#ccat', box).onchange = (e) => { CAT.category = e.target.value; CAT.shown = 40; load(); };
  $('#csrc', box).onchange = (e) => { CAT.source = e.target.value; CAT.shown = 40; load(); };
  // keep the specimen text in step with its family when typing
  $('#ctext', box).oninput = (e) => {
    CAT.text = e.target.value;
    $$('.cfam, .tile', box).forEach((row, i) => {
      const s = $('.cspec', row);
      if (s && !s.classList.contains('catimg')) s.textContent = CAT.text || (S.view === 'grid' ? PANGRAM : list[i]?.family || '');
    });
  };
  wireViewToggle(box, () => draw());
  await load();
}

// importCatalog adds catalog families to the library after asking for a
// collection, and reports what came of each.
function importCatalog(ids, list, done) {
  const names = ids.map((id) => list.find((f) => f.id === id)?.family || id);
  const cols = S.lib.collections || [];
  dialog(ids.length === 1 ? `Add ${names[0]}` : `Add ${plural(ids.length, 'family', 'families')}`, `
    <p>${ids.length === 1 ? `<b>${esc(names[0])}</b> is` : `${names.map((n) => `<b>${esc(n)}</b>`).join(', ')} are`} downloaded complete from ${ids.some((i) => i.startsWith('google:')) ? 'Google Fonts' : ''}${ids.some((i) => i.startsWith('google:')) && ids.some((i) => i.startsWith('fontsquirrel:')) ? ' and ' : ''}${ids.some((i) => i.startsWith('fontsquirrel:')) ? 'Font Squirrel' : ''} and added like an upload.</p>
    <p class="small muted">Open licenses (SIL OFL, Apache, Ubuntu) are assigned automatically. A Font Squirrel freeware license becomes a license with its text attached – check what it allows and tick the rights.</p>
    ${cols.length ? `<label class="field">Also add to collection<select name="collection"><option value="">No collection</option>${cols.map((c) => `<option value="${c.id}">${esc(c.name)}</option>`).join('')}</select></label>` : ''}`,
  { submit: 'Add to library', onSubmit: async (fd) => {
    toast(`Downloading ${plural(ids.length, 'family', 'families')}…`);
    const res = await api('POST', '/api/catalog/import', { ids, collection_id: +(fd.get('collection') || 0) });
    S.lib.loaded = false;
    const ok = res.filter((r) => r.status === 'added');
    const bad = res.filter((r) => r.status === 'error');
    if (bad.length) {
      dialog('Not everything was added', `<ul>${res.map((r) => `<li><b>${esc(r.family || r.id)}</b>: ${r.status === 'added' ? `added ${plural(r.added, 'style')}` : r.status === 'exists' ? 'already in the library' : esc(r.message)}</li>`).join('')}</ul>`,
        { submit: 'OK', cancel: false, onSubmit: async () => {} });
    } else if (ok.length) {
      toast(ok.map((r) => `${r.family}: ${plural(r.added, 'style')}${r.licenses.length ? ` · ${r.licenses.join(', ')}` : ''}`).join(' · '));
    } else toast('Already in the library');
    await done();
  } });
}

let identifyImage = null; // { blob, url } of the image to identify
async function renderIdentify(box) {
  const info = await api('GET', '/api/identify');
  const ready = info.enabled && info.services.length > 0;
  const names = { whatfontis: 'WhatFontIs', mixfont: 'Mixfont Lens', aspose: 'Aspose' };
  box.innerHTML = `
    <div class="idgrid">
      <div>
        ${!info.enabled ? '<div class="alert">Identification by image is switched off on this server (TYPEHAUS_IDENTIFY=false). The sites below still work.</div>'
          : !ready ? `<div class="alert warnbox">No identification service is set up yet. ${S.me.is_admin ? 'Add an API key under <a href="#/integrations?tab=services">Integrations → Services</a> – WhatFontIs is free for 200 lookups a day.' : 'Ask an administrator to add one. Meanwhile, use one of the sites below and look the name up here.'}</div>` : ''}
        ${info.enabled ? `<div class="drop iddrop ${ready ? '' : 'off'}" id="iddrop" tabindex="0">
          <div id="idpreview">${identifyImage ? `<img src="${identifyImage.url}" alt="Image to identify">` : '<b>Drop, paste or choose an image</b><span class="small muted">A screenshot or photo of the lettering – one line of text works best.</span>'}</div>
          <input type="file" id="idfile" accept="image/png,image/jpeg,image/gif,image/webp,image/bmp" hidden>
        </div>
        <div class="row" style="margin-top:10px">
          <input type="url" id="idurl" placeholder="…or paste a link to an image" style="flex:1">
          <label class="row small"><input type="checkbox" id="idfree"> Free fonts only</label>
          <button class="btn primary" id="idgo" ${ready ? '' : 'disabled'}>Identify</button>
        </div>
        <p class="small muted">${ready ? `Asks ${info.services.map((s) => names[s] || s).join(', ')}. The image is sent to ${info.services.length === 1 ? 'that service' : 'those services'}; typehaus doesn't keep it.` : ''}</p>` : ''}
        <div id="idres"></div>
      </div>
      <aside class="card idside">
        <h3>Other identifiers</h3>
        <p class="small muted">These have no API: open one, upload the image there, then look the name up here.</p>
        <ul class="idlinks">${info.links.map((l) => `<li><a href="${esc(l.url)}" target="_blank" rel="noopener noreferrer">${esc(l.name)} ↗</a><span class="small muted">${esc(l.about)}</span></li>`).join('')}</ul>
        <form id="lkform" class="row"><input type="search" id="lkname" placeholder="Font name, e.g. Futura PT Book" required style="flex:1"><button class="btn">Look up</button></form>
        <div id="lkres"></div>
      </aside>
    </div>`;
  $('#lkform', box).onsubmit = async (e) => {
    e.preventDefault();
    const name = $('#lkname', box).value.trim();
    if (!name) return;
    try {
      const r = await api('GET', `/api/lookup?name=${encodeURIComponent(name)}`);
      const out = $('#lkres', box);
      out.innerHTML = `${r.library.length ? `<h4>In your library</h4>${r.library.map((m) => libMatchHTML(m)).join('')}` : `<p class="small"><b>${esc(name)}</b> isn't in your library.</p>`}
        ${(() => {
          const have = new Set(r.library.map((m) => m.family.toLowerCase()));
          const cat = r.catalog.filter((c) => !have.has(c.family.toLowerCase()));
          return cat.length ? `<h4>Free catalog</h4>${cat.map((c) => catMatchHTML(c)).join('')}` : '';
        })()}
        ${!r.library.length ? `<p><button type="button" class="btn sm" data-ask="${esc(name)}">${S.me.is_admin ? 'Note it as a request' : 'Request it'}</button></p>` : ''}`;
      wireMatches(out);
      observeSpecimens(out);
    } catch (err) { fail(err); }
  };
  if (!info.enabled) return;
  const setImage = (file) => {
    if (!file || !/^image\//.test(file.type)) { toast('That is not an image', true); return; }
    if (file.size > 10 << 20) { toast('The image is larger than 10 MB', true); return; }
    if (identifyImage) URL.revokeObjectURL(identifyImage.url);
    identifyImage = { blob: file, url: URL.createObjectURL(file) };
    $('#idpreview', box).innerHTML = `<img src="${identifyImage.url}" alt="Image to identify">`;
    $('#idurl', box).value = '';
  };
  const drop = $('#iddrop', box);
  drop.onclick = () => $('#idfile', box).click();
  drop.onkeydown = (e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); $('#idfile', box).click(); } };
  $('#idfile', box).onchange = (e) => setImage(e.target.files[0]);
  drop.ondragover = (e) => { e.preventDefault(); drop.classList.add('over'); };
  drop.ondragleave = () => drop.classList.remove('over');
  drop.ondrop = (e) => { e.preventDefault(); drop.classList.remove('over'); setImage(e.dataTransfer.files[0]); };
  // paste anywhere on the page while it is open
  const onPaste = (e) => {
    if (!document.body.contains(drop)) { document.removeEventListener('paste', onPaste); return; }
    const f = [...(e.clipboardData?.files || [])].find((x) => x.type.startsWith('image/'));
    if (f) { e.preventDefault(); setImage(f); }
  };
  document.addEventListener('paste', onPaste);
  $('#idgo', box).onclick = async () => {
    const link = $('#idurl', box).value.trim();
    if (!identifyImage && !link) { toast('Choose an image first', true); return; }
    const fd = new FormData();
    if (link) fd.append('url', link); else fd.append('image', identifyImage.blob, identifyImage.blob.name || 'image.png');
    if ($('#idfree', box).checked) fd.append('free_only', '1');
    const btn = $('#idgo', box);
    btn.disabled = true; btn.textContent = 'Identifying…';
    $('#idres', box).innerHTML = '<div class="more">Asking the identification services – this can take up to a minute.</div>';
    try {
      const r = await api('POST', '/api/identify', fd);
      drawIdentify($('#idres', box), r.results);
    } catch (e) { $('#idres', box).innerHTML = ''; fail(e); } finally { btn.disabled = false; btn.textContent = 'Identify'; }
  };
}

function libMatchHTML(m) {
  return `<div class="idm"><div class="idm-head"><b>${esc(m.family)}</b> <span class="badge open">in your library</span>
    <span class="small muted">${plural(m.styles, 'style')}${m.license ? ` · ${esc(m.license)}` : ' · no license assigned'}</span>
    <span class="spacer"></span><a class="btn sm ghost" href="#/library?q=${encodeURIComponent(m.family)}">Open</a></div>
    ${specimenFor(m.family, m, null, 'specimen idspec')}</div>`;
}
function catMatchHTML(c) {
  return `<div class="idm"><div class="idm-head"><b>${esc(c.family)}</b> <span class="badge">${esc(c.license)}</span>
    <span class="small muted">${esc(CAT_SOURCES[c.source] || c.source)}</span>${c.in_library ? ' <span class="badge open">in library</span>' : ''}
    <span class="spacer"></span><a class="btn sm ghost" href="${esc(c.page)}" target="_blank" rel="noopener noreferrer">↗</a>
    ${c.importable && !c.in_library ? (S.me.is_admin ? `<button type="button" class="btn sm" data-import="${esc(c.id)}" data-fam="${esc(c.family)}">Add to library</button>`
      : `<button type="button" class="btn sm" data-ask="${esc(c.family)}">Request</button>`) : ''}</div>
    ${specimenFor(c.family, null, c, 'specimen idspec')}</div>`;
}
function wireMatches(el, after = () => {}) {
  $$('[data-import]', el).forEach((b) => b.onclick = () => importCatalog([b.dataset.import], [{ id: b.dataset.import, family: b.dataset.fam }], async () => { b.replaceWith(Object.assign(document.createElement('span'), { className: 'badge open', textContent: 'in library' })); after(); }));
  $$('[data-ask]', el).forEach((b) => b.onclick = () => requestFont(b.dataset.ask));
}

function drawIdentify(el, results) {
  el.innerHTML = results.map((r) => {
    const head = `<div class="row"><h3 style="margin:0">${esc(r.name)}</h3>
      ${r.status === 'ok' ? `<span class="small muted">${plural(r.matches.length, 'match', 'matches')}${r.word ? ` · read “${esc(r.word)}”` : ''}${r.style ? ` · ${esc(r.style)}` : ''}</span>`
        : r.status === 'skipped' ? '<span class="badge">skipped</span>' : '<span class="badge warn">failed</span>'}</div>`;
    if (r.status !== 'ok') return `<div class="card idcard">${head}<p class="small muted">${esc(r.message)}</p></div>`;
    if (!r.matches.length) return `<div class="card idcard">${head}<p class="small muted">No match. Crop the image to a line of clear text and try again.</p></div>`;
    return `<div class="card idcard">${head}${r.matches.map((m) => {
      const lib = m.library; const cat = m.catalog;
      let verdict;
      if (lib) verdict = `<span class="badge open">in your library</span> <span class="small muted">${esc(lib.family)}${lib.license ? ` · ${esc(lib.license)}` : ''}</span>`;
      else if (cat && cat.importable) verdict = `<span class="badge">free · ${esc(cat.license)}</span> <span class="small muted">${esc(CAT_SOURCES[cat.source])}</span>`;
      else if (m.free === false) verdict = '<span class="badge warn">commercial</span>';
      else if (m.free) verdict = '<span class="badge">free</span>';
      else verdict = '';
      const act = lib ? `<a class="btn sm ghost" href="#/library?q=${encodeURIComponent(lib.family)}">Open</a>`
        : cat && cat.importable && !cat.in_library ? (S.me.is_admin ? `<button type="button" class="btn sm" data-import="${esc(cat.id)}" data-fam="${esc(cat.family)}">Add to library</button>` : `<button type="button" class="btn sm" data-ask="${esc(cat.family)}">Request</button>`)
          : `<button type="button" class="btn sm" data-ask="${esc(m.name)}">${S.me.is_admin ? 'Note as request' : 'Request'}</button>`;
      return `<div class="idm"><div class="idm-head"><b>${esc(m.name)}</b>${m.score ? ` <span class="small muted">${Math.round(m.score * 100)} %</span>` : ''} ${verdict}
        <span class="spacer"></span>${m.url ? `<a class="btn sm ghost" href="${esc(m.url)}" target="_blank" rel="noopener noreferrer">${esc(r.name)} ↗</a>` : ''}${act}</div>
        ${lib || cat ? specimenFor(m.name, lib, cat, 'specimen idspec') : ''}</div>`;
    }).join('')}</div>`;
  }).join('');
  wireMatches(el);
  observeSpecimens(el);
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

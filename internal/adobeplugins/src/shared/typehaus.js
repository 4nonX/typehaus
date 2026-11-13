/*
 * typehaus panel – shared by the InDesign, Photoshop and Illustrator plugins.
 *
 * The panel asks the typehaus app on this computer about the fonts of the
 * open document (the app answers for the person signed in there, with the
 * library's license rules), switches available fonts on and requests the
 * others. Each app supplies a small host adapter:
 *
 *   host.app          "InDesign" | "Photoshop" | "Illustrator"
 *   host.document()   Promise<null | {name, fonts: [{postscript, family, style, missing}]}>
 *                     missing: true (the app says it's missing), false (the app
 *                     has it), null (the app can't tell – the typehaus app decides)
 *   host.onChange(cb) call cb when another document becomes active
 *
 * Plain ES2017 (no modules, no optional chaining) so the same file runs in
 * UXP and in the Chromium of CEP panels.
 */
(function (root) {
  'use strict';

  var DEFAULT_BASE = 'http://127.0.0.1:47130';

  function ApiError(status, body) {
    this.name = 'ApiError';
    this.status = status;
    this.code = (body && body.code) || '';
    this.message = (body && body.error) || ('HTTP ' + status);
  }
  ApiError.prototype = Object.create(Error.prototype);

  function AppMissing() {
    this.name = 'AppMissing';
    this.message = "The typehaus app isn't running on this computer.";
  }
  AppMissing.prototype = Object.create(Error.prototype);

  function newId() {
    var s = '';
    for (var i = 0; i < 20; i++) s += 'abcdefghijklmnopqrstuvwxyz0123456789'.charAt(Math.floor(Math.random() * 36));
    return s;
  }

  var sleep = function (ms) { return new Promise(function (r) { setTimeout(r, ms); }); };

  // createClient talks to the app: over HTTP on 127.0.0.1, or – where the
  // host only allows https (Photoshop on macOS) – through request and answer
  // files in the typehaus folder (opts.bridge: {write, read, remove}).
  function createClient(opts) {
    opts = opts || {};
    var base = opts.base || DEFAULT_BASE;
    var doFetch = opts.fetch || (typeof fetch !== 'undefined' ? fetch.bind(root) : null);
    var bridge = opts.bridge || null;
    var wait = opts.sleep || sleep;
    var mode = null; // http | file, once known

    function viaHTTP(method, path, body) {
      if (!doFetch) return Promise.reject(new TypeError('no fetch'));
      return doFetch(base + path, {
        method: method,
        headers: { 'X-Typehaus-Plugin': '1', 'Content-Type': 'application/json' },
        body: body ? JSON.stringify(body) : undefined,
      }).then(function (r) {
        return r.text().then(function (t) {
          var data = {};
          try { data = t ? JSON.parse(t) : {}; } catch (e) { data = { error: t }; }
          return { status: r.status, body: data };
        });
      });
    }

    async function viaFile(method, path, body, timeout) {
      var id = newId();
      await bridge.write('requests/' + id + '.json', JSON.stringify({ method: method, path: path, body: body || null }));
      var deadline = Date.now() + timeout;
      while (Date.now() < deadline) {
        await wait(150);
        var t = null;
        try { t = await bridge.read('answers/' + id + '.json'); } catch (e) { t = null; }
        if (t) {
          try { await bridge.remove('answers/' + id + '.json'); } catch (e) { /* the app cleans up */ }
          var a = JSON.parse(t);
          return { status: a.status, body: a.body || {} };
        }
      }
      try { await bridge.remove('requests/' + id + '.json'); } catch (e) { /* taken meanwhile */ }
      throw new AppMissing();
    }

    function result(r) {
      if (r.status >= 200 && r.status < 300) return r.body;
      throw new ApiError(r.status, r.body);
    }

    // call: timeout only matters for the file bridge (HTTP fails at once
    // when nothing listens).
    async function call(method, path, body, timeout) {
      timeout = timeout || 60000;
      if (mode !== 'file') {
        try {
          var r = await viaHTTP(method, path, body);
          mode = 'http';
          return result(r);
        } catch (e) {
          if (e instanceof ApiError) throw e;
          if (!bridge || mode === 'http') throw new AppMissing();
        }
      }
      var f = await viaFile(method, path, body, mode === 'file' ? timeout : Math.min(timeout, 6000));
      mode = 'file';
      return result(f);
    }

    return {
      call: call,
      mode: function () { return mode; },
      status: function () { return call('GET', '/v1/status', null, 6000); },
      check: function (fonts, app) { return call('POST', '/v1/fonts/check', { fonts: fonts, app: app }); },
      activate: function (fonts, app) { return call('POST', '/v1/fonts/activate', { fonts: fonts, app: app }, 90000); },
      request: function (font, purpose, app) { return call('POST', '/v1/requests', { font: font, purpose: purpose, app: app }); },
    };
  }

  var ref = function (f) { return { postscript_name: f.postscript || '', family: f.family || '', style: f.style || '' }; };
  var key = function (f) { return (f.postscript || (f.family + ' ' + f.style)).toLowerCase(); };

  // load gathers what the panel shows: the document's fonts and, for every
  // font the app is missing (or can't judge), what typehaus can do.
  async function load(host, client) {
    var doc = await host.document();
    if (!doc) return { state: 'nodoc' };
    var st;
    try {
      st = await client.status();
    } catch (e) {
      if (e instanceof AppMissing) return { state: 'noapp', doc: doc };
      throw e;
    }
    if (!st.connected) return { state: 'notconnected', doc: doc };
    var seen = {};
    var ask = [];
    doc.fonts.forEach(function (f) {
      var k = key(f);
      if (!k.trim() || seen[k] || f.missing === false) return;
      seen[k] = true;
      ask.push(f);
    });
    var rows = [];
    if (ask.length) {
      var res = await client.check(ask.map(ref), host.app);
      ask.forEach(function (f, i) {
        var s = res.fonts[i] || {};
        // the app knows: missing; it can't tell: missing unless installed here
        var missing = f.missing === true || (f.missing == null && !s.on_system && !s.installed);
        if (!missing) return;
        rows.push({ font: f, status: s.status || 'not_available', reason: s.reason || '', installed: !!s.installed,
          library: s.library_family ? (s.library_family + ' ' + (s.library_style || '')).trim() : '' });
      });
    }
    rows.sort(function (a, b) { return (a.font.family + a.font.style).localeCompare(b.font.family + b.font.style); });
    return { state: 'ok', doc: doc, rows: rows, total: Object.keys(seen).length + doc.fonts.filter(function (f) { return f.missing === false; }).length, server: st.server, user: st.user };
  }

  async function activate(model, host, client, only) {
    var fonts = model.rows.filter(function (r) { return r.status === 'available' && (!only || only === key(r.font)); }).map(function (r) { return ref(r.font); });
    if (!fonts.length) return { activated: 0, complete: true };
    var res = await client.activate(fonts, host.app);
    var n = (res.fonts || []).filter(function (f) { return f.status === 'active'; }).length;
    return { activated: n, complete: !!res.complete };
  }

  function request(row, model, host, client) {
    var name = row.font.postscript || (row.font.family + ' ' + row.font.style);
    return client.request(name, 'Used in the ' + host.app + ' document “' + model.doc.name + '”', host.app);
  }

  // ---- the panel ------------------------------------------------------------

  var ESC = { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' };
  var esc = function (s) { return String(s == null ? '' : s).replace(/[&<>"']/g, function (c) { return ESC[c]; }); };
  var plural = function (n, one, many) { return n + ' ' + (n === 1 ? one : (many || one + 's')); };

  var LABEL = {
    active: 'switched on – installing',
    available: 'in typehaus',
    refused: 'not available to you',
    unsupported: "can't be installed here",
    not_available: 'not in typehaus',
  };

  function render(el, model, ui) {
    var h = '';
    if (model.state === 'loading') h = '<p class="th-muted">Checking fonts…</p>';
    else if (model.state === 'nodoc') h = '<p class="th-muted">Open a document to check its fonts.</p>';
    else if (model.state === 'noapp') h = '<div class="th-box"><b>typehaus isn\'t running.</b><p>Start the typehaus app on this computer – it switches fonts on for you.</p></div>';
    else if (model.state === 'notconnected') h = '<div class="th-box"><b>typehaus isn\'t connected.</b><p>Open the typehaus app and connect this computer to your font library.</p></div>';
    else if (model.state === 'error') h = '<div class="th-box th-warn"><b>Something went wrong.</b><p>' + esc(model.error) + '</p></div>';
    else {
      var rows = model.rows;
      var avail = rows.filter(function (r) { return r.status === 'available'; }).length;
      h += '<div class="th-doc"><b>' + esc(model.doc.name) + '</b><span class="th-muted">' +
        (rows.length ? plural(rows.length, 'font') + ' missing' : 'all fonts available') + '</span></div>';
      if (!rows.length) h += '<p class="th-ok">Every font this document uses is on this computer.</p>';
      if (avail) h += '<button class="th-btn th-primary" data-on="*">Switch on ' + (avail === 1 ? 'the font' : 'all ' + avail + ' fonts') + '</button>';
      rows.forEach(function (r) {
        var k = key(r.font);
        var name = (r.font.family || r.font.postscript) + (r.font.style ? ' ' + r.font.style : '');
        var act = '';
        if (r.status === 'available') act = '<button class="th-btn" data-on="' + esc(k) + '">Switch on</button>';
        else if ((r.status === 'refused' || r.status === 'not_available') && !ui.requested[k]) act = '<button class="th-btn" data-req="' + esc(k) + '">Request</button>';
        else if (ui.requested[k]) act = '<span class="th-muted">Requested</span>';
        var note = r.status === 'active' && r.installed ? 'installed – reopen the document if it still shows as missing' : (LABEL[r.status] || r.status);
        h += '<div class="th-font"><div class="th-grow"><b>' + esc(name) + '</b>' +
          (r.font.postscript ? '<span class="th-ps">' + esc(r.font.postscript) + '</span>' : '') +
          '<span class="th-tag th-' + esc(r.status) + '">' + esc(note) + '</span>' +
          (r.reason ? '<span class="th-muted">' + esc(r.reason) + '</span>' : '') + '</div>' + act + '</div>';
      });
    }
    h += '<div class="th-foot">' + (ui.message ? '<span class="th-msg">' + esc(ui.message) + '</span>' : '<span></span>') +
      '<button class="th-link" data-refresh>Check again</button></div>';
    el.innerHTML = h;
  }

  // mount runs the panel in an element.
  function mount(el, host, client, opts) {
    opts = opts || {};
    var model = { state: 'loading' };
    var ui = { requested: {}, message: '', busy: false };
    var timer = null;

    function draw() { render(el, model, ui); }

    async function refresh() {
      try {
        model = await load(host, client);
      } catch (e) {
        model = { state: 'error', error: e.message || String(e) };
      }
      draw();
      return model;
    }

    el.addEventListener('click', async function (ev) {
      var t = ev.target;
      while (t && t !== el && !(t.getAttribute && (t.getAttribute('data-on') || t.getAttribute('data-req') || t.hasAttribute('data-refresh')))) t = t.parentNode;
      if (!t || t === el || ui.busy) return;
      if (t.hasAttribute('data-refresh')) { ui.message = ''; refresh(); return; }
      ui.busy = true;
      try {
        var on = t.getAttribute('data-on');
        var req = t.getAttribute('data-req');
        if (on) {
          ui.message = 'Switching on and installing…';
          draw();
          var r = await activate(model, host, client, on === '*' ? null : on);
          ui.message = r.complete ? plural(r.activated, 'font') + ' switched on and installed.' : 'Switched on – still installing, check again in a moment.';
        } else if (req) {
          var row = model.rows.filter(function (x) { return key(x.font) === req; })[0];
          await request(row, model, host, client);
          ui.requested[req] = true;
          ui.message = 'Requested – your administrators answer in typehaus.';
        }
      } catch (e) {
        ui.message = e.message || String(e);
      }
      ui.busy = false;
      await refresh();
    });

    host.onChange(function () { refresh(); });
    if (opts.interval !== 0) timer = setInterval(function () { if (!ui.busy) refresh(); }, opts.interval || 15000);
    draw();
    var first = refresh();
    return { refresh: refresh, stop: function () { if (timer) clearInterval(timer); }, ready: first, model: function () { return model; } };
  }

  var api = { createClient: createClient, load: load, activate: activate, request: request, mount: mount, render: render,
    ApiError: ApiError, AppMissing: AppMissing, DEFAULT_BASE: DEFAULT_BASE };
  if (typeof module !== 'undefined' && module.exports) module.exports = api;
  root.TypehausPanel = api;
})(typeof globalThis !== 'undefined' ? globalThis : (typeof window !== 'undefined' ? window : this));

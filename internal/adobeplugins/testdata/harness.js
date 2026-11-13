/*
 * Runs the typehaus panels in Node against stand-ins for the Adobe apps'
 * scripting objects, talking to the real typehaus app (plugin API) that the
 * Go test started. Prints one JSON line with what happened.
 *
 *   node harness.js <scenario> <srcdir>
 *   env PLUGIN_BASE  the app's plugin API (http://127.0.0.1:port)
 *       BRIDGE_DIR   the app's plugins folder (file bridge scenario)
 */
'use strict';
const Module = require('module');
const path = require('path');
const fs = require('fs');
const vm = require('vm');

const scenario = process.argv[2];
const srcdir = process.argv[3];
const base = process.env.PLUGIN_BASE;

// require() of the host modules ('indesign', 'photoshop', 'uxp', …) gets the mocks
const mocks = {};
const realLoad = Module._load;
Module._load = function (request, parent, isMain) {
  if (Object.prototype.hasOwnProperty.call(mocks, request)) return mocks[request];
  return realLoad.apply(this, arguments);
};

const panel = require(path.join(srcdir, 'shared', 'typehaus.js'));
const out = { scenario };

function fakeElement() {
  return { innerHTML: '', listeners: {}, addEventListener(t, f) { this.listeners[t] = f; } };
}

async function inDesign() {
  // InDesign's FontStatus values (from Adobe's UXP DOM reference)
  const FontStatus = { INSTALLED: 1718831470, NOT_AVAILABLE: 1718832705, FAUXED: 1718830689, SUBSTITUTED: 1718834037, UNKNOWN: 1433299822 };
  const font = (ps, family, style, status) => ({ postscriptName: ps, fontFamily: family, fontStyleName: style, status });
  const listeners = {};
  const doc = {
    name: 'Brochure.indd',
    fonts: { everyItem: () => ({ getElements: () => [
      font('Poppins-Regular', 'Poppins', 'Regular', FontStatus.NOT_AVAILABLE),
      font('ArialMT', 'Arial', 'Regular', FontStatus.INSTALLED),
      font('Brand-Bold', 'Brand', 'Bold', FontStatus.NOT_AVAILABLE),
      font('Futura-Bold', 'Futura', 'Bold', FontStatus.SUBSTITUTED),
      // a missing font throws for some properties
      Object.defineProperty({ postscriptName: 'Ghost-Regular', status: FontStatus.NOT_AVAILABLE, fontFamily: 'Ghost' }, 'fontStyleName', { get() { throw new Error('The requested scripting property is not available for the font.'); } }),
    ] }) },
  };
  mocks.indesign = { app: { documents: { length: 1 }, activeDocument: doc, addEventListener: (ev, f) => { listeners[ev] = f; } }, FontStatus };
  const host = require(path.join(srcdir, 'indesign', 'host.js'));
  host.onChange(() => {});
  out.events = Object.keys(listeners).sort();
  const client = panel.createClient({ base });
  let model = await panel.load(host, client);
  out.before = model.rows.map((r) => [r.font.postscript, r.status, r.reason]);
  out.ghostStyle = (model.rows.find((r) => r.font.postscript === 'Ghost-Regular') || {}).font;
  // the panel as rendered
  const el = fakeElement();
  panel.render(el, model, { requested: {}, message: '' });
  out.html = el.innerHTML;
  out.activate = await panel.activate(model, host, client);
  // InDesign now finds the font
  doc.fonts.everyItem().getElements()[0].status = FontStatus.INSTALLED;
  await panel.request(model.rows.find((r) => r.font.postscript === 'Brand-Bold'), model, host, client);
  out.mode = client.mode();
}

async function photoshopViaFiles() {
  const textLayer = (id) => ({ id, kind: 'text', layers: [] });
  const doc = { id: 7, title: 'Poster.psd', layers: [{ id: 1, kind: 'pixel' }, { id: 2, kind: 'group', layers: [textLayer(3), textLayer(4)] }] };
  const styles = {
    3: [{ textStyle: { fontPostScriptName: 'Poppins-Regular', fontName: 'Poppins', fontStyleName: 'Regular' } },
        { textStyle: { fontPostScriptName: 'ArialMT', fontName: 'Arial', fontStyleName: 'Regular' } }],
    4: [{ textStyle: { fontPostScriptName: 'Poppins-Regular', fontName: 'Poppins', fontStyleName: 'Regular' } }],
  };
  const batches = [];
  mocks.photoshop = {
    app: { activeDocument: doc, fonts: [{ postScriptName: 'ArialMT' }] },
    action: {
      batchPlay: async (descs) => { batches.push(descs.length); return descs.map((d) => ({ textKey: { textStyleRange: styles[d._target[0]._id] } })); },
      addNotificationListener: () => {},
    },
  };
  const host = require(path.join(srcdir, 'photoshop', 'host.js'));
  // Photoshop on macOS: http to 127.0.0.1 is refused – only the file bridge works
  const dir = process.env.BRIDGE_DIR;
  const bridge = {
    write: (rel, text) => fs.promises.writeFile(path.join(dir, rel), text),
    read: async (rel) => { try { return await fs.promises.readFile(path.join(dir, rel), 'utf8'); } catch (e) { return null; } },
    remove: (rel) => fs.promises.unlink(path.join(dir, rel)),
  };
  const blocked = () => Promise.reject(new TypeError('Network request failed'));
  const client = panel.createClient({ base, fetch: blocked, bridge });
  const model = await panel.load(host, client);
  out.batches = batches;
  out.before = model.rows.map((r) => [r.font.postscript, r.status]);
  out.activate = await panel.activate(model, host, client);
  out.mode = client.mode();
  out.leftover = fs.readdirSync(path.join(dir, 'requests')).concat(fs.readdirSync(path.join(dir, 'answers')));
}

async function illustrator() {
  // run host.jsx (ExtendScript, ES3) against a stand-in of Illustrator's DOM
  const range = (name, family, style) => ({ characterAttributes: { textFont: { name, family, style } } });
  const frames = [
    { textRanges: [range('Poppins-Regular', 'Poppins', 'Regular'), range('Poppins-Regular', 'Poppins', 'Regular')] },
    { textRanges: [range('Futura-Bold', 'Futura', 'Bold'), range('Quote"Font\\Name', 'Odd', 'Regular')] },
  ];
  const sandbox = { app: { documents: { length: 1 }, activeDocument: { name: 'Logo.ai', textFrames: frames } } };
  vm.createContext(sandbox);
  vm.runInContext(fs.readFileSync(path.join(srcdir, 'illustrator', 'host.jsx'), 'utf8'), sandbox);
  global.window = global;
  // CEP's Node.js runtime, as --enable-nodejs provides it
  let nodeCalls = 0;
  global.cep_node = { require: (m) => { if (m === 'http') nodeCalls++; return require(m); } };
  global.fetch = () => Promise.reject(new TypeError('blocked by CORS'));
  global.__adobe_cep__ = { evalScript: (script, cb) => setTimeout(() => cb(vm.runInContext(script, sandbox)), 0), addEventListener: () => {} };
  require(path.join(srcdir, 'illustrator', 'host.js'));
  const host = global.TypehausIllustrator;
  const client = panel.createClient({ base, fetch: host.fetch });
  let model = await panel.load(host, client);
  out.before = model.rows.map((r) => [r.font.postscript, r.status]);
  out.activate = await panel.activate(model, host, client);
  model = await panel.load(host, client); // Poppins is on the computer now
  out.after = model.rows.map((r) => [r.font.postscript, r.status]);
  sandbox.app.documents.length = 0;
  out.nodoc = (await panel.load(host, client)).state;
  out.nodeCalls = nodeCalls;
}

async function noApp() {
  const host = { app: 'InDesign', document: async () => ({ name: 'A.indd', fonts: [{ postscript: 'X', missing: true }] }), onChange() {} };
  out.http = (await panel.load(host, panel.createClient({ base: 'http://127.0.0.1:9' }))).state;
  const dir = fs.mkdtempSync(path.join(require('os').tmpdir(), 'th-'));
  fs.mkdirSync(path.join(dir, 'requests')); fs.mkdirSync(path.join(dir, 'answers'));
  const bridge = { write: (rel, t) => fs.promises.writeFile(path.join(dir, rel), t), read: async () => null, remove: (rel) => fs.promises.unlink(path.join(dir, rel)).catch(() => {}) };
  const t0 = Date.now();
  out.file = (await panel.load(host, panel.createClient({ base: 'http://127.0.0.1:9', bridge, sleep: (ms) => new Promise((r) => setTimeout(r, Math.min(ms, 20))) }))).state;
  out.fileSeconds = Math.round((Date.now() - t0) / 1000);
  out.fileLeftover = fs.readdirSync(path.join(dir, 'requests'));
}

const run = { indesign: inDesign, photoshop: photoshopViaFiles, illustrator, noapp: noApp }[scenario];
run().then(() => { console.log(JSON.stringify(out)); process.exit(0); },
  (e) => { console.log(JSON.stringify({ scenario, error: String(e && e.stack || e) })); process.exit(1); });

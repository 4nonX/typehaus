/* Photoshop: fonts come from the text layers (every style run). */
const photoshop = require('photoshop');
const app = photoshop.app;
const action = photoshop.action;

function textLayers(layers, out) {
  for (const l of layers || []) {
    if (String(l.kind).toLowerCase() === 'text') out.push(l);
    if (l.layers && l.layers.length) textLayers(l.layers, out);
  }
  return out;
}

module.exports = {
  app: 'Photoshop',
  async document() {
    const doc = app.activeDocument;
    if (!doc) return null;
    const installed = new Set();
    for (let i = 0; i < app.fonts.length; i++) installed.add(app.fonts[i].postScriptName);
    const layers = textLayers(doc.layers, []);
    const fonts = [];
    const seen = new Set();
    if (layers.length) {
      const res = await action.batchPlay(layers.map((l) => ({
        _obj: 'get',
        _target: [{ _ref: 'layer', _id: l.id }, { _ref: 'document', _id: doc.id }],
      })), {});
      for (const r of res) {
        const ranges = (r && r.textKey && r.textKey.textStyleRange) || [];
        for (const range of ranges) {
          const ts = range.textStyle || {};
          const ps = ts.fontPostScriptName || '';
          if (!ps || seen.has(ps)) continue;
          seen.add(ps);
          // Photoshop's list includes Adobe Fonts and the fonts it brings
          // itself, which no system folder shows: listed means present
          fonts.push({ postscript: ps, family: ts.fontName || '', style: ts.fontStyleName || '', missing: !installed.has(ps) });
        }
      }
    }
    return { name: doc.title || doc.name, fonts };
  },
  onChange(cb) {
    try {
      action.addNotificationListener(['open', 'close', 'select', 'make'], () => setTimeout(cb, 300));
    } catch (e) { /* older versions: the panel checks every few seconds */ }
  },
};

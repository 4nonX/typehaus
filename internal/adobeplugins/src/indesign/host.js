/* InDesign: the document's font list says which fonts are missing. */
const indesign = require('indesign');
const app = indesign.app;
const FontStatus = indesign.FontStatus;

function prop(f, name) {
  try { return f[name]; } catch (e) { return ''; } // some properties throw for missing fonts
}

function isInstalled(status) {
  return status === FontStatus.INSTALLED || String(status) === 'INSTALLED' || String(status) === String(FontStatus.INSTALLED);
}

module.exports = {
  app: 'InDesign',
  async document() {
    if (!app.documents.length) return null;
    const doc = app.activeDocument;
    const fonts = doc.fonts.everyItem().getElements().map((f) => ({
      postscript: prop(f, 'postscriptName') || '',
      family: prop(f, 'fontFamily') || '',
      style: prop(f, 'fontStyleName') || '',
      // NOT_AVAILABLE, SUBSTITUTED and FAUXED all mean the real font is missing
      missing: !isInstalled(prop(f, 'status')),
    }));
    return { name: doc.name, fonts };
  },
  onChange(cb) {
    for (const ev of ['afterOpen', 'afterNew', 'afterClose', 'afterContextChanged']) {
      try { app.addEventListener(ev, () => setTimeout(cb, 300)); } catch (e) { /* not in this version */ }
    }
  },
};

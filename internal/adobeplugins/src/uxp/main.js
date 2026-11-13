/* Start-up of the typehaus panel in UXP hosts (InDesign, Photoshop). */
const { entrypoints } = require('uxp');
const panel = require('./typehaus.js');
const host = require('./host.js');
const bridge = require('./bridge.js');

let started = null;
function start() {
  if (started) { started.refresh(); return; }
  const el = document.getElementById('typehaus');
  started = panel.mount(el, host, panel.createClient({ bridge: bridge }));
}

entrypoints.setup({
  panels: {
    typehausPanel: {
      show() { start(); },
    },
  },
});

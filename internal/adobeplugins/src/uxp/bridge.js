/*
 * The file bridge to the typehaus app, for hosts that allow plugins https
 * only (Photoshop on macOS): request and answer files in the typehaus
 * folder – ~/Library/Application Support/typehaus/plugins on macOS,
 * %APPDATA%\typehaus\plugins on Windows. Needs "localFileSystem":
 * "fullAccess" in the manifest.
 */
const fs = require('fs');
const os = require('os');

function base() {
  const home = os.homedir();
  if (os.platform() === 'darwin') return home + '/Library/Application Support/typehaus/plugins/';
  return home + '\\AppData\\Roaming\\typehaus\\plugins\\';
}
const sep = () => (os.platform() === 'darwin' ? '/' : '\\');
const full = (rel) => base() + rel.split('/').join(sep());

// UXP takes plain paths in recent versions and file: URLs in older ones.
async function both(fn, rel) {
  try {
    return await fn(full(rel));
  } catch (e) {
    return fn('file:' + full(rel));
  }
}

module.exports = {
  write: (rel, text) => both((p) => fs.writeFile(p, text, { encoding: 'utf-8' }), rel),
  read: async (rel) => {
    try {
      return await both((p) => fs.readFile(p, { encoding: 'utf-8' }), rel);
    } catch (e) {
      return null; // not there (yet)
    }
  },
  remove: (rel) => both((p) => fs.unlink(p), rel),
};

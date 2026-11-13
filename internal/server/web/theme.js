// Colour theme: "light", "dark" or unset (follow the operating system).
// Loaded in <head> without defer, so the page never flashes in the wrong theme.
// The choice is per browser; storage can be blocked, so every access is guarded.
(function () {
  var KEY = 'typehaus.theme';
  function read() {
    try { var v = localStorage.getItem(KEY); return v === 'light' || v === 'dark' ? v : ''; } catch (e) { return ''; }
  }
  function apply(v) {
    if (v) document.documentElement.setAttribute('data-theme', v);
    else document.documentElement.removeAttribute('data-theme');
  }
  apply(read());
  window.typehausTheme = {
    get: read,
    set: function (v) {
      try { if (v) localStorage.setItem(KEY, v); else localStorage.removeItem(KEY); } catch (e) {}
      apply(v);
    },
  };
})();

/*
 * Illustrator (CEP): fonts come from the ExtendScript side (host.jsx).
 * Illustrator's own font list also contains fonts missing from an open
 * document, so it can't say which are missing: the typehaus app decides
 * by what is installed on this computer (missing: null).
 */
(function (root) {
  'use strict';
  function evalScript(script) {
    return new Promise(function (resolve, reject) {
      var cep = root.__adobe_cep__;
      if (!cep) { reject(new Error('Not running inside Illustrator')); return; }
      cep.evalScript(script, function (res) { resolve(res); });
    });
  }

  // The panel is a web page (file://), so the browser would refuse to read
  // the app's answers (the app sends no CORS headers – on purpose: websites
  // must not use it). CEP's Node.js runtime (--enable-nodejs in the
  // manifest) talks to the app without that restriction.
  var nodeRequire = (root.cep_node && root.cep_node.require) || (typeof require === 'function' ? require : null);
  function nodeFetch(url, opts) {
    var http = null;
    try { http = nodeRequire && nodeRequire('http'); } catch (e) { http = null; }
    if (!http) return root.fetch(url, opts);
    return new Promise(function (resolve, reject) {
      var u = new URL(url);
      var req = http.request({ hostname: u.hostname, port: u.port, path: u.pathname + u.search, method: opts.method || 'GET', headers: opts.headers || {} }, function (res) {
        var text = '';
        res.setEncoding('utf8');
        res.on('data', function (c) { text += c; });
        res.on('end', function () { resolve({ status: res.statusCode, text: function () { return Promise.resolve(text); } }); });
      });
      req.on('error', reject);
      req.setTimeout(120000, function () { req.destroy(new Error('timeout')); });
      if (opts.body) req.write(opts.body);
      req.end();
    });
  }

  root.TypehausIllustrator = {
    app: 'Illustrator',
    fetch: nodeFetch,
    document: function () {
      return evalScript('typehausDocumentFonts()').then(function (res) {
        var r;
        try { r = JSON.parse(res); } catch (e) { throw new Error('Illustrator answered: ' + String(res).slice(0, 200)); }
        if (r.error) throw new Error(r.error);
        if (!r.doc) return null;
        r.doc.fonts.forEach(function (f) { f.missing = null; });
        return r.doc;
      });
    },
    onChange: function (cb) {
      var cep = root.__adobe_cep__;
      if (!cep || !cep.addEventListener) return;
      ['documentAfterActivate', 'documentAfterDeactivate'].forEach(function (ev) {
        try { cep.addEventListener(ev, function () { setTimeout(cb, 300); }, null); } catch (e) { /* older versions */ }
      });
    },
  };
})(typeof window !== 'undefined' ? window : globalThis);

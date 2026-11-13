// typehaus – Illustrator side (ExtendScript, ES3): the fonts the active
// document's text uses, as JSON. Read only.

function typehausQuote(s) {
    s = String(s === undefined || s === null ? '' : s);
    var out = '"';
    for (var i = 0; i < s.length; i++) {
        var c = s.charAt(i), n = s.charCodeAt(i);
        if (c === '"' || c === '\\') out += '\\' + c;
        else if (n < 32) out += '\\u' + ('0000' + n.toString(16)).slice(-4);
        else out += c;
    }
    return out + '"';
}

function typehausDocumentFonts() {
    try {
        if (app.documents.length === 0) return '{"doc":null}';
        var d = app.activeDocument;
        var seen = {}, list = [], budget = 50000;
        for (var i = 0; i < d.textFrames.length && budget > 0; i++) {
            var ranges;
            try { ranges = d.textFrames[i].textRanges; } catch (e) { continue; }
            for (var j = 0; j < ranges.length && budget > 0; j++, budget--) {
                var f;
                try { f = ranges[j].characterAttributes.textFont; } catch (e) { continue; }
                if (!f) continue;
                var name = f.name; // the PostScript name
                if (!name || seen[name]) continue;
                seen[name] = true;
                var family = '', style = '';
                try { family = f.family; style = f.style; } catch (e) {}
                list.push('{"postscript":' + typehausQuote(name) + ',"family":' + typehausQuote(family) + ',"style":' + typehausQuote(style) + '}');
            }
        }
        return '{"doc":{"name":' + typehausQuote(d.name) + ',"fonts":[' + list.join(',') + ']}}';
    } catch (e) {
        return '{"error":' + typehausQuote('Illustrator: ' + e) + '}';
    }
}

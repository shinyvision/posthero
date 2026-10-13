package script

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/dop251/goja"
	"github.com/dop251/goja_nodejs/require"
	nodeurl "github.com/dop251/goja_nodejs/url"

	"github.com/shinyvision/posthero/internal/vars"
)

// jsModules are small modules of Postman's sandbox, written in JavaScript
// on top of what the runtime has.
var jsModules = map[string]string{
	"atob":               `module.exports = function (s) { return atob(s); };`,
	"btoa":               `module.exports = function (s) { return btoa(s); };`,
	"postman-collection": `module.exports = globalThis.__postmanCollection;`,
	"querystring": `
function esc(s) { return encodeURIComponent(s).replace(/%20/g, '+'); }
exports.escape = encodeURIComponent;
exports.unescape = function (s) { return decodeURIComponent(String(s).replace(/\+/g, ' ')); };
exports.stringify = exports.encode = function (obj, sep, eq) {
  sep = sep || '&'; eq = eq || '=';
  return Object.keys(obj || {}).map(function (k) {
    var v = obj[k];
    if (Array.isArray(v)) return v.map(function (x) { return esc(k) + eq + esc(x); }).join(sep);
    return esc(k) + eq + esc(v === undefined || v === null ? '' : v);
  }).join(sep);
};
exports.parse = exports.decode = function (s, sep, eq) {
  sep = sep || '&'; eq = eq || '=';
  var out = {};
  String(s || '').split(sep).forEach(function (part) {
    if (!part) return;
    var i = part.indexOf(eq);
    var k = exports.unescape(i < 0 ? part : part.slice(0, i));
    var v = i < 0 ? '' : exports.unescape(part.slice(i + 1));
    if (Object.prototype.hasOwnProperty.call(out, k)) out[k] = [].concat(out[k], v); else out[k] = v;
  });
  return out;
};`,
	"path": `
exports.sep = '/';
exports.basename = function (p, ext) { var b = String(p).replace(/\/+$/, '').split('/').pop(); return ext && b.slice(-ext.length) === ext ? b.slice(0, -ext.length) : b; };
exports.dirname = function (p) { var parts = String(p).replace(/\/+$/, '').split('/'); parts.pop(); return parts.join('/') || (String(p)[0] === '/' ? '/' : '.'); };
exports.extname = function (p) { var b = exports.basename(p); var i = b.lastIndexOf('.'); return i <= 0 ? '' : b.slice(i); };
exports.join = function () { return exports.normalize([].slice.call(arguments).filter(function (s) { return s; }).join('/')); };
exports.normalize = function (p) {
  var abs = String(p)[0] === '/', out = [];
  String(p).split('/').forEach(function (s) { if (s === '..') out.pop(); else if (s && s !== '.') out.push(s); });
  return (abs ? '/' : '') + out.join('/');
};
exports.resolve = function () { return exports.normalize([].slice.call(arguments).join('/')); };
exports.isAbsolute = function (p) { return String(p)[0] === '/'; };`,
	"events": `
function EventEmitter() { this._events = {}; }
EventEmitter.prototype.on = EventEmitter.prototype.addListener = function (n, f) { (this._events[n] = this._events[n] || []).push(f); return this; };
EventEmitter.prototype.once = function (n, f) { var self = this; function g() { self.off(n, g); f.apply(self, arguments); } return this.on(n, g); };
EventEmitter.prototype.off = EventEmitter.prototype.removeListener = function (n, f) { this._events[n] = (this._events[n] || []).filter(function (x) { return x !== f; }); return this; };
EventEmitter.prototype.emit = function (n) { var args = [].slice.call(arguments, 1), self = this; var l = (this._events[n] || []).slice(); l.forEach(function (f) { f.apply(self, args); }); return l.length > 0; };
EventEmitter.prototype.removeAllListeners = function (n) { if (n) delete this._events[n]; else this._events = {}; return this; };
EventEmitter.prototype.listeners = function (n) { return (this._events[n] || []).slice(); };
module.exports = EventEmitter; module.exports.EventEmitter = EventEmitter;`,
	"csv-parse/lib/sync": csvParse,
	"csv-parse/sync":     csvParse,
	"xml2js":             xml2js,
	"util":               util,
	"assert": `
function fail(msg, d) { var e = new Error(msg || d); e.name = 'AssertionError'; throw e; }
function assert(v, msg) { if (!v) fail(msg, 'expected ' + v + ' to be truthy'); }
assert.ok = assert;
assert.equal = function (a, b, m) { if (a != b) fail(m, a + ' == ' + b); };
assert.notEqual = function (a, b, m) { if (a == b) fail(m, a + ' != ' + b); };
assert.strictEqual = function (a, b, m) { if (a !== b) fail(m, a + ' === ' + b); };
assert.notStrictEqual = function (a, b, m) { if (a === b) fail(m, a + ' !== ' + b); };
// Deep equality as lodash has it: key order doesn't matter.
function isEqual(a, b) { return require('lodash').isEqual(a, b); }
assert.deepEqual = assert.deepStrictEqual = function (a, b, m) { if (!isEqual(a, b)) fail(m, JSON.stringify(a) + ' deep equals ' + JSON.stringify(b)); };
assert.throws = function (f, m) { try { f(); } catch (e) { return; } fail(m, 'expected a throw'); };
assert.fail = function (m) { fail(m, 'failed'); };
module.exports = assert;`,
}

const csvParse = `
module.exports = function parse(input, opts) {
  opts = opts || {};
  var delim = opts.delimiter || ',', rows = [], row = [], field = '', q = false, s = String(input);
  for (var i = 0; i < s.length; i++) {
    var c = s[i];
    if (q) {
      if (c === '"' && s[i + 1] === '"') { field += '"'; i++; }
      else if (c === '"') q = false;
      else field += c;
    } else if (c === '"') q = true;
    else if (c === delim) { row.push(field); field = ''; }
    else if (c === '\n' || c === '\r') {
      if (c === '\r' && s[i + 1] === '\n') i++;
      row.push(field); field = '';
      if (!(opts.skip_empty_lines && row.length === 1 && row[0] === '')) rows.push(row);
      row = [];
    } else field += c;
  }
  if (field !== '' || row.length) { row.push(field); rows.push(row); }
  if (opts.trim) rows = rows.map(function (r) { return r.map(function (f) { return f.trim(); }); });
  if (opts.columns) {
    var cols = opts.columns === true ? rows.shift() : opts.columns;
    rows = rows.map(function (r) { var o = {}; cols.forEach(function (c, i) { o[c] = r[i]; }); return o; });
  }
  return rows;
};
module.exports.parse = module.exports;`

// compileModule compiles a module written in JavaScript.
func compileModule(name, src string) *goja.Program {
	code := "(function (module, exports, require) {\n" + src + "\n})"
	return goja.MustCompile(name+".js", code, false)
}

// loadModule runs a compiled module, filling module.exports.
func loadModule(rt *goja.Runtime, prog *goja.Program, module *goja.Object) {
	fn, err := rt.RunProgram(prog)
	if err != nil {
		panic(err)
	}
	call, _ := goja.AssertFunction(fn)
	if _, err := call(goja.Undefined(), module, module.Get("exports"), rt.Get("require")); err != nil {
		panic(err)
	}
}

func registerModules(reg *require.Registry) {
	for name, src := range jsModules {
		prog := compileModule(name, src)
		reg.RegisterNativeModule(name, func(rt *goja.Runtime, module *goja.Object) {
			loadModule(rt, prog, module)
		})
	}
	// url is goja_nodejs's (URL, URLSearchParams) with Node's older parse,
	// format and resolve, which Postman's sandbox has too.
	legacyURL := compileModule("url", urlModule)
	reg.RegisterNativeModule("url", func(rt *goja.Runtime, module *goja.Object) {
		nodeurl.Require(rt, module)
		loadModule(rt, legacyURL, module)
	})
	// uuid is a function that makes a v4 UUID, with v4 and friends on it, as
	// Postman's is.
	reg.RegisterNativeModule("uuid", func(rt *goja.Runtime, module *goja.Object) {
		v4 := func() string { return vars.UUID() }
		exports := rt.ToValue(v4).(*goja.Object)
		_ = exports.Set("v4", v4)
		_ = exports.Set("v1", v4)
		_ = exports.Set("v7", v4)
		_ = exports.Set("validate", func(s string) bool {
			return len(s) == 36 && strings.Count(s, "-") == 4
		})
		_ = module.Set("exports", exports)
	})
	for _, name := range []string{"cheerio", "stream", "string_decoder", "timers", "punycode"} {
		name := name
		reg.RegisterNativeModule(name, func(rt *goja.Runtime, _ *goja.Object) {
			panic(rt.NewGoError(errors.New("the " + name + " module isn't available in posthero's sandbox")))
		})
	}
}

// xmlNode is an element as the xml2js module builds on it: its name as
// written (soap:Envelope), its attributes in order, and what it holds,
// text and elements, in order.
type xmlNode struct {
	Name    string      `json:"name"`
	Attrs   [][2]string `json:"attrs"`
	Content []any       `json:"content"`
}

// xmlTree reads the root element of an XML document, nil when there is
// none. Names keep their prefixes, as xml2js keeps them.
func xmlTree(text string) (*xmlNode, error) {
	dec := xml.NewDecoder(strings.NewReader(text))
	dec.Strict = false
	var root *xmlNode
	var stack []*xmlNode
	for {
		tok, err := dec.RawToken()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			n := &xmlNode{Name: qualified(t.Name), Attrs: [][2]string{}, Content: []any{}}
			for _, a := range t.Attr {
				n.Attrs = append(n.Attrs, [2]string{qualified(a.Name), a.Value})
			}
			switch {
			case len(stack) > 0:
				top := stack[len(stack)-1]
				top.Content = append(top.Content, n)
			case root == nil:
				root = n
			default:
				return root, nil // what follows the root isn't part of it
			}
			stack = append(stack, n)
		case xml.CharData:
			if len(stack) > 0 {
				top := stack[len(stack)-1]
				top.Content = append(top.Content, string(t))
			}
		case xml.EndElement:
			// RawToken doesn't check that elements close in order.
			name := qualified(t.Name)
			if len(stack) == 0 {
				return nil, fmt.Errorf("</%s> closes nothing", name)
			}
			if open := stack[len(stack)-1].Name; open != name {
				return nil, fmt.Errorf("<%s> is closed by </%s>", open, name)
			}
			stack = stack[:len(stack)-1]
		}
	}
	if len(stack) > 0 {
		return nil, fmt.Errorf("<%s> isn't closed", stack[len(stack)-1].Name)
	}
	return root, nil
}

// qualified is a name as it was written, with its prefix.
func qualified(n xml.Name) string {
	if n.Space == "" {
		return n.Local
	}
	return n.Space + ":" + n.Local
}

package script

// The larger modules of Postman's sandbox that posthero writes in
// JavaScript (see jsModules).

// xml2js builds objects from XML the way xml2js does, with its options and
// their defaults (explicitArray: true, explicitRoot: true, …), on the
// element tree posthero reads (see xmlTree). Postman's xml2Json is this with
// explicitArray: false and trim: true.
const xml2js = `
var defaults = {
  explicitArray: true, explicitRoot: true, mergeAttrs: false, ignoreAttrs: false,
  attrkey: '$', charkey: '_', explicitCharkey: false, trim: false, normalize: false,
  normalizeTags: false, emptyTag: ''
};
var own = function (o, k) { return Object.prototype.hasOwnProperty.call(o, k); };

function options(opts) {
  var o = {}, k;
  for (k in defaults) o[k] = defaults[k];
  for (k in opts || {}) if (own(opts, k)) o[k] = opts[k];
  o.tagNameProcessors = [].concat(o.tagNameProcessors || []);
  if (o.normalizeTags) o.tagNameProcessors.unshift(function (n) { return n.toLowerCase(); });
  return o;
}

function processItem(fns, item, key) {
  [].concat(fns || []).forEach(function (f) { item = f(item, key); });
  return item;
}

function assignOrPush(o, obj, key, value) {
  if (!own(obj, key)) obj[key] = o.explicitArray ? [value] : value;
  else {
    if (!Array.isArray(obj[key])) obj[key] = [obj[key]];
    obj[key].push(value);
  }
}

// element turns one element into its name and value, as xml2js does when
// the element closes.
function element(node, o) {
  var obj = {};
  obj[o.charkey] = '';
  if (!o.ignoreAttrs) {
    node.attrs.forEach(function (a) {
      var value = o.attrValueProcessors ? processItem(o.attrValueProcessors, a[1], a[0]) : a[1];
      var key = o.attrNameProcessors ? processItem(o.attrNameProcessors, a[0]) : a[0];
      if (o.mergeAttrs) assignOrPush(o, obj, key, value);
      else {
        if (!own(obj, o.attrkey)) obj[o.attrkey] = {};
        obj[o.attrkey][key] = value;
      }
    });
  }
  var name = processItem(o.tagNameProcessors, node.name);
  node.content.forEach(function (c) {
    if (typeof c === 'string') obj[o.charkey] += c;
    else {
      var child = element(c, o);
      assignOrPush(o, obj, child.name, child.value);
    }
  });
  var emptyStr = '';
  if (/^\s*$/.test(obj[o.charkey])) {
    emptyStr = obj[o.charkey];
    delete obj[o.charkey];
  } else {
    if (o.trim) obj[o.charkey] = obj[o.charkey].trim();
    if (o.normalize) obj[o.charkey] = obj[o.charkey].replace(/\s{2,}/g, ' ').trim();
    if (o.valueProcessors) obj[o.charkey] = processItem(o.valueProcessors, obj[o.charkey], name);
    if (Object.keys(obj).length === 1 && own(obj, o.charkey) && !o.explicitCharkey) obj = obj[o.charkey];
  }
  if (typeof obj === 'object' && obj !== null && Object.keys(obj).length === 0) {
    obj = typeof o.emptyTag === 'function' ? o.emptyTag() : (o.emptyTag !== '' ? o.emptyTag : emptyStr);
  }
  return { name: name, value: obj };
}

function toObject(xml, o) {
  if (xml.trim() === '') return null;
  var root = JSON.parse(__ph.xmltree(xml));
  if (!root) return null;
  var r = element(root, o);
  if (!o.explicitRoot) return r.value;
  var out = {};
  out[r.name] = r.value;
  return out;
}

function parse(xml, opts, cb) {
  if (typeof opts === 'function') { cb = opts; opts = {}; }
  var out = null, err = null;
  try { out = toObject(String(xml), options(opts)); } catch (e) { err = e; }
  if (cb) cb(err, out);
  return out;
}

function Parser(opts) { this.opts = opts || {}; }
Parser.prototype.parseString = function (xml, cb) { return parse(xml, this.opts, cb); };
Parser.prototype.parseStringPromise = function (xml) {
  var self = this;
  return new Promise(function (ok, no) { parse(xml, self.opts, function (e, r) { if (e) no(e); else ok(r); }); });
};
exports.parseString = parse;
exports.parseStringPromise = function (xml, opts) { return new Parser(opts).parseStringPromise(xml); };
exports.Parser = Parser;
exports.processors = {
  normalize: function (s) { return s.toLowerCase(); },
  firstCharLowerCase: function (s) { return s.charAt(0).toLowerCase() + s.slice(1); },
  stripPrefix: function (s) { return s.replace(/(?!xmlns)^.*:/, ''); },
  parseNumbers: function (s) { if (!isNaN(s)) s = s % 1 === 0 ? parseInt(s, 10) : parseFloat(s); return s; },
  parseBooleans: function (s) { if (/^(?:true|false)$/i.test(s)) s = s.toLowerCase() === 'true'; return s; }
};`

// util is Node's util as browserify has it, which is what Postman's
// sandbox gives scripts: format, inspect, inherits and the is* checks.
const util = `
function isArray(v) { return Array.isArray(v); }
function isBoolean(v) { return typeof v === 'boolean'; }
function isNull(v) { return v === null; }
function isNullOrUndefined(v) { return v === null || v === undefined; }
function isNumber(v) { return typeof v === 'number'; }
function isString(v) { return typeof v === 'string'; }
function isSymbol(v) { return typeof v === 'symbol'; }
function isUndefined(v) { return v === undefined; }
function isObject(v) { return typeof v === 'object' && v !== null; }
function isFunction(v) { return typeof v === 'function'; }
function objectToString(v) { return Object.prototype.toString.call(v); }
function isRegExp(v) { return isObject(v) && objectToString(v) === '[object RegExp]'; }
function isDate(v) { return isObject(v) && objectToString(v) === '[object Date]'; }
function isError(v) { return isObject(v) && (objectToString(v) === '[object Error]' || v instanceof Error); }
function isPrimitive(v) { return v === null || (typeof v !== 'object' && typeof v !== 'function'); }
function own(o, k) { return Object.prototype.hasOwnProperty.call(o, k); }

function inspect(obj, opts) {
  var ctx = { seen: [] };
  if (arguments.length >= 3) ctx.depth = arguments[2];
  if (isBoolean(opts)) ctx.showHidden = opts;
  else if (opts) for (var k in opts) if (own(opts, k)) ctx[k] = opts[k];
  if (ctx.depth === undefined) ctx.depth = 2;
  if (ctx.customInspect === undefined) ctx.customInspect = true;
  return formatValue(ctx, obj, ctx.depth);
}

function formatValue(ctx, value, recurseTimes) {
  if (ctx.customInspect && value && isFunction(value.inspect) && value.inspect !== inspect && !(value.constructor && value.constructor.prototype === value)) {
    var ret = value.inspect(recurseTimes, ctx);
    return isString(ret) ? ret : formatValue(ctx, ret, recurseTimes);
  }
  var primitive = formatPrimitive(value);
  if (primitive) return primitive;
  var keys = Object.keys(value);
  var visible = {};
  keys.forEach(function (k) { visible[k] = true; });
  if (ctx.showHidden) keys = Object.getOwnPropertyNames(value);
  if (isError(value) && (keys.indexOf('message') >= 0 || keys.indexOf('description') >= 0)) return formatError(value);
  if (keys.length === 0) {
    if (isFunction(value)) return '[Function' + (value.name ? ': ' + value.name : '') + ']';
    if (isRegExp(value)) return RegExp.prototype.toString.call(value);
    if (isDate(value)) return Date.prototype.toString.call(value);
    if (isError(value)) return formatError(value);
  }
  var base = '', array = false, braces = ['{', '}'];
  if (isArray(value)) { array = true; braces = ['[', ']']; }
  if (isFunction(value)) base = ' [Function' + (value.name ? ': ' + value.name : '') + ']';
  if (isRegExp(value)) base = ' ' + RegExp.prototype.toString.call(value);
  if (isDate(value)) base = ' ' + Date.prototype.toUTCString.call(value);
  if (isError(value)) base = ' ' + formatError(value);
  if (keys.length === 0 && (!array || value.length === 0)) return braces[0] + base + braces[1];
  if (recurseTimes < 0) return isRegExp(value) ? RegExp.prototype.toString.call(value) : '[Object]';
  ctx.seen.push(value);
  var output;
  if (array) {
    output = [];
    for (var i = 0; i < value.length; i++) output.push(own(value, String(i)) ? formatProperty(ctx, value, recurseTimes, visible, String(i), true) : '');
    keys.forEach(function (k) { if (!/^\d+$/.test(k)) output.push(formatProperty(ctx, value, recurseTimes, visible, k, true)); });
  } else {
    output = keys.map(function (k) { return formatProperty(ctx, value, recurseTimes, visible, k, false); });
  }
  ctx.seen.pop();
  return reduceToSingleString(output, base, braces);
}

function formatPrimitive(value) {
  if (isUndefined(value)) return 'undefined';
  if (isString(value)) return "'" + JSON.stringify(value).replace(/^"|"$/g, '').replace(/'/g, "\\'").replace(/\\"/g, '"') + "'";
  if (isNumber(value) || isBoolean(value)) return '' + value;
  if (isNull(value)) return 'null';
  if (isSymbol(value)) return value.toString();
  return '';
}

function formatError(value) { return '[' + Error.prototype.toString.call(value) + ']'; }

function formatProperty(ctx, value, recurseTimes, visible, key, array) {
  var name, str;
  var desc = Object.getOwnPropertyDescriptor(value, key) || { value: value[key] };
  if (desc.get) str = desc.set ? '[Getter/Setter]' : '[Getter]';
  else if (desc.set) str = '[Setter]';
  if (!own(visible, key)) name = '[' + key + ']';
  if (!str) {
    if (ctx.seen.indexOf(desc.value) < 0) {
      str = formatValue(ctx, desc.value, recurseTimes === null ? null : recurseTimes - 1);
      if (str.indexOf('\n') > -1) {
        if (array) str = str.split('\n').map(function (l) { return '  ' + l; }).join('\n').substr(2);
        else str = '\n' + str.split('\n').map(function (l) { return '   ' + l; }).join('\n');
      }
    } else str = '[Circular]';
  }
  if (name === undefined) {
    if (array && /^\d+$/.test(key)) return str;
    name = JSON.stringify('' + key);
    if (/^"([a-zA-Z_][a-zA-Z_0-9]*)"$/.test(name)) name = name.substr(1, name.length - 2);
    else name = name.replace(/'/g, "\\'").replace(/\\"/g, '"').replace(/(^"|"$)/g, "'");
  }
  return name + ': ' + str;
}

function reduceToSingleString(output, base, braces) {
  var length = output.reduce(function (n, s) { return n + s.length + 1; }, 0);
  if (length > 60) return braces[0] + (base === '' ? '' : base + '\n ') + ' ' + output.join(',\n  ') + ' ' + braces[1];
  return braces[0] + base + ' ' + output.join(', ') + ' ' + braces[1];
}

function format(f) {
  var i;
  if (!isString(f)) {
    var objects = [];
    for (i = 0; i < arguments.length; i++) objects.push(inspect(arguments[i]));
    return objects.join(' ');
  }
  i = 1;
  var args = arguments, len = args.length;
  var str = String(f).replace(/%[sdj%]/g, function (x) {
    if (x === '%%') return '%';
    if (i >= len) return x;
    switch (x) {
      case '%s': return String(args[i++]);
      case '%d': return Number(args[i++]);
      case '%j':
        try { return JSON.stringify(args[i++]); } catch (e) { return '[Circular]'; }
    }
    return x;
  });
  for (var x = args[i]; i < len; x = args[++i]) str += isNull(x) || !isObject(x) ? ' ' + x : ' ' + inspect(x);
  return str;
}

exports.format = format;
exports.inspect = inspect;
exports.inherits = function (ctor, superCtor) {
  ctor.super_ = superCtor;
  ctor.prototype = Object.create(superCtor.prototype, { constructor: { value: ctor, enumerable: false, writable: true, configurable: true } });
};
exports.deprecate = function (fn) { return function () { return fn.apply(this, arguments); }; };
exports.promisify = function (fn) {
  return function () {
    var self = this, args = [].slice.call(arguments);
    return new Promise(function (ok, no) { fn.apply(self, args.concat(function (err, v) { if (err) no(err); else ok(v); })); });
  };
};
exports._extend = function (origin, add) {
  if (!isObject(add)) return origin;
  Object.keys(add).forEach(function (k) { origin[k] = add[k]; });
  return origin;
};
exports.isArray = isArray; exports.isBoolean = isBoolean; exports.isNull = isNull;
exports.isNullOrUndefined = isNullOrUndefined; exports.isNumber = isNumber; exports.isString = isString;
exports.isSymbol = isSymbol; exports.isUndefined = isUndefined; exports.isRegExp = isRegExp;
exports.isObject = isObject; exports.isDate = isDate; exports.isError = isError;
exports.isFunction = isFunction; exports.isPrimitive = isPrimitive;`

// urlModule adds Node's older url API to goja_nodejs's url module: parse,
// format and resolve, as Postman's sandbox (browserify's url) has them.
const urlModule = `
var qs = require('querystring');
var slashed = { 'http:': true, 'https:': true, 'ftp:': true, 'gopher:': true, 'file:': true, 'ws:': true, 'wss:': true };
var hostless = { 'javascript:': true };

function Url() {
  this.protocol = null; this.slashes = null; this.auth = null; this.host = null; this.port = null;
  this.hostname = null; this.hash = null; this.search = null; this.query = null; this.pathname = null;
  this.path = null; this.href = null;
}

function parse(input, parseQueryString, slashesDenoteHost) {
  if (input instanceof Url) return input;
  var u = new Url(), rest = String(input).trim();
  var hash = rest.indexOf('#');
  if (hash >= 0) { u.hash = rest.slice(hash); rest = rest.slice(0, hash); }
  var q = rest.indexOf('?');
  if (q >= 0) {
    u.search = rest.slice(q);
    u.query = rest.slice(q + 1);
    rest = rest.slice(0, q);
    if (parseQueryString) u.query = qs.parse(u.query);
  } else if (parseQueryString) {
    u.search = '';
    u.query = {};
  }
  var proto = /^([a-z0-9.+-]+:)/i.exec(rest);
  if (proto) { u.protocol = proto[1].toLowerCase(); rest = rest.slice(proto[1].length); }
  if (slashesDenoteHost || proto || /^\/\/[^@\/]+@[^@\/]+/.test(rest)) {
    if (rest.slice(0, 2) === '//' && !hostless[u.protocol]) { rest = rest.slice(2); u.slashes = true; }
  }
  if (!hostless[u.protocol] && (u.slashes || (proto && !slashed[u.protocol]))) {
    var end = rest.search(/[\/\\]/);
    if (end < 0) end = rest.length;
    var host = rest.slice(0, end);
    rest = rest.slice(end);
    var at = host.lastIndexOf('@');
    if (at >= 0) { u.auth = decodeURIComponent(host.slice(0, at)); host = host.slice(at + 1); }
    var port = /:([0-9]*)$/.exec(host);
    if (port) { if (port[1]) u.port = port[1]; host = host.slice(0, port.index); }
    var ipv6 = host.charAt(0) === '[' && host.charAt(host.length - 1) === ']';
    u.hostname = (ipv6 ? host.slice(1, -1) : host).toLowerCase();
    u.host = (ipv6 ? '[' + u.hostname + ']' : u.hostname) + (u.port ? ':' + u.port : '');
  }
  if (rest) u.pathname = rest;
  else if (slashed[u.protocol] && u.hostname) u.pathname = '/';
  if (u.pathname || u.search) u.path = (u.pathname || '') + (u.search || '');
  u.href = format(u);
  return u;
}

function format(obj) {
  if (typeof obj === 'string') obj = parse(obj);
  var auth = obj.auth ? encodeURIComponent(obj.auth).replace(/%3A/i, ':') + '@' : '';
  var protocol = obj.protocol || '', pathname = obj.pathname || '', hash = obj.hash || '', host = false, query = '';
  if (obj.host) host = auth + obj.host;
  else if (obj.hostname) {
    host = auth + (obj.hostname.indexOf(':') >= 0 ? '[' + obj.hostname + ']' : obj.hostname);
    if (obj.port) host += ':' + obj.port;
  }
  if (obj.query && typeof obj.query === 'object' && Object.keys(obj.query).length) query = qs.stringify(obj.query);
  var search = obj.search || (query && '?' + query) || '';
  if (protocol && protocol.slice(-1) !== ':') protocol += ':';
  if (obj.slashes || ((!protocol || slashed[protocol]) && host !== false)) {
    host = '//' + (host || '');
    if (pathname && pathname.charAt(0) !== '/') pathname = '/' + pathname;
  } else if (!host) host = '';
  if (hash && hash.charAt(0) !== '#') hash = '#' + hash;
  if (search && search.charAt(0) !== '?') search = '?' + search;
  pathname = pathname.replace(/[?#]/g, encodeURIComponent);
  search = search.replace('#', '%23');
  return protocol + host + pathname + search + hash;
}

// resolve works out a link from where it is, as a browser would (RFC
// 3986); from may be a path alone.
function resolve(from, to) {
  from = String(from);
  to = String(to);
  if (/^[a-z][a-z0-9+.-]*:/i.test(to)) return format(parse(to));
  var f = parse(from);
  if (to.slice(0, 2) === '//') return (f.protocol || '') + to;
  var origin = (f.protocol || '') + (f.slashes ? '//' : '') + (f.host !== null ? (f.auth ? f.auth + '@' : '') + f.host : '');
  var hash = '', query = null;
  var h = to.indexOf('#');
  if (h >= 0) { hash = to.slice(h); to = to.slice(0, h); }
  var q = to.indexOf('?');
  if (q >= 0) { query = to.slice(q); to = to.slice(0, q); }
  var path = f.pathname || '', search = f.search || '';
  if (to !== '') {
    if (to.charAt(0) === '/') path = to;
    else if (f.host && !path) path = '/' + to;
    else path = path.slice(0, path.lastIndexOf('/') + 1) + to;
    path = removeDots(path);
    search = query === null ? '' : query;
  } else if (query !== null) search = query;
  return origin + path + search + hash;
}

// removeDots resolves . and .. in a path.
function removeDots(path) {
  var segs = path.split('/'), out = [];
  segs.forEach(function (s) {
    if (s === '.') return;
    if (s === '..') {
      if (out.length > 1 || (out.length === 1 && out[0] !== '')) out.pop();
      return;
    }
    out.push(s);
  });
  var last = segs[segs.length - 1];
  if (last === '.' || last === '..') out.push('');
  return out.join('/');
}

Url.prototype.format = function () { return format(this); };
Url.prototype.resolve = function (to) { return resolve(this.href, to); };
exports.Url = Url;
exports.parse = parse;
exports.format = format;
exports.resolve = resolve;`

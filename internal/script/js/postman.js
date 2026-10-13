// posthero's version of Postman's script sandbox: pm, postman, the legacy
// globals and the libraries scripts require. It sits on __ph, the bridge
// to posthero (see engine.go).
(function (global) {
  'use strict';
  var ph = global.__ph;

  // ---------------------------------------------------------------------
  // Helpers
  // ---------------------------------------------------------------------

  function str(v) {
    if (v === undefined || v === null) return '';
    if (typeof v === 'object') {
      try { return JSON.stringify(v); } catch (e) { return String(v); }
    }
    return String(v);
  }

  function isObject(v) { return v !== null && typeof v === 'object'; }

  // ---------------------------------------------------------------------
  // PropertyList: Postman's list of headers, query parameters, cookies …
  // ---------------------------------------------------------------------

  function Property(def) {
    if (typeof def === 'string') def = { key: def, value: '' };
    def = def || {};
    for (var k in def) if (Object.prototype.hasOwnProperty.call(def, k)) this[k] = def[k];
    if (this.key === undefined && this.name !== undefined) this.key = this.name;
    if (this.value === undefined) this.value = '';
  }
  Property.prototype.toJSON = function () {
    var out = {};
    for (var k in this) if (Object.prototype.hasOwnProperty.call(this, k) && typeof this[k] !== 'function') out[k] = this[k];
    return out;
  };
  Property.prototype.valueOf = function () { return this.value; };
  Property.prototype.update = function (def) {
    if (typeof def === 'string') { this.value = def; return; }
    for (var k in def) if (Object.prototype.hasOwnProperty.call(def, k)) this[k] = def[k];
  };

  function Header(def, value) {
    if (typeof def === 'string' && value !== undefined) def = { key: def, value: value };
    else if (typeof def === 'string') {
      var i = def.indexOf(':');
      def = i < 0 ? { key: def.trim(), value: '' } : { key: def.slice(0, i).trim(), value: def.slice(i + 1).trim() };
    }
    Property.call(this, def);
  }
  Header.prototype = Object.create(Property.prototype);
  Header.prototype.toString = function () { return this.key + ': ' + this.value; };

  function QueryParam(def) {
    if (typeof def === 'string') {
      var i = def.indexOf('=');
      def = i < 0 ? { key: def, value: null } : { key: def.slice(0, i), value: def.slice(i + 1) };
    }
    Property.call(this, def);
  }
  QueryParam.prototype = Object.create(Property.prototype);
  QueryParam.prototype.toString = function () {
    return this.value === null || this.value === undefined ? this.key : this.key + '=' + this.value;
  };

  function Cookie(def) { Property.call(this, def); if (def && def.name !== undefined) this.name = def.name; }
  Cookie.prototype = Object.create(Property.prototype);
  Cookie.prototype.toString = function () { return this.name + '=' + this.value; };
  Cookie.prototype.valueOf = function () { return this.value; };

  function PropertyList(Type, items, caseless) {
    this._Type = Type || Property;
    this._caseless = !!caseless;
    this._items = [];
    this.populate(items);
  }
  PropertyList.prototype._norm = function (k) { return this._caseless && typeof k === 'string' ? k.toLowerCase() : k; };
  PropertyList.prototype._make = function (item) { return item instanceof this._Type ? item : new this._Type(item); };
  PropertyList.prototype._keyOf = function (item) { return item.key !== undefined ? item.key : item.name; };
  PropertyList.prototype.populate = function (items) {
    if (!items) return;
    if (typeof items === 'string') items = items.split('\n').filter(function (l) { return l.trim(); });
    if (!Array.isArray(items) && isObject(items)) {
      var list = [];
      for (var k in items) if (Object.prototype.hasOwnProperty.call(items, k)) list.push({ key: k, value: items[k] });
      items = list;
    }
    for (var i = 0; i < items.length; i++) this.add(items[i]);
  };
  PropertyList.prototype.repopulate = function (items) { this.clear(); this.populate(items); };
  PropertyList.prototype.clear = function () { this._items = []; };
  PropertyList.prototype.add = function (item) { if (item !== undefined && item !== null) this._items.push(this._make(item)); };
  PropertyList.prototype.append = PropertyList.prototype.add;
  PropertyList.prototype.prepend = function (item) { this._items.unshift(this._make(item)); };
  PropertyList.prototype.insert = function (item, before) {
    var at = before === undefined ? -1 : this.indexOf(before);
    if (at < 0) return this.add(item);
    this._items.splice(at, 0, this._make(item));
  };
  PropertyList.prototype.insertAfter = function (item, after) {
    var at = after === undefined ? -1 : this.indexOf(after);
    if (at < 0) return this.add(item);
    this._items.splice(at + 1, 0, this._make(item));
  };
  PropertyList.prototype.upsert = function (item) {
    var p = this._make(item);
    var key = this._norm(this._keyOf(p));
    for (var i = 0; i < this._items.length; i++) {
      if (this._norm(this._keyOf(this._items[i])) === key) {
        this._items[i].update(p.toJSON());
        return false;
      }
    }
    this._items.push(p);
    return true;
  };
  PropertyList.prototype.remove = function (predicate, context) {
    var self = this;
    var match;
    if (typeof predicate === 'function') match = function (it) { return predicate.call(context, it); };
    else if (predicate instanceof Property) match = function (it) { return it === predicate; };
    else if (isObject(predicate)) {
      var k = self._norm(self._keyOf(predicate));
      match = function (it) { return self._norm(self._keyOf(it)) === k; };
    } else {
      var key = self._norm(predicate);
      match = function (it) { return self._norm(self._keyOf(it)) === key; };
    }
    this._items = this._items.filter(function (it) { return !match(it); });
  };
  PropertyList.prototype.one = function (key) {
    var k = this._norm(key);
    for (var i = this._items.length - 1; i >= 0; i--) if (this._norm(this._keyOf(this._items[i])) === k) return this._items[i];
    return undefined;
  };
  PropertyList.prototype.get = function (key) {
    var k = this._norm(key);
    for (var i = this._items.length - 1; i >= 0; i--) {
      var it = this._items[i];
      if (this._norm(this._keyOf(it)) === k && !it.disabled) return it.valueOf();
    }
    return undefined;
  };
  PropertyList.prototype.has = function (key, value) {
    var k = this._norm(isObject(key) ? this._keyOf(key) : key);
    for (var i = 0; i < this._items.length; i++) {
      var it = this._items[i];
      if (this._norm(this._keyOf(it)) === k && (arguments.length < 2 || it.value === value)) return true;
    }
    return false;
  };
  PropertyList.prototype.idx = function (i) { return this._items[i]; };
  PropertyList.prototype.indexOf = function (item) {
    if (typeof item === 'string') {
      var k = this._norm(item);
      for (var i = 0; i < this._items.length; i++) if (this._norm(this._keyOf(this._items[i])) === k) return i;
      return -1;
    }
    return this._items.indexOf(item);
  };
  PropertyList.prototype.count = function () { return this._items.length; };
  PropertyList.prototype.all = function () { return this._items.slice(); };
  PropertyList.prototype.each = function (fn, ctx) { this._items.forEach(fn, ctx); };
  PropertyList.prototype.forEach = PropertyList.prototype.each;
  PropertyList.prototype.filter = function (fn, ctx) { return this._items.filter(fn, ctx); };
  PropertyList.prototype.find = function (fn, ctx) { return this._items.find(fn, ctx); };
  PropertyList.prototype.map = function (fn, ctx) { return this._items.map(fn, ctx); };
  PropertyList.prototype.reduce = function (fn, acc, ctx) { return this._items.reduce(function (a, it) { return fn.call(ctx, a, it); }, acc); };
  PropertyList.prototype.toObject = function (excludeDisabled, caseSensitive, multiValue) {
    var out = {};
    var self = this;
    this._items.forEach(function (it) {
      if (excludeDisabled && it.disabled) return;
      var k = self._keyOf(it);
      if (self._caseless && caseSensitive === false) k = String(k).toLowerCase();
      if (multiValue && Object.prototype.hasOwnProperty.call(out, k)) {
        out[k] = [].concat(out[k], it.valueOf());
      } else {
        out[k] = it.valueOf();
      }
    });
    return out;
  };
  PropertyList.prototype.toJSON = function () { return this._items.map(function (it) { return it.toJSON(); }); };
  PropertyList.prototype.toString = function () {
    return this._items.filter(function (it) { return !it.disabled; }).map(function (it) { return it.toString(); }).join(this._Type === QueryParam ? '&' : '\n');
  };
  PropertyList.prototype.eachParent = function () {};
  PropertyList.prototype.assimilate = function (source) { this.populate(source instanceof PropertyList ? source.all() : source); };
  PropertyList.isPropertyList = function (o) { return o instanceof PropertyList; };
  Object.defineProperty(PropertyList.prototype, 'members', { get: function () { return this._items; } });
  if (typeof Symbol !== 'undefined') {
    PropertyList.prototype[Symbol.iterator] = function () { return this._items[Symbol.iterator](); };
  }

  // ---------------------------------------------------------------------
  // Url
  // ---------------------------------------------------------------------

  function splitRaw(raw) {
    var depth = 0, q = -1, h = -1;
    for (var i = 0; i < raw.length; i++) {
      if (raw.substr(i, 2) === '{{') { depth++; i++; continue; }
      if (raw.substr(i, 2) === '}}' && depth > 0) { depth--; i++; continue; }
      if (depth > 0) continue;
      if (raw[i] === '?' && q < 0 && h < 0) q = i;
      else if (raw[i] === '#' && h < 0) h = i;
    }
    var end = raw.length, hash = '', query = '';
    if (h >= 0) { hash = raw.slice(h + 1); end = h; }
    if (q >= 0) { query = raw.slice(q + 1, end); end = q; }
    return { base: raw.slice(0, end), query: query, hash: hash, hasQuery: q >= 0 };
  }

  function Url(def) {
    this.query = new PropertyList(QueryParam);
    this.variables = new PropertyList(Property);
    this.update(def || '');
  }
  Url.prototype.update = function (def) {
    if (typeof def === 'string') def = { raw: def };
    def = def || {};
    var raw = def.raw !== undefined ? String(def.raw) : '';
    var parts = splitRaw(raw);
    var rest = parts.base;
    this.protocol = undefined;
    // The protocol may be a variable: {{scheme}}://…
    var m = /^((?:\{\{[^{}]*\}\}|[^:/{}])+):\/\//.exec(rest);
    if (m) { this.protocol = m[1]; rest = rest.slice(m[0].length); }
    var slash = rest.indexOf('/');
    var host = slash < 0 ? rest : rest.slice(0, slash);
    var path = slash < 0 ? '' : rest.slice(slash + 1);
    var at = host.lastIndexOf('@');
    if (at >= 0 && host.indexOf('}}', at) < 0) {
      var cred = host.slice(0, at).split(':');
      this.auth = { user: cred[0], password: cred.slice(1).join(':') };
      host = host.slice(at + 1);
    }
    var colon = host.lastIndexOf(':');
    this.port = undefined;
    if (colon >= 0 && host.indexOf('}}', colon) < 0 && host.indexOf(']', colon) < 0) { this.port = host.slice(colon + 1); host = host.slice(0, colon); }
    this.host = host ? (host.indexOf('{{') >= 0 ? [host] : host.split('.')) : [];
    this.path = path || rest.slice(-1) === '/' ? path.split('/') : [];
    this.hash = parts.hash || undefined;
    this.query.clear();
    var query = def.query;
    if (query && query.length !== undefined && typeof query !== 'string') this.query.populate(query);
    else if (parts.query) this.query.populate(parts.query.split('&').filter(function (s) { return s; }));
    this.variables.clear();
    if (def.variable) this.variables.populate(def.variable);
  };
  Url.prototype.getHost = function () { return this.host.join('.'); };
  Url.prototype.getPath = function (unresolved) {
    var vars = this.variables;
    var segs = this.path.map(function (s) {
      if (!unresolved && s.charAt(0) === ':' && vars.has(s.slice(1))) return vars.get(s.slice(1));
      return s;
    });
    return '/' + segs.join('/');
  };
  Url.prototype.getQueryString = function () { return this.query.toString(); };
  Url.prototype.getPathWithQuery = function () {
    var q = this.getQueryString();
    return this.getPath() + (q ? '?' + q : '');
  };
  Url.prototype.getRemote = function (forcePort) {
    var port = this.port || (forcePort ? (this.protocol === 'https' ? '443' : '80') : '');
    return this.getHost() + (port ? ':' + port : '');
  };
  Url.prototype.getRaw = function () { return this.toString(); };
  Url.prototype.getOAuth1BaseUrl = function () { return (this.protocol || 'http') + '://' + this.getRemote() + this.getPath(); };
  Url.prototype.toString = function (forceProtocol) {
    var out = '';
    if (this.protocol) out += this.protocol + '://';
    else if (forceProtocol) out += 'http://';
    if (this.auth && this.auth.user) out += this.auth.user + (this.auth.password ? ':' + this.auth.password : '') + '@';
    out += this.getHost();
    if (this.port) out += ':' + this.port;
    if (this.path.length) out += '/' + this.path.join('/');
    var q = this.getQueryString();
    if (q) out += '?' + q;
    if (this.hash) out += '#' + this.hash;
    return out;
  };
  Url.prototype.addQueryParams = function (params) {
    if (typeof params === 'string') params = params.split('&');
    var self = this;
    (Array.isArray(params) ? params : [params]).forEach(function (p) { self.query.add(p); });
  };
  Url.prototype.removeQueryParams = function (params) {
    var self = this;
    (Array.isArray(params) ? params : [params]).forEach(function (p) { self.query.remove(isObject(p) ? p.key : p); });
  };
  Url.prototype.toJSON = function () {
    return { raw: this.toString(), protocol: this.protocol, host: this.host, port: this.port, path: this.path, query: this.query.toJSON(), hash: this.hash, variable: this.variables.toJSON() };
  };
  Url.parse = function (raw) { return new Url(raw).toJSON(); };
  Url.isUrl = function (o) { return o instanceof Url; };

  // ---------------------------------------------------------------------
  // Request and its body and auth
  // ---------------------------------------------------------------------

  function RequestBody(def) { this.update(def); }
  RequestBody.prototype.update = function (def) {
    if (typeof def === 'string') def = { mode: 'raw', raw: def };
    def = def || {};
    this.mode = def.mode;
    this.raw = def.raw;
    this.urlencoded = new PropertyList(Property, def.urlencoded);
    this.formdata = new PropertyList(Property, def.formdata);
    this.file = def.file;
    this.graphql = def.graphql;
    this.options = def.options;
    this.disabled = def.disabled;
  };
  RequestBody.prototype.isEmpty = function () {
    switch (this.mode) {
      case 'raw': return !this.raw;
      case 'urlencoded': return this.urlencoded.count() === 0;
      case 'formdata': return this.formdata.count() === 0;
      case 'file': return !this.file || !this.file.src;
      case 'graphql': return !this.graphql || !this.graphql.query;
    }
    return true;
  };
  RequestBody.prototype.toString = function () {
    switch (this.mode) {
      case 'raw': return this.raw || '';
      case 'urlencoded': return this.urlencoded.toString().replace(/\n/g, '&');
      case 'graphql': return JSON.stringify(this.graphql || {});
    }
    return '';
  };
  RequestBody.prototype.toJSON = function () {
    var out = { mode: this.mode };
    if (this.mode === 'raw') out.raw = this.raw;
    if (this.mode === 'urlencoded') out.urlencoded = this.urlencoded.toJSON();
    if (this.mode === 'formdata') out.formdata = this.formdata.toJSON();
    if (this.mode === 'file') out.file = this.file;
    if (this.mode === 'graphql') out.graphql = this.graphql;
    if (this.options) out.options = this.options;
    if (this.disabled) out.disabled = true;
    return out;
  };

  // RequestAuth keeps the parameters of every auth type it was given, each
  // under the type's name (auth.bearer, auth.basic, …), as Postman's SDK
  // does; type says which one applies.
  function RequestAuth(def) {
    if (typeof def === 'string') def = { type: def };
    def = def || {};
    this.type = 'noauth';
    if (def.type) this.use(def.type);
    for (var k in def) if (Object.prototype.hasOwnProperty.call(def, k) && k !== 'type') this.update(def[k], k);
  }
  RequestAuth.isValidType = function (type) {
    return typeof type === 'string' && type !== 'type' && !(type in RequestAuth.prototype);
  };
  // _list is the parameters of a type, made when it has none yet.
  RequestAuth.prototype._list = function (type) {
    if (!(this[type] instanceof PropertyList)) this[type] = new PropertyList(Property);
    return this[type];
  };
  // _fill upserts parameters given as a list, a PropertyList or an object of
  // key: value.
  RequestAuth.prototype._fill = function (type, options) {
    var list = this._list(type);
    if (options instanceof PropertyList) options = options.all();
    if (Array.isArray(options)) {
      options.forEach(function (p) { if (isObject(p) && p.key !== undefined) list.upsert(p instanceof Property ? p.toJSON() : p); });
      return;
    }
    if (!isObject(options)) return;
    for (var k in options) if (Object.prototype.hasOwnProperty.call(options, k)) list.upsert({ key: k, value: options[k] });
  };
  // update changes parameters of a type, the current one when none is
  // given.
  RequestAuth.prototype.update = function (options, type) {
    if (!isObject(options)) return;
    type = type || this.type;
    if (!RequestAuth.isValidType(type)) return;
    this._fill(type, options);
  };
  // use switches to a type, with parameters for it.
  RequestAuth.prototype.use = function (type, options) {
    if (!RequestAuth.isValidType(type)) return;
    this.type = type;
    this._list(type);
    if (options) this._fill(type, options);
  };
  RequestAuth.prototype.clear = function (type) {
    if (this[type] instanceof PropertyList) this[type].clear();
  };
  RequestAuth.prototype.parameters = function () { return this._list(this.type); };
  RequestAuth.prototype.current = function () {
    var out = {};
    this._list(this.type).each(function (p) { out[p.key] = p.value; });
    return out;
  };
  RequestAuth.prototype.toJSON = function () {
    var out = { type: this.type };
    for (var k in this) {
      if (Object.prototype.hasOwnProperty.call(this, k) && this[k] instanceof PropertyList && k !== 'noauth' && k !== 'inherit') out[k] = this[k].toJSON();
    }
    return out;
  };

  function Request(def) { this.update(def); }
  Request.prototype.update = function (def) {
    if (typeof def === 'string') def = { url: def };
    def = def || {};
    this.id = def.id;
    this.name = def.name;
    this.method = (def.method || 'GET').toUpperCase();
    this.url = def.url instanceof Url ? def.url : new Url(def.url);
    var headers = def.header || def.headers;
    this.headers = new PropertyList(Header, headers instanceof PropertyList ? headers.all() : headers, true);
    this.body = def.body ? (def.body instanceof RequestBody ? def.body : new RequestBody(def.body)) : undefined;
    this.auth = def.auth ? new RequestAuth(def.auth) : undefined;
    this.description = def.description;
    this.certificate = def.certificate;
    this.proxy = def.proxy;
  };
  Request.prototype.addHeader = function (h, v) { this.headers.add(new Header(h, v)); };
  Request.prototype.upsertHeader = function (h, v) { this.headers.upsert(new Header(h, v)); };
  Request.prototype.removeHeader = function (h, opts) {
    var key = isObject(h) ? h.key : h;
    var ignoreCase = !opts || opts.ignoreCase !== false;
    this.headers._items = this.headers._items.filter(function (it) {
      return ignoreCase ? String(it.key).toLowerCase() !== String(key).toLowerCase() : it.key !== key;
    });
  };
  Request.prototype.getHeaders = function (opts) {
    opts = opts || {};
    var out = {};
    this.headers.each(function (h) {
      if (opts.enabled && h.disabled) return;
      var k = opts.ignoreCase ? String(h.key).toLowerCase() : h.key;
      if (Object.prototype.hasOwnProperty.call(out, k) && opts.multiValue !== false) out[k] = [].concat(out[k], h.value);
      else out[k] = h.value;
    });
    return out;
  };
  Request.prototype.forEachHeader = function (fn) { this.headers.each(fn); };
  Request.prototype.addQueryParams = function (p) { this.url.addQueryParams(p); };
  Request.prototype.removeQueryParams = function (p) { this.url.removeQueryParams(p); };
  // authorizeUsing switches the request to an auth type, with fresh
  // parameters; null removes the auth.
  Request.prototype.authorizeUsing = function (type, options) {
    if (isObject(type) && (options === undefined || options === null)) {
      options = type[type.type];
      if (options === undefined) {
        options = {};
        for (var k in type) if (Object.prototype.hasOwnProperty.call(type, k) && k !== 'type') options[k] = type[k];
      }
      type = type.type;
    }
    if (type === null) { delete this.auth; return; }
    if (!RequestAuth.isValidType(type)) return;
    if (!(this.auth instanceof RequestAuth)) this.auth = new RequestAuth(this.auth);
    this.auth.clear(type);
    this.auth.use(type, options);
  };
  // toJSON takes parts a script replaced outright (pm.request.url = '…') as
  // Postman does.
  Request.prototype.toJSON = function () {
    var url = this.url instanceof Url ? this.url : new Url(this.url);
    var headers = this.headers instanceof PropertyList ? this.headers : new PropertyList(Header, this.headers, true);
    var body = this.body && !(this.body instanceof RequestBody) ? new RequestBody(this.body) : this.body;
    var auth = this.auth && !(this.auth instanceof RequestAuth) ? new RequestAuth(this.auth) : this.auth;
    return {
      id: this.id, name: this.name, method: this.method, url: url.toJSON(),
      header: headers.toJSON(), body: body ? body.toJSON() : undefined,
      auth: auth ? auth.toJSON() : undefined, description: this.description,
    };
  };
  Request.isRequest = function (o) { return o instanceof Request; };

  // ---------------------------------------------------------------------
  // Response
  // ---------------------------------------------------------------------

  function Response(def) {
    def = def || {};
    this._postman_type = 'response';
    this.id = def.id;
    this.code = def.code;
    this.status = def.status;
    this.headers = new PropertyList(Header, def.headers, true);
    this.cookies = new PropertyList(Cookie, (def.cookies || []).map(function (c) { return new Cookie(c); }));
    this.responseTime = def.responseTime;
    this.responseSize = def.responseSize;
    this._body = def.body === undefined ? '' : def.body;
    this._bytes = def.bytes;
    this._size = def.size || { body: this._body.length, header: 0, total: this._body.length };
  }
  Object.defineProperty(Response.prototype, 'stream', {
    get: function () { return global.Buffer ? global.Buffer.from(this._bytes || this._body) : this._body; },
  });
  Response.prototype.text = function () { return this._body; };
  Response.prototype.json = function (reviver, strict) {
    var body = this._body;
    // A byte order mark is dropped unless strict, as Postman's liquid-json
    // does.
    if (strict !== true) body = body.replace(/^\uFEFF/, '');
    try {
      return JSON.parse(body, reviver);
    } catch (e) {
      var err = new SyntaxError('the response isn\'t JSON (' + e.message + ')');
      throw err;
    }
  };
  Response.prototype.reason = function () { return this.status; };
  Response.prototype.size = function () { return this._size; };
  Response.prototype.dataURI = function () {
    var ct = this.headers.get('Content-Type') || 'text/plain';
    return 'data:' + ct.split(';')[0] + ';base64,' + ph.base64(this._bytes || this._body);
  };
  Response.prototype.contentInfo = function () {
    var ct = this.headers.get('Content-Type') || '';
    var parts = ct.split(';');
    var info = { mimeType: parts[0].trim(), mimeFormat: (parts[0].split('/')[1] || '').trim() };
    for (var i = 1; i < parts.length; i++) {
      var kv = parts[i].split('=');
      if (kv[0].trim() === 'charset') info.charset = (kv[1] || '').trim();
    }
    var cd = this.headers.get('Content-Disposition') || '';
    var fm = /filename="?([^";]+)"?/.exec(cd);
    if (fm) info.fileName = fm[1];
    return info;
  };
  Response.prototype.toJSON = function () {
    return { code: this.code, status: this.status, header: this.headers.toJSON(), body: this._body, responseTime: this.responseTime, cookie: this.cookies.toJSON() };
  };
  Response.isResponse = function (o) { return o instanceof Response; };

  // ---------------------------------------------------------------------
  // Variables
  // ---------------------------------------------------------------------

  function VariableScope(kind, readonly) { this._kind = kind; this._readonly = readonly; }
  VariableScope.prototype.get = function (key) { return ph.get(this._kind, String(key)); };
  VariableScope.prototype.set = function (key, value, type) {
    if (this._readonly) throw new Error('pm.' + this._kind + ' can only be read');
    if (key === undefined || key === null) throw new Error('a variable needs a name');
    ph.set(this._kind, String(key), value);
  };
  VariableScope.prototype.has = function (key) { return ph.has(this._kind, String(key)); };
  VariableScope.prototype.unset = function (key) { ph.unset(this._kind, String(key)); };
  VariableScope.prototype.clear = function () { ph.clear(this._kind); };
  VariableScope.prototype.toObject = function () { return ph.all(this._kind); };
  VariableScope.prototype.toJSON = function () {
    var obj = ph.all(this._kind), values = [];
    for (var k in obj) values.push({ key: k, value: obj[k] });
    return { name: this.name, values: values };
  };
  VariableScope.prototype.replaceIn = function (template) { return ph.resolveIn(this._kind, str(template)); };
  VariableScope.prototype.values = function () {
    var obj = ph.all(this._kind), list = [];
    for (var k in obj) list.push({ key: k, value: obj[k] });
    return new PropertyList(Property, list);
  };
  Object.defineProperty(VariableScope.prototype, 'name', { get: function () { return ph.scopeName(this._kind); } });

  // pm.variables reads through every scope and writes local ones.
  function Variables() { VariableScope.call(this, 'local'); }
  Variables.prototype = Object.create(VariableScope.prototype);
  Variables.prototype.get = function (key) { return ph.get('*', String(key)); };
  Variables.prototype.has = function (key) { return ph.has('*', String(key)); };
  Variables.prototype.toObject = function () { return ph.all('*'); };
  Variables.prototype.replaceIn = function (template) { return ph.resolveIn('*', str(template)); };

  // ---------------------------------------------------------------------
  // chai, with Postman's assertions for requests and responses
  // ---------------------------------------------------------------------

  var chaiInstance = null;
  function chai() {
    if (chaiInstance) return chaiInstance;
    chaiInstance = global.require('chai');
    chaiInstance.use(postmanChai);
    return chaiInstance;
  }

  function postmanChai(chai, utils) {
    var Assertion = chai.Assertion;
    function isResponse(o) { return o && o._postman_type === 'response'; }
    function isRequest(o) { return o instanceof Request; }
    function statusClass(name, test, label) {
      Assertion.addProperty(name, function () {
        var o = this._obj;
        this.assert(test(o.code), 'expected response code to be ' + label + ' but found #{act}', 'expected response code to not be ' + label + ' but found #{act}', label, o.code);
      });
    }
    var withResponse = function (name, fn) {
      Assertion.overwriteProperty(name, function (_super) {
        return function () { if (isResponse(this._obj)) fn.call(this, this._obj); else _super.call(this); };
      });
    };
    withResponse('ok', function (r) {
      this.assert(r.code === 200, 'expected response to have status code 200 but got #{act}', 'expected response to not have status code 200', 200, r.code);
    });
    statusClass('info', function (c) { return c >= 100 && c < 200; }, '1XX');
    statusClass('success', function (c) { return c >= 200 && c < 300; }, '2XX');
    statusClass('redirection', function (c) { return c >= 300 && c < 400; }, '3XX');
    statusClass('clientError', function (c) { return c >= 400 && c < 500; }, '4XX');
    statusClass('serverError', function (c) { return c >= 500; }, '5XX');
    statusClass('error', function (c) { return c >= 400; }, '4XX or 5XX');
    var named = { accepted: 202, badRequest: 400, unauthorized: 401, unauthorised: 401, forbidden: 403, notFound: 404, notAcceptable: 406, rateLimited: 429 };
    Object.keys(named).forEach(function (n) {
      Assertion.addProperty(n, function () {
        var c = this._obj.code;
        this.assert(c === named[n], 'expected response code to be ' + named[n] + ' but found #{act}', 'expected response code to not be ' + named[n], named[n], c);
      });
    });
    Assertion.addMethod('status', function (want) {
      var r = this._obj;
      if (typeof want === 'number') {
        this.assert(r.code === want, 'expected response to have status code #{exp} but got #{act}', 'expected response to not have status code #{act}', want, r.code);
      } else {
        this.assert(r.status === want, 'expected response to have status reason #{exp} but got #{act}', 'expected response to not have status reason #{act}', want, r.status);
      }
    });
    Assertion.addMethod('statusCode', function (want) {
      this.assert(this._obj.code === want, 'expected response to have status code #{exp} but got #{act}', 'expected response to not have status code #{act}', want, this._obj.code);
    });
    Assertion.addMethod('statusCodeClass', function (cls) {
      var c = Math.floor(this._obj.code / 100);
      this.assert(c === cls, 'expected response code to be #{exp}XX but found #{act}', 'expected response code to not be #{exp}XX', cls, this._obj.code);
    });
    Assertion.addMethod('header', function (key, value) {
      var o = this._obj;
      var list = o.headers;
      var has = list.has(key);
      if (arguments.length < 2) {
        this.assert(has, 'expected response to have header with key \'' + key + '\'', 'expected response to not have header with key \'' + key + '\'', true, has);
        return;
      }
      var got = list.get(key);
      this.assert(got === value, 'expected \'' + key + '\' header value to be #{exp} but got #{act}', 'expected \'' + key + '\' header value to not be #{exp}', value, got);
    });
    Assertion.addMethod('body', function (want) {
      var o = this._obj;
      var text = isResponse(o) ? o.text() : (o.body ? o.body.toString() : '');
      if (arguments.length === 0) {
        this.assert(text.length > 0, 'expected response to have content in body', 'expected response to not have content in body');
      } else if (want instanceof RegExp) {
        this.assert(want.test(text), 'expected response body text to match #{exp} but got #{act}', 'expected response body text to not match #{exp}', want, text);
      } else if (typeof want === 'string') {
        this.assert(text === want, 'expected response body to equal #{exp} but got #{act}', 'expected response body to not equal #{exp}', want, text);
      } else {
        var json;
        try { json = JSON.parse(text); } catch (e) { json = undefined; }
        this.assert(utils.eql(json, want), 'expected response body json to equal #{exp} but got #{act}', 'expected response body json to not equal #{exp}', want, json);
      }
    });
    Assertion.addProperty('withBody', function () {
      var text = this._obj.text();
      this.assert(text.length > 0, 'expected response to have content in body', 'expected response to not have content in body');
    });
    Assertion.addMethod('jsonBody', function (path, value) {
      var json;
      try { json = this._obj.json(); } catch (e) { json = undefined; }
      if (arguments.length === 0) {
        this.assert(json !== undefined, 'expected response body to be a valid json but got error #{act}', 'expected response body to not be a valid json', true, json);
        return;
      }
      if (isObject(path)) {
        this.assert(utils.eql(json, path), 'expected response body json to equal #{exp} but got #{act}', 'expected response body json to not equal #{exp}', path, json);
        return;
      }
      var _ = global.require('lodash');
      var has = _.has(json, path);
      if (arguments.length < 2) {
        this.assert(has, 'expected #{act} in response to contain property #{exp}', 'expected #{act} in response to not contain property #{exp}', path, json);
        return;
      }
      var got = _.get(json, path);
      this.assert(utils.eql(got, value), 'expected json property \'' + path + '\' to be #{exp} but got #{act}', 'expected json property \'' + path + '\' to not be #{exp}', value, got);
    });
    Assertion.addMethod('jsonSchema', function (schema, options) {
      var Ajv = global.require('ajv');
      var ajv = new Ajv(Object.assign({ allErrors: true, logger: false }, options || {}));
      var json;
      try { json = this._obj.json(); } catch (e) { json = undefined; }
      var valid = ajv.validate(schema, json);
      this.assert(valid, 'expected data to satisfy schema but found following errors: \n' + ajv.errorsText(), 'expected data to not satisfy schema', true, valid);
    });
    Assertion.addMethod('responseTime', function (ms) {
      this.assert(this._obj.responseTime <= ms, 'expected response time to be at most #{exp}ms but was #{act}ms', 'expected response time to be over #{exp}ms', ms, this._obj.responseTime);
    });
    Assertion.addMethod('cookie', function (name, value) {
      var c = this._obj.cookies;
      if (arguments.length < 2) {
        this.assert(c.has(name), 'expected response to have cookie \'' + name + '\'', 'expected response to not have cookie \'' + name + '\'');
        return;
      }
      this.assert(c.get(name) === value, 'expected cookie \'' + name + '\' to have value #{exp} but got #{act}', 'expected cookie \'' + name + '\' to not have value #{exp}', value, c.get(name));
    });
    function mediaType(name, re) {
      withResponse(name, function (r) {
        var ct = r.headers.get('Content-Type') || '';
        this.assert(re.test(ct), 'expected response to be ' + name + ' but got #{act}', 'expected response to not be ' + name, name, ct);
      });
    }
    mediaType('json', /json/i);
    mediaType('html', /html/i);
    mediaType('xml', /xml/i);
    Assertion.addMethod('url', function (want) {
      var got = this._obj.url.toString();
      this.assert(got === want, 'expected request to have url #{exp} but got #{act}', 'expected request to not have url #{exp}', want, got);
    });
    Assertion.addMethod('method', function (want) {
      var got = this._obj.method;
      this.assert(String(got).toUpperCase() === String(want).toUpperCase(), 'expected request to have method #{exp} but got #{act}', 'expected request to not have method #{exp}', want, got);
    });
    void isRequest;
  }

  function addTo(obj) {
    Object.defineProperty(obj, 'to', { get: function () { return chai().expect(obj).to; }, configurable: true });
  }

  // ---------------------------------------------------------------------
  // Tests
  // ---------------------------------------------------------------------

  function errorText(e) {
    if (e && e.name && e.message) return e.name + ': ' + e.message;
    return String(e);
  }

  function test(name, fn) {
    name = str(name);
    if (typeof fn !== 'function') {
      ph.result(name, false, true, '');
      return pm;
    }
    try {
      if (fn.length > 0) {
        // fn(done): the test ends when done is called.
        var id = ph.hold('test ' + name);
        var finished = false;
        fn(function (err) {
          if (finished) return;
          finished = true;
          ph.release(id);
          ph.result(name, !err, false, err ? errorText(err) : '');
        });
        return pm;
      }
      var r = fn();
      if (r && typeof r.then === 'function') {
        var hid = ph.hold('test ' + name);
        r.then(function () { ph.release(hid); ph.result(name, true, false, ''); },
          function (e) { ph.release(hid); ph.result(name, false, false, errorText(e)); });
        return pm;
      }
      ph.result(name, true, false, '');
    } catch (e) {
      ph.result(name, false, false, errorText(e));
    }
    return pm;
  }
  test.skip = function (name) { ph.result(str(name), false, true, ''); return pm; };

  // ---------------------------------------------------------------------
  // pm.sendRequest
  // ---------------------------------------------------------------------

  function toRequestJSON(req) {
    if (typeof req === 'string') return { method: 'GET', url: { raw: req } };
    if (req instanceof Request) return req.toJSON();
    var r = new Request({ url: req.url, method: req.method, header: req.header || req.headers, body: req.body, auth: req.auth });
    var out = r.toJSON();
    if (req.timeout) out.timeout = req.timeout;
    return out;
  }

  // sendRequest calls back, or returns a promise without a callback. The
  // callback is called straight from posthero, so what it throws fails the
  // script instead of getting lost in a promise.
  function sendRequest(req, cb) {
    var payload = JSON.stringify(toRequestJSON(req));
    if (typeof cb === 'function') {
      ph.send(payload, function (err, res) {
        if (err) cb(new Error(err), null);
        else cb(null, new Response(res));
      });
      return undefined;
    }
    return new Promise(function (resolve, reject) {
      ph.send(payload, function (err, res) {
        if (err) reject(new Error(err));
        else resolve(new Response(res));
      });
    });
  }

  // ---------------------------------------------------------------------
  // Cookies
  // ---------------------------------------------------------------------

  function CookieJar() {}
  function jarCall(op, url, a, b, cb) {
    try {
      var out = ph.jar(op, str(url), a, b);
      if (typeof cb === 'function') cb(null, out);
      return out;
    } catch (e) {
      if (typeof cb === 'function') cb(e);
      else throw e;
    }
  }
  CookieJar.prototype.get = function (url, name, cb) { return jarCall('get', url, name, null, cb); };
  CookieJar.prototype.getAll = function (url, opts, cb) {
    if (typeof opts === 'function') { cb = opts; opts = null; }
    return jarCall('getAll', url, null, null, cb);
  };
  CookieJar.prototype.set = function (url, name, value, cb) {
    if (isObject(name)) { cb = value; return jarCall('set', url, name.name || name.key, JSON.stringify(name), cb); }
    if (typeof value === 'function') { cb = value; value = ''; }
    return jarCall('set', url, name, JSON.stringify({ value: str(value) }), cb);
  };
  CookieJar.prototype.unset = function (url, name, cb) { return jarCall('unset', url, name, null, cb); };
  CookieJar.prototype.clear = function (url, cb) { return jarCall('clear', url, null, null, cb); };

  // ---------------------------------------------------------------------
  // pm
  // ---------------------------------------------------------------------

  var info = ph.info;
  var request = new Request(ph.request);
  addTo(request);
  // What the request was before the script, to tell whether it changed it.
  var requestBefore = JSON.stringify(request.toJSON());
  var response = ph.response ? new Response(ph.response) : undefined;
  if (response) addTo(response);

  var location = info.location.slice();
  location.current = info.current;

  var pm = {
    info: {
      eventName: info.eventName, iteration: info.iteration, iterationCount: info.iterationCount,
      requestName: info.requestName, requestId: info.requestId,
    },
    variables: new Variables(),
    environment: new VariableScope('environment'),
    collectionVariables: new VariableScope('collection'),
    globals: new VariableScope('global'),
    iterationData: new VariableScope('data', true),
    vault: {
      get: function (key) { return Promise.resolve(ph.vault('get', str(key), '')); },
      set: function (key, value) { ph.vault('set', str(key), str(value)); return Promise.resolve(); },
      unset: function (key) { ph.vault('unset', str(key), ''); return Promise.resolve(); },
    },
    request: request,
    response: response,
    test: test,
    sendRequest: sendRequest,
    cookies: (function () {
      var list = new PropertyList(Cookie, response ? response.cookies.all() : (ph.cookies || []).map(function (c) { return new Cookie(c); }));
      list.jar = function () { return new CookieJar(); };
      return list;
    })(),
    execution: {
      skipRequest: function () { ph.skip(); },
      setNextRequest: function (next) { ph.next(next === null || next === undefined ? null : str(next)); },
      location: location,
    },
    visualizer: {
      set: function (template, data, options) { ph.visualize(str(template), JSON.stringify(data === undefined ? {} : data), JSON.stringify(options || {})); },
      clear: function () { ph.visualize('', '{}', '{}'); },
    },
    require: function (name) { throw new Error('pm.require("' + name + '"): packages from the Postman package library aren\'t available in posthero'); },
  };
  // pm.expect is chai's expect itself, so expect.fail and the like are
  // there; chai loads the first time a script uses it.
  Object.defineProperty(pm, 'expect', { get: function () { return chai().expect; }, enumerable: true, configurable: true });
  pm.iterationData.toObject = function () { return ph.all('data'); };
  pm.environment.unset = function (key) { ph.unset('environment', String(key)); };

  // ---------------------------------------------------------------------
  // The legacy sandbox: postman, tests, responseBody and friends
  // ---------------------------------------------------------------------

  var tests = {};
  var postman = {
    setEnvironmentVariable: function (k, v) { pm.environment.set(k, str(v)); },
    getEnvironmentVariable: function (k) { return pm.environment.get(k); },
    clearEnvironmentVariable: function (k) { pm.environment.unset(k); },
    clearEnvironmentVariables: function () { pm.environment.clear(); },
    setGlobalVariable: function (k, v) { pm.globals.set(k, str(v)); },
    getGlobalVariable: function (k) { return pm.globals.get(k); },
    clearGlobalVariable: function (k) { pm.globals.unset(k); },
    clearGlobalVariables: function () { pm.globals.clear(); },
    getResponseHeader: function (k) { return response ? response.headers.get(k) : undefined; },
    getResponseCookie: function (k) {
      var c = response ? response.cookies.one(k) : undefined;
      return c ? c.toJSON() : undefined;
    },
    setNextRequest: function (n) { pm.execution.setNextRequest(n); },
  };

  global.pm = pm;
  global.postman = postman;
  global.tests = tests;
  global.environment = ph.all('environment');
  global.globals = ph.all('global');
  global.data = ph.all('data');
  global.iteration = info.iteration;
  global.request = {
    id: info.requestId, name: info.requestName, url: request.url.toString(), method: request.method,
    headers: request.getHeaders({ enabled: true }),
    data: (function () {
      var b = request.body;
      if (!b) return {};
      if (b.mode === 'urlencoded') return b.urlencoded.toObject(true);
      if (b.mode === 'formdata') return b.formdata.toObject(true);
      if (b.mode === 'raw') return b.raw;
      if (b.mode === 'graphql') return b.graphql;
      return {};
    })(),
  };
  if (response) {
    global.responseBody = response.text();
    global.responseCode = { code: response.code, name: response.status, detail: response.status };
    global.responseHeaders = response.headers.toObject();
    global.responseTime = response.responseTime;
    global.responseCookies = response.cookies.all().map(function (c) { return c.toJSON(); });
  }
  // xml2Json is xml2js with the options Postman's sandbox gives it.
  global.xml2Json = function (xml) {
    var out, failed = null;
    global.require('xml2js').parseString(str(xml), { explicitArray: false, trim: true, mergeAttrs: false }, function (err, res) { failed = err; out = res; });
    if (failed) throw failed;
    return out;
  };
  global.atob = function (s) { return ph.atob(str(s)); };
  global.btoa = function (s) { return ph.btoa(str(s)); };
  global.structuredClone = global.structuredClone || function (v) { return v === undefined ? v : JSON.parse(JSON.stringify(v)); };
  Object.defineProperty(global, '_', { get: function () { return global.require('lodash'); }, configurable: true });
  Object.defineProperty(global, 'CryptoJS', { get: function () { return global.require('crypto-js'); }, configurable: true });
  Object.defineProperty(global, 'tv4', { get: function () { return global.require('tv4'); }, configurable: true });

  // The SDK, for scripts that require('postman-collection').
  global.__postmanCollection = {
    Url: Url, Request: Request, Response: Response, Header: Header, QueryParam: QueryParam, Cookie: Cookie,
    PropertyList: PropertyList, Property: Property, RequestBody: RequestBody, RequestAuth: RequestAuth,
    VariableScope: VariableScope,
  };

  // After the script: the tests[] it set, and what it made of the request,
  // or null when it left the request alone, so that goes out as written.
  global.__ph_finish = function () {
    for (var name in tests) {
      if (Object.prototype.hasOwnProperty.call(tests, name)) ph.result(name, !!tests[name], false, tests[name] ? '' : 'tests["' + name + '"] is false');
    }
    var after = JSON.stringify(request.toJSON());
    return after === requestBefore ? null : after;
  };
})(this);

package script

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/dop251/goja"

	"github.com/shinyvision/posthero/internal/jsonx"
	"github.com/shinyvision/posthero/internal/postman"
	"github.com/shinyvision/posthero/internal/send"
	"github.com/shinyvision/posthero/internal/vars"
)

// scopeKinds maps the sandbox's scope names to the variable scopes.
var scopeKinds = map[string]string{
	"local": vars.Local, "data": vars.Data, "environment": vars.Environment, "collection": vars.Collection, "global": vars.Global,
}

// bridge makes __ph, what the sandbox uses to reach posthero, for a script
// of an event.
func (s *Session) bridge(event string) error {
	rt := s.rt
	env := s.env
	ph := rt.NewObject()
	set := func(name string, v any) { _ = ph.Set(name, v) }

	info := env.Info
	location := info.Location
	if location == nil {
		location = []string{}
	}
	set("info", map[string]any{
		"eventName": event, "iteration": info.Iteration, "iterationCount": max(info.IterationCount, 1),
		"requestName": info.RequestName, "requestId": info.RequestID, "location": location, "current": event,
	})
	if event == "test" && env.Sent != nil {
		set("request", sentJSON(env.Sent, env.Request, info))
	} else {
		set("request", requestJSON(env.Request, info))
	}
	if event == "test" && env.Response != nil {
		set("response", responseJSON(rt, env.Response))
	} else {
		set("response", nil)
	}

	scope := func(kind string) *vars.Scope {
		if env.Vars == nil {
			return nil
		}
		return env.Vars.Scope(scopeKinds[kind])
	}
	value := func(v vars.Var) goja.Value {
		if v.Typed != nil {
			return rt.ToValue(v.Typed)
		}
		return rt.ToValue(v.Value)
	}
	set("get", func(kind, key string) goja.Value {
		if kind == "*" {
			if env.Vars == nil {
				return goja.Undefined()
			}
			if v, _, ok := env.Vars.Lookup(key); ok {
				return value(v)
			}
			return goja.Undefined()
		}
		if v, ok := scope(kind).Get(key); ok {
			return value(v)
		}
		return goja.Undefined()
	})
	set("has", func(kind, key string) bool {
		if kind == "*" {
			if env.Vars == nil {
				return false
			}
			_, _, ok := env.Vars.Lookup(key)
			return ok
		}
		_, ok := scope(kind).Get(key)
		return ok
	})
	set("set", func(kind, key string, v goja.Value) {
		sc := scope(kind)
		if sc == nil {
			if kind == "environment" {
				panic(rt.NewGoError(fmt.Errorf("pm.environment.set: no environment is selected")))
			}
			return
		}
		text, t := typed(v)
		sc.SetTyped(key, text, t)
		s.res.Changed[sc.Kind] = true
	})
	set("unset", func(kind, key string) {
		if sc := scope(kind); sc != nil {
			sc.Unset(key)
			s.res.Changed[sc.Kind] = true
		}
	})
	set("clear", func(kind string) {
		if sc := scope(kind); sc != nil {
			sc.Vars = nil
			s.res.Changed[sc.Kind] = true
		}
	})
	set("all", func(kind string) goja.Value {
		obj := rt.NewObject()
		if env.Vars == nil {
			return obj
		}
		var scopes []*vars.Scope
		if kind == "*" {
			// The winning value of each name: from the lowest scope up.
			for i := len(env.Vars.Scopes) - 1; i >= 0; i-- {
				scopes = append(scopes, env.Vars.Scopes[i])
			}
		} else if sc := scope(kind); sc != nil {
			scopes = []*vars.Scope{sc}
		}
		for _, sc := range scopes {
			for _, v := range sc.Vars {
				_ = obj.Set(v.Key, value(v))
			}
		}
		return obj
	})
	set("scopeName", func(kind string) string {
		if sc := scope(kind); sc != nil {
			return sc.Name
		}
		return ""
	})
	set("resolveIn", func(kind, text string) string {
		if env.Vars == nil {
			return text
		}
		if kind == "*" {
			return env.Vars.Resolve(text)
		}
		one := vars.New(scope(kind))
		return one.Resolve(text)
	})

	set("result", func(name string, passed, skipped bool, errText string) {
		s.res.Tests = append(s.res.Tests, Test{Name: name, Passed: passed, Skipped: skipped, Error: errText})
	})
	set("hold", func(string) int { return s.hold() })
	set("release", func(id int) { s.release(id) })
	set("skip", func() {
		if event != "prerequest" {
			panic(rt.NewGoError(fmt.Errorf("pm.execution.skipRequest() works only in pre-request scripts")))
		}
		s.res.Skip = true
	})
	set("next", func(v goja.Value) {
		n := ""
		if !goja.IsNull(v) && !goja.IsUndefined(v) {
			n = v.String()
		}
		s.res.Next = &n
	})
	set("visualize", func(tpl, data, opts string) {
		if tpl == "" {
			s.res.Visual = nil
			return
		}
		s.res.Visual = &Visual{Template: tpl, Data: data, Options: opts}
	})
	set("vault", func(op, key, val string) goja.Value {
		if env.Vault == nil {
			panic(rt.NewGoError(fmt.Errorf("pm.vault isn't available here")))
		}
		switch op {
		case "get":
			if v, ok := env.Vault.Get(key); ok {
				return rt.ToValue(v)
			}
			return goja.Undefined()
		case "set":
			if err := env.Vault.Set(key, val); err != nil {
				panic(rt.NewGoError(err))
			}
		case "unset":
			if err := env.Vault.Unset(key); err != nil {
				panic(rt.NewGoError(err))
			}
		}
		return goja.Undefined()
	})
	// send calls back later, failed or not, as Postman does; what the
	// callback throws fails the script (see safely).
	set("send", func(payload string, cb goja.Value) {
		fn, ok := goja.AssertFunction(cb)
		if !ok {
			return
		}
		id := s.hold()
		go func() {
			resp, err := s.sendRequest(payload)
			s.later(func() {
				if !s.held[id] {
					return // the script that asked has ended
				}
				s.release(id)
				var cerr error
				if err != nil {
					_, cerr = fn(goja.Undefined(), rt.ToValue(err.Error()))
				} else {
					_, cerr = fn(goja.Undefined(), goja.Null(), rt.ToValue(responseJSON(rt, resp)))
				}
				if cerr != nil {
					panic(cerr)
				}
			})
		}()
	})
	set("jar", func(op, rawURL string, name, val goja.Value) goja.Value {
		return s.jar(op, rawURL, name, val)
	})
	set("xmltree", func(text string) string {
		n, err := xmlTree(text)
		if err != nil {
			panic(rt.NewGoError(fmt.Errorf("the XML isn't valid: %w", err)))
		}
		data, _ := jsonx.Marshal(n)
		return string(data)
	})
	set("atob", func(text string) string {
		data, err := base64.StdEncoding.DecodeString(strings.TrimRight(text, "=") + strings.Repeat("=", (4-len(strings.TrimRight(text, "="))%4)%4))
		if err != nil {
			panic(rt.NewTypeError("atob: the string isn't valid base64"))
		}
		return string(data)
	})
	set("btoa", func(text string) string { return b64(text) })
	set("base64", func(v goja.Value) string {
		if ab, ok := v.Export().(goja.ArrayBuffer); ok {
			return base64.StdEncoding.EncodeToString(ab.Bytes())
		}
		return b64(v.String())
	})
	var cookies []map[string]any
	if env.Jar != nil && env.Request != nil {
		if u, err := url.Parse(env.Vars.Resolve(env.Request.URL.String())); err == nil {
			for _, c := range env.Jar.Cookies(u) {
				cookies = append(cookies, map[string]any{"name": c.Name, "key": c.Name, "value": c.Value})
			}
		}
	}
	set("cookies", cookies)
	rt.Set("__ph", ph)
	return nil
}

// sendRequest sends what pm.sendRequest was given.
func (s *Session) sendRequest(payload string) (*send.Response, error) {
	if s.env.Send == nil {
		return nil, errors.New("pm.sendRequest isn't available here")
	}
	req, err := sendRequestFrom(payload)
	if err != nil {
		return nil, err
	}
	return s.env.Send(s.ctx, req)
}

// sendRequestFrom reads pm.sendRequest's argument.
func sendRequestFrom(payload string) (*send.Request, error) {
	var in struct {
		Method string `json:"method"`
		URL    struct {
			Raw   string         `json:"raw"`
			Query postman.Params `json:"query"`
		} `json:"url"`
		Header  postman.Params `json:"header"`
		Body    *postman.Body  `json:"body"`
		Auth    *postman.Auth  `json:"auth"`
		Timeout int            `json:"timeout"`
	}
	if err := json.Unmarshal([]byte(payload), &in); err != nil {
		return nil, err
	}
	if in.URL.Raw == "" {
		return nil, fmt.Errorf("pm.sendRequest needs a URL")
	}
	r := &send.Request{Method: strings.ToUpper(in.Method), Header: in.Header, Body: in.Body, Auth: in.Auth, Settings: send.DefaultSettings()}
	if r.Method == "" {
		r.Method = http.MethodGet
	}
	if in.Body != nil && in.Body.Mode == "" {
		r.Body = nil
	}
	r.URL = postman.Rebuild(nil, in.URL.Raw, nil, nil)
	if in.Timeout > 0 {
		r.Settings.Timeout = time.Duration(in.Timeout) * time.Millisecond
	}
	return r, nil
}

// jar runs pm.cookies.jar() operations.
func (s *Session) jar(op, rawURL string, name, val goja.Value) goja.Value {
	rt := s.rt
	j := s.env.Jar
	if j == nil {
		panic(rt.NewGoError(fmt.Errorf("there is no cookie jar")))
	}
	if !strings.Contains(rawURL, "://") {
		rawURL = "http://" + rawURL
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		panic(rt.NewGoError(fmt.Errorf("pm.cookies.jar(): %q isn't a URL", rawURL)))
	}
	switch op {
	case "get":
		for _, c := range j.Cookies(u) {
			if c.Name == name.String() {
				return rt.ToValue(c.Value)
			}
		}
		return goja.Undefined()
	case "getAll":
		var out []map[string]any
		for _, c := range j.Cookies(u) {
			out = append(out, map[string]any{"name": c.Name, "value": c.Value})
		}
		return rt.ToValue(out)
	case "set":
		var c send.Cookie
		_ = json.Unmarshal([]byte(val.String()), &c)
		c.Name = name.String()
		j.Set(u.Host, c)
		return goja.Undefined()
	case "unset":
		j.Delete(u.Host, name.String())
	case "clear":
		j.Delete(u.Host, "")
	}
	return goja.Undefined()
}

// relativeURLs makes new URL(relative, base) resolve the way browsers,
// Node and Postman's sandbox do. goja_nodejs's URL takes the relative part
// as a path from the root: new URL('three', 'http://x/one/two') was
// http://x/three, not http://x/one/three. A base it can't read is left to
// the URL itself to complain about.
func relativeURLs(rt *goja.Runtime) error {
	rt.Set("__ph_resolveURL", func(ref, base string) goja.Value {
		b, err := url.Parse(strings.TrimSpace(base))
		if err != nil || !b.IsAbs() {
			return goja.Null()
		}
		r, err := url.Parse(strings.TrimSpace(ref))
		if err != nil {
			return goja.Null()
		}
		return rt.ToValue(b.ResolveReference(r).String())
	})
	_, err := rt.RunString(`(function () {
  var Orig = globalThis.URL, resolve = __ph_resolveURL;
  function URL(input, base) {
    if (!new.target) throw new TypeError("Class constructor URL cannot be invoked without 'new'");
    if (base !== undefined) {
      var abs = resolve(String(input), String(base && base.href !== undefined ? base.href : base));
      if (abs !== null) return new Orig(abs);
      return new Orig(input, base);
    }
    return new Orig(input);
  }
  URL.prototype = Orig.prototype;
  Object.setPrototypeOf(URL, Orig);
  globalThis.URL = URL;
  require('url').URL = URL;
  delete globalThis.__ph_resolveURL;
})();`)
	return err
}

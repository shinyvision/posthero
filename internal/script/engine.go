// Package script runs Postman's pre-request and test scripts: JavaScript
// with Postman's pm API, chai, lodash, crypto-js, moment and the other
// libraries Postman's sandbox has.
package script

import (
	"context"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/dop251/goja"
	"github.com/dop251/goja/file"
	"github.com/dop251/goja/parser"
	"github.com/dop251/goja_nodejs/buffer"
	"github.com/dop251/goja_nodejs/require"
	"github.com/dop251/goja_nodejs/url"

	"github.com/shinyvision/posthero/internal/jsonx"
	"github.com/shinyvision/posthero/internal/postman"
	"github.com/shinyvision/posthero/internal/send"
	"github.com/shinyvision/posthero/internal/vars"
)

//go:embed js/*.js
var jsFiles embed.FS

// Info is what pm.info says.
type Info struct {
	// EventName is "prerequest" or "test".
	EventName      string
	Iteration      int
	IterationCount int
	RequestName    string
	RequestID      string
	// Location is the collection, folders and request's names.
	Location []string
}

// Vault keeps secrets scripts reach through pm.vault.
type Vault interface {
	Get(key string) (string, bool)
	Set(key, value string) error
	Unset(key string) error
}

// Env is what scripts of one request can see and change.
type Env struct {
	Vars *vars.Set
	Info Info
	// Request is the request before it is sent; pre-request scripts change
	// it.
	Request *send.Request
	// Sent and Response are what was sent and what came back, for tests.
	Sent     *send.Prepared
	Response *send.Response
	Jar      *send.Jar
	// Send sends a request for pm.sendRequest.
	Send  func(ctx context.Context, r *send.Request) (*send.Response, error)
	Vault Vault
	// Timeout ends a script that runs longer (0: a minute).
	Timeout time.Duration
}

// Test is the outcome of one pm.test.
type Test struct {
	Name    string
	Passed  bool
	Skipped bool
	Error   string
}

// Log is a line of console output.
type Log struct {
	Level string
	Text  string
	At    time.Time
}

// Visual is what pm.visualizer.set asked to show.
type Visual struct {
	Template, Data, Options string
}

// Result is what scripts did.
type Result struct {
	Tests []Test
	Logs  []Log
	// Skip says pm.execution.skipRequest was called.
	Skip bool
	// Next is set by setNextRequest: a request name or id, or "" to stop.
	Next *string
	// Changed lists the variable scopes scripts changed.
	Changed map[string]bool
	Visual  *Visual
	// Errors are scripts that failed, with why.
	Errors []error
}

// Passed counts the tests that passed, and Failed those that failed.
func (r *Result) Passed() int { return r.count(func(t Test) bool { return t.Passed }) }

// Failed counts the tests that failed.
func (r *Result) Failed() int { return r.count(func(t Test) bool { return !t.Passed && !t.Skipped }) }

func (r *Result) count(f func(Test) bool) int {
	n := 0
	for _, t := range r.Tests {
		if f(t) {
			n++
		}
	}
	return n
}

// Engine compiles the libraries once, for every run.
type Engine struct {
	once     sync.Once
	prelude  *goja.Program
	registry *require.Registry
	libs     map[string]*goja.Program
	err      error
}

// NewEngine returns an engine.
func NewEngine() *Engine { return &Engine{} }

var libFiles = map[string]string{
	"chai": "chai.js", "lodash": "lodash.js", "crypto-js": "crypto-js.js", "moment": "moment.js", "tv4": "tv4.js", "ajv": "ajv.js",
}

func (e *Engine) init() error {
	e.once.Do(func() {
		src, err := jsFiles.ReadFile("js/postman.js")
		if err != nil {
			e.err = err
			return
		}
		if e.prelude, err = goja.Compile("postman.js", string(src), true); err != nil {
			e.err = err
			return
		}
		e.libs = map[string]*goja.Program{}
		e.registry = require.NewRegistry()
		for name := range libFiles {
			name := name
			e.registry.RegisterNativeModule(name, func(rt *goja.Runtime, module *goja.Object) {
				prog, err := e.lib(name)
				if err != nil {
					panic(rt.NewGoError(err))
				}
				fn, err := rt.RunProgram(prog)
				if err != nil {
					panic(err)
				}
				call, _ := goja.AssertFunction(fn)
				exports := module.Get("exports")
				if _, err := call(goja.Undefined(), module, exports, rt.Get("require")); err != nil {
					panic(err)
				}
			})
		}
		registerModules(e.registry)
	})
	return e.err
}

var libMu sync.Mutex

// lib compiles a library the first time it is needed.
func (e *Engine) lib(name string) (*goja.Program, error) {
	libMu.Lock()
	defer libMu.Unlock()
	if p, ok := e.libs[name]; ok {
		return p, nil
	}
	src, err := jsFiles.ReadFile("js/" + libFiles[name])
	if err != nil {
		return nil, err
	}
	// A CommonJS wrapper; define is hidden so UMD builds take the module
	// path.
	code := "(function (module, exports, require) { var define = undefined; var window = undefined; var self = undefined;\n" + string(src) + "\n})"
	p, err := goja.Compile(libFiles[name], code, false)
	if err != nil {
		return nil, fmt.Errorf("loading %s: %w", name, err)
	}
	e.libs[name] = p
	return p, nil
}

// Session runs the scripts of one request (or one request of a run) in
// one JavaScript runtime, so libraries load once.
type Session struct {
	e      *Engine
	rt     *goja.Runtime
	env    *Env
	res    Result
	ctx    context.Context
	jobs   chan func()
	holds  int
	nextID int
	timers map[int]*time.Timer
	held   map[int]bool
}

// NewSession starts a runtime for env.
func (e *Engine) NewSession(ctx context.Context, env *Env) (*Session, error) {
	if err := e.init(); err != nil {
		return nil, err
	}
	rt := goja.New()
	rt.SetFieldNameMapper(goja.TagFieldNameMapper("json", true))
	e.registry.Enable(rt)
	buffer.Enable(rt)
	url.Enable(rt)
	if err := relativeURLs(rt); err != nil {
		return nil, err
	}
	s := &Session{e: e, rt: rt, env: env, ctx: ctx, jobs: make(chan func(), 64), timers: map[int]*time.Timer{}, held: map[int]bool{}}
	s.res.Changed = map[string]bool{}
	s.installTimers()
	s.installConsole()
	return s, nil
}

// Result returns what the scripts run so far did.
func (s *Session) Result() Result { return s.res }

// Run runs one script for an event ("prerequest" or "test"). A script
// that fails is recorded in the result, and the error returned.
func (s *Session) Run(code, event string, location []string) error {
	if strings.TrimSpace(code) == "" {
		return nil
	}
	// However the script ends, what it left pending ends with it.
	defer s.settle()
	s.env.Info.EventName = event
	if location != nil {
		s.env.Info.Location = location
	}
	if err := s.bridge(event); err != nil {
		return s.fail(err)
	}
	if _, err := s.rt.RunProgram(s.e.prelude); err != nil {
		return s.fail(fmt.Errorf("posthero's script sandbox: %w", err))
	}
	timeout := s.env.Timeout
	if timeout <= 0 {
		timeout = time.Minute
	}
	deadline := time.AfterFunc(timeout, func() { s.rt.Interrupt(fmt.Sprintf("the script ran longer than %s", timeout)) })
	defer deadline.Stop()
	stop := context.AfterFunc(s.ctx, func() { s.rt.Interrupt("cancelled") })
	defer stop()
	defer s.rt.ClearInterrupt()

	var scriptErr error
	done := false
	s.rt.Set("__ph_done", func(call goja.FunctionCall) goja.Value {
		done = true
		if errText := call.Argument(0); !goja.IsUndefined(errText) && !goja.IsNull(errText) {
			scriptErr = errors.New(cleanStack(errText.String()))
		}
		return goja.Undefined()
	})
	wrapped := "(async function () {\n" + code + "\n})().then(function () { __ph_done(); }, function (e) { __ph_done(e && e.stack ? String(e.stack) : (e && e.name ? e.name + ': ' + e.message : String(e))); });"
	prog, err := compileScript(event+"-script.js", wrapped)
	if err != nil {
		return s.fail(err)
	}
	if _, err := s.rt.RunProgram(prog); err != nil {
		return s.fail(describe(err))
	}
	// Wait for timers, requests and async tests.
	for s.holds > 0 || !done {
		if !done && s.holds == 0 {
			break // a promise nothing will ever settle
		}
		select {
		case job := <-s.jobs:
			if err := s.safely(job); err != nil {
				return s.fail(describe(err))
			}
		case <-s.ctx.Done():
			return s.fail(errors.New("cancelled"))
		case <-time.After(timeout):
			return s.fail(fmt.Errorf("the script waited longer than %s for something", timeout))
		}
	}
	s.clearTimers()
	if finish, ok := goja.AssertFunction(s.rt.Get("__ph_finish")); ok {
		v, err := finish(goja.Undefined())
		if err != nil {
			return s.fail(describe(err))
		}
		// null: the script left the request alone.
		if event == "prerequest" && s.env.Request != nil && !goja.IsNull(v) && !goja.IsUndefined(v) {
			if err := applyRequest(s.env.Request, v.String()); err != nil {
				return s.fail(fmt.Errorf("the script left the request unusable: %w", err))
			}
		}
	}
	if scriptErr != nil {
		return s.fail(scriptErr)
	}
	return nil
}

// compileScript compiles a wrapped script. A syntax error says where it is
// in the script as written, which starts a line after the wrapper's.
func compileScript(name, src string) (*goja.Program, error) {
	ast, err := parser.ParseFile(nil, name, src, 0, parser.WithDisableSourceMaps)
	if err != nil {
		var list parser.ErrorList
		if errors.As(err, &list) && len(list) > 0 {
			return nil, syntaxError(list[0].Message, list[0].Position)
		}
		return nil, err
	}
	prog, err := goja.CompileAST(ast, false)
	var syn *goja.CompilerSyntaxError
	if errors.As(err, &syn) && syn.File != nil {
		return nil, syntaxError(syn.Message, syn.File.Position(syn.Offset))
	}
	return prog, err
}

func syntaxError(msg string, at file.Position) error {
	return fmt.Errorf("SyntaxError: %s\n    at line %d, column %d", msg, at.Line-1, at.Column)
}

// settle ends what a script left pending: its timers, and the requests and
// tests it waited for. Their jobs are dropped, so none of it runs during the
// next script.
func (s *Session) settle() {
	s.clearTimers()
	s.holds = 0
	clear(s.held)
	for {
		select {
		case <-s.jobs:
		default:
			return
		}
	}
}

// safely runs a job on the runtime, turning a panic into an error.
func (s *Session) safely(job func()) (err error) {
	defer func() {
		if r := recover(); r != nil {
			if e, ok := r.(error); ok {
				err = e
			} else {
				err = fmt.Errorf("%v", r)
			}
		}
	}()
	job()
	return nil
}

func (s *Session) fail(err error) error {
	s.res.Errors = append(s.res.Errors, err)
	return err
}

// describe makes a script error readable: the message and where.
func describe(err error) error {
	var ex *goja.Exception
	if errors.As(err, &ex) {
		msg := ex.Value().String()
		if obj, ok := ex.Value().(*goja.Object); ok {
			if stack := obj.Get("stack"); stack != nil && !goja.IsUndefined(stack) {
				msg = stack.String()
			}
		}
		return errors.New(cleanStack(msg))
	}
	var intr *goja.InterruptedError
	if errors.As(err, &intr) {
		return fmt.Errorf("%v", intr.Value())
	}
	return err
}

// cleanStack keeps the first line of a stack and where in the script it
// happened, with the line number as the script has it.
func cleanStack(stack string) string {
	lines := strings.Split(stack, "\n")
	out := []string{lines[0]}
	for _, l := range lines[1:] {
		l = strings.TrimSpace(l)
		if strings.Contains(l, "-script.js:") {
			// at <anonymous> (test-script.js:5:3(12))
			i := strings.Index(l, "-script.js:")
			rest := l[i+len("-script.js:"):]
			var line, col int
			if _, err := fmt.Sscanf(rest, "%d:%d", &line, &col); err == nil {
				out = append(out, fmt.Sprintf("    at line %d, column %d", line-1, col))
			}
			break
		}
	}
	return strings.Join(out, "\n")
}

// hold keeps the loop waiting for something, until release.
func (s *Session) hold() int {
	s.nextID++
	s.holds++
	s.held[s.nextID] = true
	return s.nextID
}

func (s *Session) release(id int) {
	if s.held[id] {
		delete(s.held, id)
		s.holds--
	}
}

// later runs f on the runtime's loop, from another goroutine.
func (s *Session) later(f func()) {
	select {
	case s.jobs <- f:
	case <-s.ctx.Done():
	}
}

func (s *Session) installTimers() {
	set := func(repeat bool) func(goja.FunctionCall) goja.Value {
		return func(call goja.FunctionCall) goja.Value {
			fn, ok := goja.AssertFunction(call.Argument(0))
			if !ok {
				return goja.Undefined()
			}
			ms := call.Argument(1).ToInteger()
			if ms < 0 {
				ms = 0
			}
			args := call.Arguments
			if len(args) > 2 {
				args = args[2:]
			} else {
				args = nil
			}
			id := s.hold()
			var fire func()
			fire = func() {
				s.later(func() {
					if !s.held[id] {
						return // cleared
					}
					if !repeat {
						s.release(id)
						delete(s.timers, id)
					}
					if _, err := fn(goja.Undefined(), args...); err != nil {
						panic(err)
					}
					if repeat && s.held[id] {
						s.timers[id] = time.AfterFunc(time.Duration(ms)*time.Millisecond, fire)
					}
				})
			}
			s.timers[id] = time.AfterFunc(time.Duration(ms)*time.Millisecond, fire)
			return s.rt.ToValue(id)
		}
	}
	clear := func(call goja.FunctionCall) goja.Value {
		id := int(call.Argument(0).ToInteger())
		if t, ok := s.timers[id]; ok {
			t.Stop()
			delete(s.timers, id)
		}
		s.release(id)
		return goja.Undefined()
	}
	s.rt.Set("setTimeout", set(false))
	s.rt.Set("setInterval", set(true))
	s.rt.Set("setImmediate", func(call goja.FunctionCall) goja.Value {
		return set(false)(goja.FunctionCall{This: call.This, Arguments: append([]goja.Value{call.Argument(0), s.rt.ToValue(0)}, call.Arguments[min(1, len(call.Arguments)):]...)})
	})
	s.rt.Set("clearTimeout", clear)
	s.rt.Set("clearInterval", clear)
	s.rt.Set("clearImmediate", clear)
}

func (s *Session) clearTimers() {
	for id, t := range s.timers {
		t.Stop()
		delete(s.timers, id)
		s.release(id)
	}
}

// installConsole makes console.* write to the result's log.
func (s *Session) installConsole() {
	console := s.rt.NewObject()
	for _, level := range []string{"log", "info", "warn", "error", "debug", "trace"} {
		level := level
		_ = console.Set(level, func(call goja.FunctionCall) goja.Value {
			parts := make([]string, len(call.Arguments))
			for i, a := range call.Arguments {
				parts[i] = s.format(a)
			}
			lv := level
			if lv == "trace" {
				lv = "debug"
			}
			s.res.Logs = append(s.res.Logs, Log{Level: lv, Text: strings.Join(parts, " "), At: time.Now()})
			return goja.Undefined()
		})
	}
	_ = console.Set("clear", func(goja.FunctionCall) goja.Value { return goja.Undefined() })
	s.rt.Set("console", console)
}

// format writes a value the way Postman's console shows it: text as it is,
// everything else as JSON.
func (s *Session) format(v goja.Value) string {
	switch {
	case goja.IsUndefined(v):
		return "undefined"
	case goja.IsNull(v):
		return "null"
	}
	if _, isStr := v.Export().(string); isStr {
		return v.String()
	}
	if obj, ok := v.(*goja.Object); ok {
		if obj.ClassName() == "Error" {
			if st := obj.Get("stack"); st != nil && !goja.IsUndefined(st) {
				return cleanStack(st.String())
			}
			return v.String()
		}
		if _, isFn := goja.AssertFunction(v); isFn {
			return "[Function]"
		}
		stringify, _ := goja.AssertFunction(s.rt.Get("JSON").ToObject(s.rt).Get("stringify"))
		out, err := stringify(goja.Undefined(), v, goja.Null(), s.rt.ToValue(2))
		if err == nil && !goja.IsUndefined(out) {
			return out.String()
		}
	}
	return v.String()
}

// typed is a script's value as a variable keeps it: the text, and the value
// itself when it isn't text.
func typed(v goja.Value) (string, any) {
	if goja.IsUndefined(v) || goja.IsNull(v) {
		return "", nil
	}
	x := v.Export()
	switch t := x.(type) {
	case string:
		return t, nil
	case bool, int64, float64, int:
		return v.String(), x
	}
	data, err := jsonx.Marshal(x)
	if err != nil {
		return v.String(), nil
	}
	return string(data), x
}

func validUTF8(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	return strings.ToValidUTF8(string(b), "�")
}

// requestJSON is a request as the sandbox reads it.
func requestJSON(r *send.Request, info Info) map[string]any {
	out := map[string]any{"id": info.RequestID, "name": info.RequestName}
	if r == nil {
		return out
	}
	out["method"] = r.Method
	u := map[string]any{"raw": r.URL.String()}
	if len(r.URL.Query) > 0 {
		u["query"] = toAny(r.URL.Query)
	}
	if len(r.URL.Variable) > 0 {
		u["variable"] = toAny(r.URL.Variable)
	}
	out["url"] = u
	out["header"] = toAny(r.Header)
	if r.Body != nil {
		out["body"] = toAny(r.Body)
	}
	if r.Auth != nil {
		out["auth"] = toAny(r.Auth)
	}
	return out
}

// sentJSON is a sent request as test scripts read it.
func sentJSON(p *send.Prepared, r *send.Request, info Info) map[string]any {
	out := requestJSON(r, info)
	if p == nil {
		return out
	}
	out["method"] = p.Method
	out["url"] = map[string]any{"raw": p.URL}
	var headers []map[string]any
	for _, h := range p.Header {
		headers = append(headers, map[string]any{"key": h.Name, "value": h.Value})
	}
	out["header"] = headers
	return out
}

func toAny(v any) any {
	data, _ := jsonx.Marshal(v)
	var out any
	_ = json.Unmarshal(data, &out)
	return out
}

// responseJSON is a response as the sandbox reads it.
func responseJSON(rt *goja.Runtime, r *send.Response) map[string]any {
	var headers []map[string]any
	for _, h := range r.Header {
		headers = append(headers, map[string]any{"key": h.Name, "value": h.Value})
	}
	var cookies []map[string]any
	for _, c := range r.Cookies {
		ck := map[string]any{"name": c.Name, "key": c.Name, "value": c.Value, "domain": c.Domain, "path": c.Path, "httpOnly": c.HttpOnly, "secure": c.Secure}
		if !c.Expires.IsZero() {
			ck["expires"] = c.Expires.Format(time.RFC1123)
		}
		cookies = append(cookies, ck)
	}
	return map[string]any{
		"code": r.Status, "status": r.StatusText, "headers": headers, "cookies": cookies,
		"body": bodyText(r.Body, r.Header), "bytes": rt.NewArrayBuffer(r.Body),
		"responseTime": r.Time.Milliseconds(), "responseSize": int64(len(r.Body)),
		"size": map[string]any{"body": int64(len(r.Body)), "header": r.HeaderSize, "total": r.HeaderSize + int64(len(r.Body))},
	}
}

// applyRequest takes back what a pre-request script made of the request.
func applyRequest(r *send.Request, data string) error {
	var in struct {
		Method string `json:"method"`
		URL    struct {
			Raw      string             `json:"raw"`
			Query    postman.Params     `json:"query"`
			Variable []postman.Variable `json:"variable"`
		} `json:"url"`
		Header postman.Params `json:"header"`
		Body   *postman.Body  `json:"body"`
		Auth   *postman.Auth  `json:"auth"`
	}
	if err := json.Unmarshal([]byte(data), &in); err != nil {
		return err
	}
	if in.Method != "" {
		r.Method = strings.ToUpper(in.Method)
	}
	paths := map[string]string{}
	for _, v := range in.URL.Variable {
		paths[v.Key] = string(v.Value)
	}
	query := in.URL.Query
	if query == nil {
		query = postman.Params{}
	}
	u := postman.Rebuild(r.URL, in.URL.Raw, query, paths)
	if len(u.Query) == 0 {
		u.Query = nil
	}
	r.URL = u
	r.Header = in.Header
	if in.Body != nil && in.Body.Mode == "" {
		in.Body = nil
	}
	r.Body = in.Body
	if in.Auth != nil {
		if in.Auth.Type == "" || in.Auth.Type == "inherit" {
			in.Auth = r.Auth
		}
		r.Auth = in.Auth
	}
	return nil
}

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

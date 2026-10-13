// Package run sends requests the way Postman does: the pre-request scripts
// of the collection, its folders and the request, variables, auth, the
// request itself, then the test scripts in the same order. It also runs
// whole folders and collections (the collection runner).
package run

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/shinyvision/posthero/internal/auth"
	"github.com/shinyvision/posthero/internal/jsonx"
	"github.com/shinyvision/posthero/internal/postman"
	"github.com/shinyvision/posthero/internal/script"
	"github.com/shinyvision/posthero/internal/send"
	"github.com/shinyvision/posthero/internal/vars"
)

// Runner sends requests.
type Runner struct {
	Engine *script.Engine
	Client *send.Client
	Auth   *auth.Manager
	// Settings are the global request settings (:set).
	Settings send.Settings
	// Dir is where relative file paths start.
	Dir   string
	Vault script.Vault
	// ScriptTimeout ends scripts that run longer.
	ScriptTimeout time.Duration
}

// Target is a request in its collection.
type Target struct {
	Collection *postman.Collection
	Item       *postman.Item
	Parents    []*postman.Item
}

// Location is the names of the collection, folders and request.
func (t Target) Location() []string {
	var out []string
	if t.Collection != nil {
		out = append(out, t.Collection.Info.Name)
	}
	for _, p := range t.Parents {
		out = append(out, p.Name)
	}
	return append(out, t.Item.Name)
}

// Execution is what happened when a request was sent.
type Execution struct {
	Target Target
	// Request is the request after the pre-request scripts; Prepared what
	// was sent (variables filled in, auth added).
	Request  *send.Request
	Prepared *send.Prepared
	Response *send.Response
	Auth     *auth.Used
	// Scripts is what the scripts did, pre-request and tests together.
	Scripts script.Result
	// Skipped says a pre-request script skipped the request.
	Skipped bool
	// Err is why the request wasn't sent or got no response.
	Err error
	// PreErr is a failing pre-request script (the request isn't sent).
	PreErr error
	Start  time.Time
	// Missing are variables that weren't set.
	Missing []string
}

// Tests returns the test results.
func (e *Execution) Tests() []script.Test { return e.Scripts.Tests }

// Options change one execution.
type Options struct {
	Info script.Info
	// Prompt asks the user what logging in needs; nil asks nothing.
	Prompt auth.Prompter
	// NoScripts sends without running scripts.
	NoScripts bool
}

// Execute sends one request with its scripts. v holds every scope (local,
// data, environment, collection, globals); scripts change them in place.
func (r *Runner) Execute(ctx context.Context, t Target, v *vars.Set, o Options) *Execution {
	ex := &Execution{Target: t, Start: time.Now()}
	a, from := send.EffectiveAuth(t.Item, t.Parents, t.Collection)
	req := send.FromItem(t.Item, a, r.Settings)
	ex.Request = req
	before := authJSON(req.Auth)

	info := o.Info
	info.RequestName, info.RequestID = t.Item.Name, t.Item.ID
	if info.IterationCount == 0 {
		info.IterationCount = 1
	}
	env := &script.Env{
		Vars: v, Info: info, Request: req, Jar: r.Client.Jar, Vault: r.Vault, Timeout: r.ScriptTimeout,
		Send: func(ctx context.Context, sr *send.Request) (*send.Response, error) { return r.sendPlain(ctx, sr, v) },
	}
	location := t.Location()
	var sess *script.Session
	if !o.NoScripts {
		var err error
		if sess, err = r.Engine.NewSession(ctx, env); err != nil {
			ex.Err = err
			return ex
		}
		for _, code := range scripts(t, "prerequest") {
			if err := sess.Run(code, "prerequest", location); err != nil {
				ex.Scripts = sess.Result()
				ex.PreErr = err
				ex.Err = fmt.Errorf("a pre-request script failed, so the request wasn't sent: %w", err)
				return ex
			}
		}
		ex.Scripts = sess.Result()
		if ex.Scripts.Skip {
			ex.Skipped = true
			return ex
		}
	}
	// The script may have changed the auth; its origin then is the script.
	if authJSON(req.Auth) != before {
		from = "pre-request script"
	}
	p, err := req.Prepare(v, send.Options{Dir: r.Dir})
	if err != nil {
		ex.Err = err
		return ex
	}
	ex.Missing = p.Missing
	ex.Prepared = p
	resp, used, err := r.Auth.Do(ctx, r.Client, p, req.Auth, from, v.Resolve, o.Prompt)
	ex.Auth = used
	if err != nil {
		ex.Err = err
		if resp != nil {
			// A body cut short (send.IncompleteError): the status, headers
			// and what arrived are shown with the error; no tests run on it.
			ex.Response = resp
			if resp.Sent != nil {
				ex.Prepared = resp.Sent
			}
		}
		return ex
	}
	ex.Response = resp
	if resp.Sent != nil {
		ex.Prepared = resp.Sent
	}
	if sess != nil {
		env.Sent, env.Response = ex.Prepared, resp
		for _, code := range scripts(t, "test") {
			_ = sess.Run(code, "test", location) // a failing test script is reported, the rest still run
		}
		ex.Scripts = sess.Result()
	}
	return ex
}

// scripts are the scripts for an event: the collection's, the folders'
// (outermost first), then the request's.
func scripts(t Target, listen string) []string {
	var out []string
	add := func(events []postman.Event) {
		if s := postman.Scripts(events, listen); strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	if t.Collection != nil {
		add(t.Collection.Event)
	}
	for _, p := range t.Parents {
		add(p.Event)
	}
	add(t.Item.Event)
	return out
}

// sendPlain sends a request of pm.sendRequest: its own auth, no scripts.
func (r *Runner) sendPlain(ctx context.Context, sr *send.Request, v *vars.Set) (*send.Response, error) {
	if sr.Settings.MaxRedirects == 0 {
		timeout := sr.Settings.Timeout
		sr.Settings = r.Settings
		if timeout > 0 {
			sr.Settings.Timeout = timeout
		}
	}
	p, err := sr.Prepare(v, send.Options{Dir: r.Dir})
	if err != nil {
		return nil, err
	}
	resp, _, err := r.Auth.Do(ctx, r.Client, p, sr.Auth, "pm.sendRequest", v.Resolve, nil)
	return resp, err
}

func authJSON(a *postman.Auth) string {
	if a == nil {
		return ""
	}
	data, _ := jsonx.Marshal(a)
	return string(data)
}

// ErrStopped says a run was stopped (by setNextRequest(null) or the user).
var ErrStopped = errors.New("stopped")

// Package signin hands a Postman browser login back to the running app.
package signin

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/shinyvision/posthero/internal/postman/syncapi"
)

type Options struct {
	Login       syncapi.LoginOptions
	OpenBrowser func(string) error
	// Ready exposes the local page for copying when a browser isn't available.
	Ready func(string)
}

// Run opens a temporary loopback page, then accepts either the desktop
// callback or a code pasted into that page. It never asks for a password.
func Run(ctx context.Context, opts Options) (syncapi.Session, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	flow, err := syncapi.BeginLogin(ctx, opts.Login)
	if err != nil {
		return syncapi.Session{}, err
	}
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return syncapi.Session{}, err
	}
	nonce := make([]byte, 24)
	if _, err := rand.Read(nonce); err != nil {
		l.Close()
		return syncapi.Session{}, err
	}
	path := "/" + hex.EncodeToString(nonce) + "/"
	address := "http://" + l.Addr().String() + path
	b := &browser{ctx: ctx, flow: flow, host: l.Addr().String(), path: path, result: make(chan syncapi.Session, 1)}
	srv := &http.Server{Handler: b, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 40 * time.Second, IdleTimeout: 10 * time.Second}
	served := make(chan error, 1)
	go func() { served <- srv.Serve(l) }()
	// Register only during this login. On other platforms, or if desktop
	// integration fails, the local page still accepts a pasted code.
	cleanup, note := register(ctx, address, b.complete)
	b.mu.Lock()
	b.note = note
	b.mu.Unlock()
	defer cleanup()
	if opts.Ready != nil {
		opts.Ready(address)
	}
	if opts.OpenBrowser != nil {
		if err := opts.OpenBrowser(address); err != nil {
			b.mu.Lock()
			b.note = "Open this page manually from the address in posthero."
			b.mu.Unlock()
		}
	}
	select {
	case s := <-b.result:
		// The helper opens this page after relaying the callback. Keep the
		// success message reachable briefly, without accepting further codes.
		go func() { time.Sleep(10 * time.Second); _ = srv.Close() }()
		return s, nil
	case <-ctx.Done():
		_ = srv.Close()
		return syncapi.Session{}, ctx.Err()
	case err := <-served:
		_ = srv.Close()
		return syncapi.Session{}, err
	}
}

type browser struct {
	ctx           context.Context
	flow          *syncapi.LoginFlow
	host, path    string
	mu            sync.Mutex
	busy, done    bool
	note, problem string
	result        chan syncapi.Session
}

func (b *browser) complete(input string) error {
	if _, err := syncapi.AuthorizationCode(input); err != nil {
		return err
	}
	b.mu.Lock()
	if b.busy || b.done {
		b.mu.Unlock()
		return errors.New("a sign-in code is already being processed")
	}
	b.busy = true
	b.mu.Unlock()
	s, err := b.flow.Complete(b.ctx, input)
	b.mu.Lock()
	b.busy = false
	if err != nil {
		b.problem = err.Error()
	} else {
		b.done, b.problem = true, ""
	}
	b.mu.Unlock()
	if err == nil {
		b.result <- s
	}
	return err
}

func (b *browser) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; connect-src 'self'; form-action 'self'; frame-ancestors 'none'")
	if r.Host != b.host || !strings.HasPrefix(r.URL.Path, b.path) {
		http.NotFound(w, r)
		return
	}
	if origin := r.Header.Get("Origin"); origin != "" && origin != "http://"+b.host {
		http.Error(w, "wrong origin", http.StatusForbidden)
		return
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == b.path+"status":
		b.mu.Lock()
		defer b.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"done": b.done, "problem": b.problem})
	case r.Method == http.MethodPost && r.URL.Path == b.path:
		r.Body = http.MaxBytesReader(w, r.Body, 16384)
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid form", 400)
			return
		}
		if err := b.complete(r.PostForm.Get("code")); err != nil {
			b.render(w, err.Error())
			return
		}
		b.render(w, "")
	case r.Method == http.MethodGet && r.URL.Path == b.path:
		b.render(w, "")
	default:
		http.NotFound(w, r)
	}
}

func (b *browser) render(w http.ResponseWriter, problem string) {
	b.mu.Lock()
	data := struct {
		URL, Note, Problem string
		Done               bool
	}{b.flow.URL, b.note, problem, b.done}
	b.mu.Unlock()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = page.Execute(w, data)
}

var page = template.Must(template.New("login").Parse(`<!doctype html><html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>Sign in to Postman · posthero</title>
<style>body{font:18px system-ui;max-width:42rem;margin:10vh auto;padding:2rem;color:#243040;background:#f6f7f9}a,button{color:#934114}button,input{font:inherit;padding:.7rem}input{width:95%}p{line-height:1.5}.error{color:#b02323}</style>
<h1 id="title">{{if .Done}}Connected to Postman{{else}}Connect posthero to Postman{{end}}</h1>
<p id="success" {{if not .Done}}hidden{{end}}>You can close this tab and return to posthero.</p>
<div id="login" {{if .Done}}hidden{{end}}><p><a href="{{.URL}}" target="_blank" rel="noreferrer">Sign in to Postman</a> in your browser, choose your team, then allow the browser to open posthero.</p>
<p>{{.Note}}</p><p>If the browser doesn't return, copy the code or callback link from Postman's success page and paste it below. If it opens your team home instead, open the sign-in link in a private window.</p>
<form method="post"><label>Sign-in code or callback link<input name="code" required autocomplete="off" autofocus></label><p><button>Finish sign-in</button></p></form><p class="error" id="problem">{{.Problem}}</p><p>This sign-in expires after five minutes. Cancel it from posthero with Esc twice.</p></div>
<script>const timer=setInterval(async()=>{try{const r=await fetch('status');const s=await r.json();if(s.done){clearInterval(timer);document.getElementById('login').hidden=true;document.getElementById('success').hidden=false;document.getElementById('title').textContent='Connected to Postman'}else if(s.problem){document.getElementById('problem').textContent=s.problem}}catch(e){}},750)</script></html>`))

// Relay is the small process launched by the desktop URI handler. The
// socket is inside a private directory belonging to the sign-in process.
// The returned URL is the local success page, never the callback secret.
func Relay(socket, callback string) (string, error) {
	if !strings.HasPrefix(callback, syncapi.CallbackURL+"?") {
		return "", errors.New("invalid Postman callback")
	}
	if _, err := syncapi.AuthorizationCode(callback); err != nil {
		return "", err
	}
	c, err := net.DialTimeout("unix", socket, 3*time.Second)
	if err != nil {
		return "", errors.New("no Postman sign-in is waiting in posthero")
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(35 * time.Second))
	if _, err := fmt.Fprintln(c, callback); err != nil {
		return "", err
	}
	data, err := io.ReadAll(io.LimitReader(c, 8192))
	if err != nil {
		return "", errors.New("couldn't hand the sign-in back to posthero")
	}
	address := string(data)
	if !strings.HasPrefix(address, "http://127.0.0.1:") {
		return "", errors.New("posthero could not complete the sign-in; check the sign-in page")
	}
	return address, nil
}

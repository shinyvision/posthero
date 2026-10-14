package app

import (
	"context"
	"errors"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/shinyvision/posthero/internal/auth"
	"github.com/shinyvision/posthero/internal/config"
	"github.com/shinyvision/posthero/internal/postman"
	"github.com/shinyvision/posthero/internal/postman/signin"
	"github.com/shinyvision/posthero/internal/postman/syncapi"
	"github.com/shinyvision/posthero/internal/ui/form"
)

// keysURL is where Postman API keys are made.
const keysURL = "https://go.postman.co/settings/me/api-keys"

// loggedInMsg says a Postman login worked.
type loggedInMsg struct {
	login config.Login
	me    postman.User
	note  string
}

// loginForm offers browser sign-in and keeps the API key option available.
func (m *Model) loginForm(problem string) *form.Form {
	f := &form.Form{Title: "Connect to Postman", Subtitle: "Sign in with your Postman account in your browser. Your session is kept in the system keyring.", Verb: "sign in", Busy: "finish signing in in your browser…"}
	if problem != "" {
		f.Subtitle = problem
	}
	f.Rows = []*form.Row{{Fields: []*form.Field{
		form.NewButton("Sign in with browser", nil),
		form.NewButton("Use API key", func(*form.Form) tea.Cmd { return form.Open(m.apiKeyForm("")) }),
		form.NewCancel(),
	}}}
	f.Submit = func(f *form.Form) tea.Cmd {
		ctx, cancel := context.WithCancel(m.ctx)
		f.OnCancel = cancel
		opts := m.opts.Sync
		login := syncapi.LoginOptions{IdentityURL: opts.IdentityURL}
		if s := m.opts.Login.Session; s != nil {
			login.TeamID, login.TeamDomain = s.TeamID, s.TeamDomain
		} else {
			login.TeamID = m.me.TeamID
		}
		send := m.send
		return func() tea.Msg {
			defer cancel()
			s, err := signin.Run(ctx, signin.Options{Login: login, OpenBrowser: auth.OpenURL, Ready: func(address string) {
				if send != nil {
					send(statusMsg{text: "sign-in page: " + address})
				}
			}})
			if err != nil {
				return form.ResultMsg{Err: err}
			}
			api := syncapi.New(s.Token, opts)
			defer api.Close()
			me, err := api.Me(ctx)
			if err != nil {
				return form.ResultMsg{Err: err}
			}
			return form.ResultMsg{Then: loggedInMsg{login: config.Login{Session: &s, From: "posthero"}, me: me}}
		}
	}
	return f
}

func (m *Model) apiKeyForm(problem string) *form.Form {
	key := form.NewText("API key", "", 52)
	key.SetMask(true)
	key.Placeholder = "PMAK-…"
	key.Description = "Postman → your avatar → Settings → API keys → Generate"
	subtitle := "posthero reads and saves your team's collections through the Postman API, with an API key of yours. It goes in your system keyring. Make one at " + keysURL
	if problem != "" {
		subtitle = problem
	}
	f := &form.Form{
		Title:    "Connect to Postman",
		Subtitle: subtitle,
		Rows: []*form.Row{
			{Label: "API key", Fields: []*form.Field{key}},
			{Fields: []*form.Field{form.NewButton("Connect", nil), form.NewCancel()}},
		},
		Verb: "connect",
		Busy: "checking with Postman…",
	}
	base := m.opts.APIBase
	f.Submit = func(f *form.Form) tea.Cmd {
		l := config.Login{APIKey: strings.TrimSpace(f.Field("API key").Value()), From: "posthero"}
		if l.APIKey == "" {
			return form.Fail(errors.New("paste an API key; make one at " + keysURL))
		}
		ctx, cancel := context.WithTimeout(m.ctx, 20*time.Second)
		f.OnCancel = cancel
		return func() tea.Msg {
			defer cancel()
			me, err := postman.NewClient(l.APIKey, base).Me(ctx)
			if err != nil {
				if postman.IsUnauthorized(err) {
					err = errors.New("Postman didn't accept that key: is it whole, and not expired?")
				}
				return form.ResultMsg{Err: err}
			}
			return form.ResultMsg{Then: loggedInMsg{login: l, me: me}}
		}
	}
	return f
}

func (m *Model) loggedIn(msg loggedInMsg) tea.Cmd {
	// Work still running belongs to the old key: stop it.
	m.stop()
	m.stopWatch()
	m.closeRemote()
	m.ctx, m.stop = context.WithCancel(context.Background())
	if !m.opts.Demo {
		var err error
		if msg.login.Session != nil {
			msg.note, err = config.SaveSession(*msg.login.Session)
		} else {
			msg.note, err = config.SaveLogin(msg.login.APIKey)
		}
		if err != nil {
			msg.note = "connected, but couldn't keep the login: " + err.Error()
		}
	}
	m.opts.Login, m.opts.HasLogin = msg.login, true
	m.remote = m.newRemote(msg.login)
	m.renewing, m.nextRenew = false, time.Time{}
	m.refreshSeq++
	m.refreshing, m.refreshAgain, m.refreshAsked = false, false, false
	if m.local == nil {
		m.api = m.remote
		if m.st != nil && m.st.API != nil {
			m.st.API = m.api
		}
	}
	if m.st == nil {
		m.openWorkspace(m.startWorkspace())
	}
	text := "connected as " + orName(msg.me.FullName, msg.me.Username)
	if msg.note != "" {
		text += " · " + msg.note
	}
	m.setStatus(text, false)
	return m.start()
}

func (m *Model) newRemote(l config.Login) postman.API {
	if l.Session == nil {
		return postman.NewClient(l.APIKey, m.opts.APIBase)
	}
	opts := m.opts.Sync
	if !m.opts.Demo && opts.OnSession == nil {
		opts.OnSession = func(s syncapi.Session) error { _, err := config.SaveSession(s); return err }
	}
	return syncapi.NewSession(*l.Session, opts)
}

func (m *Model) closeRemote() {
	if c, ok := m.remote.(interface{ Close() }); ok {
		c.Close()
	}
}

func (m *Model) postmanLoginProblem() {
	if m.opts.Login.Session != nil {
		m.form = m.loginForm("Your Postman session has ended. Sign in again to reconnect.")
	} else {
		m.form = m.apiKeyForm("Postman didn't accept the API key. Make a new one and paste it here.")
	}
}

package app

import (
	"context"
	"errors"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/shinyvision/posthero/internal/config"
	"github.com/shinyvision/posthero/internal/postman"
	"github.com/shinyvision/posthero/internal/ui/form"
)

// keysURL is where Postman API keys are made.
const keysURL = "https://go.postman.co/settings/me/api-keys"

// loggedInMsg says an API key worked.
type loggedInMsg struct {
	login config.Login
	me    postman.User
	note  string
}

// loginForm asks for a Postman API key. problem explains why it is asked
// again.
func (m *Model) loginForm(problem string) *form.Form {
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
	save := !m.opts.Demo
	f.Submit = func(f *form.Form) tea.Cmd {
		l := config.Login{APIKey: strings.TrimSpace(f.Field("API key").Value()), From: "posthero"}
		if l.APIKey == "" {
			return form.Fail(errors.New("paste an API key; make one at " + keysURL))
		}
		return func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			me, err := postman.NewClient(l.APIKey, base).Me(ctx)
			if err != nil {
				if postman.IsUnauthorized(err) {
					err = errors.New("Postman didn't accept that key: is it whole, and not expired?")
				}
				return form.ResultMsg{Err: err}
			}
			note := ""
			if save {
				if note, err = config.SaveLogin(l.APIKey); err != nil {
					note = "connected, but couldn't keep the key: " + err.Error()
				}
			}
			return form.ResultMsg{Then: loggedInMsg{login: l, me: me, note: note}}
		}
	}
	return f
}

func (m *Model) loggedIn(msg loggedInMsg) tea.Cmd {
	// Work still running belongs to the old key: stop it.
	m.stop()
	m.ctx, m.stop = context.WithCancel(context.Background())
	m.opts.Login, m.opts.HasLogin = msg.login, true
	m.remote = postman.NewClient(msg.login.APIKey, m.opts.APIBase)
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

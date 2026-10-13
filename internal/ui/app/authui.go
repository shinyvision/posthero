package app

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/shinyvision/posthero/internal/auth"
	"github.com/shinyvision/posthero/internal/mouse"
	"github.com/shinyvision/posthero/internal/postman"
	"github.com/shinyvision/posthero/internal/send"
	"github.com/shinyvision/posthero/internal/ui/form"
	"github.com/shinyvision/posthero/internal/ui/lineinput"
	"github.com/shinyvision/posthero/internal/ui/modal"
	"github.com/shinyvision/posthero/internal/ui/theme"
	"github.com/shinyvision/posthero/internal/vars"
	"github.com/shinyvision/posthero/internal/vim"
)

// errCancelled says the user cancelled a login dialog.
var errCancelled = errors.New("login cancelled")

// promptMsg asks the user something a login needs, from the background.
type promptMsg struct {
	kind    string // "login", "form" or "browser"
	login   auth.LoginPrompt
	form    auth.FormPrompt
	browser auth.BrowserPrompt
	reply   chan promptAnswer
	ctx     context.Context
}

type promptAnswer struct {
	login auth.LoginAnswer
	form  auth.FormAnswer
	url   string
	err   error
}

// uiPrompter asks through the app's dialogs; logins in the background wait
// for the answer.
type uiPrompter struct{ send func(tea.Msg) }

func (m *Model) prompter() auth.Prompter {
	if m.send == nil {
		return nil
	}
	return &uiPrompter{send: m.send}
}

func (p *uiPrompter) ask(ctx context.Context, msg promptMsg) promptAnswer {
	msg.reply = make(chan promptAnswer, 1)
	msg.ctx = ctx
	p.send(msg)
	select {
	case a := <-msg.reply:
		return a
	case <-ctx.Done():
		return promptAnswer{err: ctx.Err()}
	}
}

// Login implements auth.Prompter.
func (p *uiPrompter) Login(ctx context.Context, q auth.LoginPrompt) (auth.LoginAnswer, error) {
	a := p.ask(ctx, promptMsg{kind: "login", login: q})
	return a.login, a.err
}

// Form implements auth.Prompter.
func (p *uiPrompter) Form(ctx context.Context, q auth.FormPrompt) (auth.FormAnswer, error) {
	a := p.ask(ctx, promptMsg{kind: "form", form: q})
	return a.form, a.err
}

// Browser implements auth.Prompter.
func (p *uiPrompter) Browser(ctx context.Context, q auth.BrowserPrompt) (string, error) {
	a := p.ask(ctx, promptMsg{kind: "browser", browser: q})
	return a.url, a.err
}

// Status implements auth.Prompter.
func (p *uiPrompter) Status(text string) { p.send(statusMsg{text: text}) }

// showPrompt shows a login dialog.
func (m *Model) showPrompt(msg promptMsg) tea.Cmd {
	answered := false
	reply := func(a promptAnswer) {
		if !answered {
			answered = true
			msg.reply <- a
		}
	}
	switch msg.kind {
	case "login":
		q := msg.login
		user := form.NewText("User", q.Username, 40)
		user.Description = "your user name or email"
		pass := form.NewText("Password", "", 40)
		pass.SetMask(true)
		remember := form.NewToggle("Remember", q.Remember, 40)
		remember.Description = "keep the password in your system keyring, so posthero logs in by itself"
		subtitle := "posthero logs in for you: it fills in the login page itself."
		if q.Error != "" {
			subtitle = theme.Error.Render(q.Error)
		}
		f := &form.Form{
			Title:    "Log in to " + q.Issuer,
			Subtitle: subtitle,
			Rows: []*form.Row{
				{Label: "User", Fields: []*form.Field{user}},
				{Label: "Password", Fields: []*form.Field{pass}},
				{Label: "Remember", Fields: []*form.Field{remember}},
				{Fields: []*form.Field{form.NewButton("Log in", nil), form.NewCancel()}},
			},
			Verb: "log in",
		}
		f.Submit = func(f *form.Form) tea.Cmd {
			reply(promptAnswer{login: auth.LoginAnswer{Username: strings.TrimSpace(user.Value()), Password: pass.Value(), Remember: remember.On()}})
			return func() tea.Msg { return form.ResultMsg{} }
		}
		m.form = f
		m.formClosed = func() { reply(promptAnswer{err: errCancelled}) }
		m.watchPrompt(msg.ctx, f)
	case "form":
		q := msg.form
		var rows []*form.Row
		var fields []*form.Field
		for _, fl := range q.Fields {
			f := form.NewText(fl.Label, fl.Value, 40)
			f.SetMask(fl.Secret)
			fields = append(fields, f)
			rows = append(rows, &form.Row{Label: fl.Label, Fields: []*form.Field{f}})
		}
		var choice *form.Field
		if len(q.Buttons) > 1 {
			labels := make([]string, len(q.Buttons))
			for i, b := range q.Buttons {
				labels[i] = b.Label
			}
			choice = form.NewSelect("Answer", labels[0], labels, 40)
			rows = append(rows, &form.Row{Label: "Answer", Fields: []*form.Field{choice}})
		}
		rows = append(rows, &form.Row{Fields: []*form.Field{form.NewButton("Go on", nil), form.NewCancel()}})
		subtitle := q.Message
		if subtitle == "" {
			subtitle = "The login page of " + q.Issuer + " asks for this."
		}
		f := &form.Form{Title: q.Title, Subtitle: subtitle, Rows: rows, Verb: "go on"}
		f.Submit = func(*form.Form) tea.Cmd {
			ans := auth.FormAnswer{Values: map[string]string{}}
			for i, fl := range q.Fields {
				ans.Values[fl.Name] = fields[i].Value()
			}
			if choice != nil {
				for _, b := range q.Buttons {
					if b.Label == choice.Value() {
						ans.Button = b
					}
				}
			}
			reply(promptAnswer{form: ans})
			return func() tea.Msg { return form.ResultMsg{} }
		}
		m.form = f
		m.formClosed = func() { reply(promptAnswer{err: errCancelled}) }
		m.watchPrompt(msg.ctx, f)
	case "browser":
		q := msg.browser
		w := &browserWait{q: q, reply: reply, input: lineinput.New("› ", "", m.regs)}
		w.input.Insert()
		cmds := []tea.Cmd{modal.Show(w)}
		if q.Listening && q.Done != nil {
			done := q.Done
			cmds = append(cmds, func() tea.Msg {
				select {
				case u := <-done:
					return browserDoneMsg{w: w, url: u}
				case <-msg.ctx.Done():
					return browserDoneMsg{w: w, err: msg.ctx.Err()}
				}
			})
		}
		return tea.Batch(cmds...)
	}
	return nil
}

// watchPrompt closes a login form whose login gave up (timed out, or the
// request was cancelled).
func (m *Model) watchPrompt(ctx context.Context, f *form.Form) {
	go func() {
		<-ctx.Done()
		if m.send != nil {
			m.send(cmdMsg{func() tea.Cmd {
				if m.form == f {
					m.form, m.formClosed = nil, nil
				}
				return nil
			}})
		}
	}()
}

// browserDoneMsg says the browser came back to posthero.
type browserDoneMsg struct {
	w   *browserWait
	url string
	err error
}

// browserWait waits for a login in the browser, and takes the address the
// browser ended up on when posthero can't catch it by itself.
type browserWait struct {
	q     auth.BrowserPrompt
	reply func(promptAnswer)
	input *lineinput.Model
	done  bool
	box   string
}

// Key implements modal.Modal.
func (b *browserWait) Key(k string) (bool, tea.Cmd) {
	if b.done {
		return true, nil
	}
	switch k {
	case "<Esc>":
		if b.input.Mode() == vim.Normal || b.input.Value() == "" {
			b.reply(promptAnswer{err: errCancelled})
			return true, nil
		}
	case "<CR>":
		u := strings.TrimSpace(b.input.Value())
		if u == "" {
			return false, nil
		}
		b.reply(promptAnswer{url: u})
		return true, nil
	case "gx", "<C-o>":
		_ = auth.OpenURL(b.q.URL)
		return false, nil
	}
	b.input.Key(k)
	return false, nil
}

// Paste implements the paste of modals.
func (b *browserWait) Paste(s string) { b.input.Paste(strings.TrimSpace(s)) }

// View implements modal.Modal.
func (b *browserWait) View(maxW, _ int) string {
	w := min(maxW-6, 76)
	body := []string{"posthero opened the login of " + b.q.Issuer + " in your browser."}
	if b.q.Why != "" {
		body = append(body, theme.Dim.Render("("+b.q.Why+")"))
	}
	body = append(body, "")
	if b.q.Listening {
		body = append(body, "When you have logged in, posthero goes on by itself.", theme.Dim.Render("Or paste the address your browser ends up on:"))
	} else {
		body = append(body, "When you have logged in, your browser ends up on a page that may not load.", "Copy that page's address and paste it here:")
	}
	line, _ := b.input.View(w)
	body = append(body, line, "", theme.Dim.Render("⏎ continue · ctrl+o opens the browser again · esc cancels"))
	b.box = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(theme.Accent).Padding(0, 1).Width(w + 4).
		Render(theme.AccentFg.Bold(true).Render("Log in in your browser") + "\n\n" + strings.Join(body, "\n"))
	return b.box
}

// Mouse implements modal.Mouser.
func (b *browserWait) Mouse(mouse.Event) (bool, tea.Cmd) { return false, nil }

func (m *Model) browserDone(msg browserDoneMsg) tea.Cmd {
	if msg.w.done {
		return nil
	}
	msg.w.done = true
	if msg.err != nil {
		msg.w.reply(promptAnswer{err: msg.err})
	} else {
		msg.w.reply(promptAnswer{url: msg.url})
	}
	for i, md := range m.modals {
		if md == msg.w {
			m.modals = append(m.modals[:i], m.modals[i+1:]...)
			break
		}
	}
	m.setStatus("logged in through the browser", false)
	return nil
}

// authOf is the OAuth 2.0 settings of a request (or the open one), with
// its variables filled in. ok is false when it doesn't use OAuth 2.0.
func (m *Model) authOf(colID, itemID string) (auth.Config, *postman.Auth, string, bool) {
	c, it, parents := m.findItem(colID, itemID)
	if it == nil || c == nil || c.Data == nil {
		return auth.Config{}, nil, "", false
	}
	a, from := send.EffectiveAuth(it, parents, c.Data)
	if a == nil {
		return auth.Config{}, nil, "", false
	}
	if a.Type != "oauth2" {
		return auth.Config{}, a, from, false
	}
	set := m.scopes(colID)
	return auth.ConfigFrom(auth.Resolve(a, set.Resolve)), a, from, true
}

// currentAuth is the OAuth 2.0 of the request in the editor or under the
// tree's cursor.
func (m *Model) currentAuth() (auth.Config, string, bool) {
	n := m.treeNode()
	if n == nil {
		return auth.Config{}, "", false
	}
	if n.Col.Data == nil {
		return auth.Config{}, "", false // still loading
	}
	if n.Item == nil || n.Item.Folder {
		// A collection or folder: the auth it gives its requests.
		a := n.Col.Data.Auth
		if n.Item != nil {
			a = n.Item.Auth
		}
		if a == nil || a.Type != "oauth2" {
			return auth.Config{}, "", false
		}
		return auth.ConfigFrom(auth.Resolve(a, m.scopes(n.Col.Summary.ID).Resolve)), n.Col.Summary.ID, true
	}
	cfg, _, _, ok := m.authOf(n.Col.Summary.ID, n.Item.ID)
	return cfg, n.Col.Summary.ID, ok
}

// authSummary says how a request signs in.
func authSummary(a *postman.Auth, from string, set *vars.Set) string {
	name := auth.Names[a.Type]
	if name == "" {
		name = a.Type
	}
	out := name + ", set on the " + from
	if strings.HasPrefix(from, "folder ") {
		out = name + ", set on " + from
	}
	if a.Type == "oauth2" {
		c := auth.ConfigFrom(auth.Resolve(a, set.Resolve))
		out += fmt.Sprintf("\n\n%s at %s\nclient %s · %s", orName(c.TokenName, "a token"), c.Issuer(), c.ClientID, grantName(c.Grant))
		out += "\n\n:token shows the token · :login logs in again · :logout forgets it"
	}
	return out
}

func grantName(g string) string {
	switch g {
	case auth.GrantPKCE:
		return "authorization code with PKCE"
	case auth.GrantCode:
		return "authorization code"
	case auth.GrantImplicit:
		return "implicit"
	case auth.GrantPassword:
		return "password"
	case auth.GrantClient:
		return "client credentials"
	}
	return g
}

// authLine is the status bar's note about the open request's login.
func (m *Model) authLine() string {
	if m.open == nil {
		return ""
	}
	cfg, a, _, ok := m.authOf(m.open.colID, m.open.itemID)
	if !ok {
		if a != nil && a.Type != "noauth" {
			return theme.Dim.Render(auth.Names[a.Type])
		}
		return ""
	}
	if !cfg.Fetches() {
		return theme.Dim.Render("token from the settings")
	}
	t := m.authm.Tokens.Get(cfg.Key())
	if t == nil {
		return theme.Dim.Render("not logged in · ⏎ logs in")
	}
	who := t.Subject
	if who == "" {
		who = "logged in"
	}
	now := time.Now()
	switch {
	case t.Valid(now):
		left := ""
		if !t.ExpiresAt.IsZero() {
			left = " " + shortDuration(time.Until(t.ExpiresAt))
		}
		return theme.Success.Render("●") + " " + who + theme.Dim.Render(left)
	case t.CanRefresh(now):
		return theme.Warn.Render("●") + " " + who + theme.Dim.Render(" refreshes on the next request")
	}
	return theme.Dim.Render("○ " + who + " · logs in again on the next request")
}

func shortDuration(d time.Duration) string {
	switch {
	case d < 0:
		return "ran out"
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// showToken shows the login of the current request, with what to do.
func (m *Model) showToken() tea.Cmd {
	cfg, _, ok := m.currentAuth()
	if !ok {
		m.setStatus("this request doesn't log in with OAuth 2.0", true)
		return nil
	}
	t := m.authm.Tokens.Get(cfg.Key())
	var b strings.Builder
	fmt.Fprintf(&b, "%s\nclient %s · %s\n", cfg.Issuer(), cfg.ClientID, grantName(cfg.Grant))
	if t == nil {
		b.WriteString("\nNot logged in yet: the next request logs in.")
	} else {
		now := time.Now()
		fmt.Fprintf(&b, "\nuser       %s\n", orName(t.Subject, "?"))
		if !t.ExpiresAt.IsZero() {
			fmt.Fprintf(&b, "valid      %s (until %s)\n", shortDuration(t.ExpiresAt.Sub(now)), t.ExpiresAt.Local().Format("15:04:05"))
		}
		switch {
		case t.RefreshToken == "":
			b.WriteString("refresh    no refresh token; logs in again when it runs out\n")
		case t.RefreshExpiresAt.IsZero():
			b.WriteString("refresh    works until the identity provider ends the session\n")
		default:
			fmt.Fprintf(&b, "refresh    %s (posthero refreshes it while it is open)\n", shortDuration(t.RefreshExpiresAt.Sub(now)))
		}
		fmt.Fprintf(&b, "logged in  %s · refreshed %d times\n", t.ObtainedAt.Local().Format("2 Jan 15:04"), t.Refreshed)
		if t.Scope != "" {
			fmt.Fprintf(&b, "scope      %s\n", t.Scope)
		}
		if claims, ok := auth.Claims(t.AccessToken); ok {
			if roles := tokenRoles(claims); len(roles) > 0 {
				fmt.Fprintf(&b, "roles      %s\n", strings.Join(roles, ", "))
			}
		}
		if note := m.authm.Note(cfg.Key()); note != "" {
			b.WriteString("\n" + theme.Warn.Render("last refresh: "+note) + "\n")
		}
	}
	opts := []modal.Option{
		{Key: "l", Label: "Log in again", Run: func() tea.Msg { return cmdMsg{func() tea.Cmd { return m.forceLogin("") }} }},
		{Key: "L", Label: "Log out", Run: func() tea.Msg { return cmdMsg{func() tea.Cmd { return m.logout(false) }} }},
	}
	if t != nil {
		tok := t.AccessToken
		opts = append([]modal.Option{
			{Key: "y", Label: "Copy the token", Run: func() tea.Msg { return cmdMsg{func() tea.Cmd { m.copyText(tok, "the access token"); return nil }} }},
			{Key: "r", Label: "Refresh now", Run: func() tea.Msg { return cmdMsg{func() tea.Cmd { return m.refreshToken(cfg) }} }},
		}, opts...)
	}
	return modal.Show(&modal.Choice{Title: "Login", Body: b.String(), Options: opts})
}

// tokenRoles reads the roles a Keycloak (or Azure) token gives.
func tokenRoles(claims map[string]any) []string {
	var out []string
	add := func(v any) {
		if list, ok := v.([]any); ok {
			for _, r := range list {
				if s, ok := r.(string); ok && !strings.HasPrefix(s, "default-roles") && s != "offline_access" && s != "uma_authorization" {
					out = append(out, s)
				}
			}
		}
	}
	if ra, ok := claims["realm_access"].(map[string]any); ok {
		add(ra["roles"])
	}
	if res, ok := claims["resource_access"].(map[string]any); ok {
		for client, v := range res {
			if m, ok := v.(map[string]any); ok {
				if list, ok := m["roles"].([]any); ok {
					for _, r := range list {
						if s, ok := r.(string); ok {
							out = append(out, client+":"+s)
						}
					}
				}
			}
		}
	}
	add(claims["roles"])
	sort.Strings(out)
	return out
}

func (m *Model) refreshToken(cfg auth.Config) tea.Cmd {
	p := m.prompter()
	return m.run("refreshing the token", func(ctx context.Context) tea.Msg {
		_, how, err := m.authm.Token(ctx, cfg, p, auth.Fresh)
		if err != nil {
			return statusMsg{text: "refresh: " + err.Error(), isError: true}
		}
		if how == auth.LoggedIn {
			return statusMsg{text: "logged in again"}
		}
		return statusMsg{text: "refreshed"}
	})
}

// forceLogin logs in again (as user, when given) for the current request.
func (m *Model) forceLogin(user string) tea.Cmd {
	cfg, _, ok := m.currentAuth()
	if !ok {
		m.setStatus("this request doesn't log in with OAuth 2.0", true)
		return nil
	}
	p := m.prompter()
	return m.run("logging in to "+cfg.Issuer(), func(ctx context.Context) tea.Msg {
		t, err := m.authm.ForceLogin(ctx, cfg, p, user)
		if err != nil {
			return statusMsg{text: "login: " + err.Error(), isError: true}
		}
		return statusMsg{text: "logged in to " + cfg.Issuer() + " as " + orName(t.Subject, "you")}
	})
}

// logout forgets the current request's token and ends the session;
// forget drops the saved password too.
func (m *Model) logout(forget bool) tea.Cmd {
	cfg, _, ok := m.currentAuth()
	if !ok {
		m.setStatus("this request doesn't log in with OAuth 2.0", true)
		return nil
	}
	return m.run("logging out", func(context.Context) tea.Msg {
		m.authm.Forget(cfg, forget)
		note := "logged out of " + cfg.Issuer() + "; the next request logs in again"
		if forget {
			note += ", asking for the password"
		}
		return statusMsg{text: note}
	})
}

// keptFreshMsg says which tokens were refreshed in the background.
type keptFreshMsg struct {
	refreshed []string
	failed    map[string]error
}

// keepFresh refreshes the tokens of the workspace that are about to run
// out, so logins never end while posthero is open.
func (m *Model) keepFresh() tea.Cmd {
	if m.st == nil {
		return nil
	}
	configs := map[string]auth.Config{}
	known := map[string]bool{}
	for _, t := range m.authm.Tokens.All() {
		known[t.Key] = true
	}
	if len(known) == 0 {
		return nil
	}
	for _, c := range m.st.Collections() {
		if c.Data == nil {
			continue
		}
		var set *vars.Set
		add := func(a *postman.Auth) {
			if a == nil || a.Type != "oauth2" {
				return
			}
			if set == nil {
				set = m.scopes(c.Summary.ID)
			}
			cfg := auth.ConfigFrom(auth.Resolve(a, set.Resolve))
			if known[cfg.Key()] {
				configs[cfg.Key()] = cfg
			}
		}
		add(c.Data.Auth)
		c.Data.Walk(func(it *postman.Item, _ []*postman.Item) bool {
			if it.Folder {
				add(it.Auth)
			} else if it.Request != nil {
				add(it.Request.Auth)
			}
			return true
		})
	}
	if len(configs) == 0 {
		return nil
	}
	am := m.authm
	ctx := m.ctx
	return func() tea.Msg {
		c, cancel := context.WithTimeout(ctx, time.Minute)
		defer cancel()
		refreshed, failed := am.KeepFresh(c, configs)
		if len(refreshed) == 0 && len(failed) == 0 {
			return nil
		}
		return keptFreshMsg{refreshed: refreshed, failed: failed}
	}
}

func (m *Model) keptFresh(msg keptFreshMsg) tea.Cmd {
	// Refreshing is quiet; the status bar's login note shows the time left.
	return nil
}

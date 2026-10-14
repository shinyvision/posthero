package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/shinyvision/posthero/internal/auth"
	"github.com/shinyvision/posthero/internal/mouse"
	"github.com/shinyvision/posthero/internal/postman"
	"github.com/shinyvision/posthero/internal/ui/modal"
	"github.com/shinyvision/posthero/internal/ui/theme"
	"github.com/shinyvision/posthero/internal/vars"
)

// usersHelp is the keys of the users dialog, for it and the help.
const usersHelp = `  j k gg G       move
  ⏎ l            work as the user under the cursor
  1-9            work as the user with that number
  a o            sign in as someone new (they are kept too)
  dd D x         remove the user: their saved password, token and session
  esc q          close`

// userConfig is the OAuth 2.0 login of the current request, when it has
// users to switch between; otherwise it says why not in the status bar.
func (m *Model) userConfig() (auth.Config, bool) {
	cfg, _, ok := m.currentAuth()
	switch {
	case !ok:
		m.setStatus("this request doesn't log in with OAuth 2.0, so it has no users", true)
	case !cfg.Fetches():
		m.setStatus("this request's OAuth 2.0 uses the token in its settings, so it has no users", true)
	case !cfg.HasUsers():
		m.setStatus("this request logs in as its client (client credentials), so it has no users", true)
	default:
		return cfg, true
	}
	return auth.Config{}, false
}

// showUsers opens the users dialog: who the current request's login can
// work as, to switch, add or remove one.
func (m *Model) showUsers() tea.Cmd {
	cfg, ok := m.userConfig()
	if !ok {
		return nil
	}
	u := &usersModal{m: m, cfg: cfg}
	u.reload()
	for i, us := range u.users {
		if us.Current {
			u.sel = i
		}
	}
	return modal.Show(u)
}

// switchUser makes the current request's login work as name (someone new,
// asked for, when empty). Other logins of the workspace at the same
// identity provider follow, unless they name their own user.
func (m *Model) switchUser(name string) tea.Cmd {
	cfg, ok := m.userConfig()
	if !ok {
		return nil
	}
	var others []auth.Config
	for key, c := range m.oauthConfigs() {
		if key != cfg.Key() && c.IdP() == cfg.IdP() && !c.Pinned() && m.authm.Tokens.Get(key) != nil {
			others = append(others, c)
		}
	}
	p := m.prompter()
	note := "switching to " + name
	if name == "" {
		note = "signing in to " + cfg.Issuer()
	}
	return m.run(note, func(ctx context.Context) tea.Msg {
		t, err := m.authm.SwitchUser(ctx, cfg, p, name)
		if err != nil {
			if errors.Is(err, errCancelled) {
				return statusMsg{text: "still " + orName(m.whoNow(cfg), "not signed in")}
			}
			return statusMsg{text: "switching users: " + err.Error(), isError: true}
		}
		who := orName(t.Login, t.Subject)
		for _, c := range others {
			_ = m.authm.Follow(ctx, c, who)
		}
		return statusMsg{text: "you are " + orName(t.Subject, who) + " now · " + cfg.Issuer()}
	})
}

// whoNow is who a login works as now.
func (m *Model) whoNow(cfg auth.Config) string {
	if t := m.authm.Tokens.Get(cfg.Key()); t != nil {
		return orName(t.Subject, t.Login)
	}
	return ""
}

// removeUser asks, then forgets a user of the current request's login.
func (m *Model) removeUser(u *usersModal, name string) tea.Cmd {
	cfg := u.cfg
	body := "posthero forgets their saved password,\nthe token it keeps for them and their session.\nTheir account at " + cfg.Issuer() + " stays as it is."
	if strings.EqualFold(m.whoNow(cfg), name) {
		body += "\n\n" + theme.Warn.Render("You work as "+name+" now: this logs you out.")
	}
	yes := func() tea.Msg {
		return cmdMsg{func() tea.Cmd {
			return m.run("removing "+name, func(context.Context) tea.Msg {
				m.authm.RemoveUser(cfg, name)
				return cmdMsg{func() tea.Cmd {
					u.reload()
					m.setStatus("removed "+name, false)
					return nil
				}}
			})
		}}
	}
	return modal.Show(&modal.Choice{
		Title:   "Remove " + name + "?",
		Body:    body,
		Danger:  true,
		Options: []modal.Option{{Key: "n", Label: "Keep"}, {Key: "y", Label: "Remove", Run: yes}},
	})
}

// oauthConfigs is every OAuth 2.0 login of the workspace that posthero
// gets tokens for, by token key, with its variables filled in.
func (m *Model) oauthConfigs() map[string]auth.Config {
	configs := map[string]auth.Config{}
	if m.st == nil {
		return configs
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
			if cfg := auth.ConfigFrom(auth.Resolve(a, set.Resolve)); cfg.Fetches() {
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
	return configs
}

// usersModal lists who a login can work as. It is made for vim hands:
// j and k move, ⏎ switches, a number picks, a adds, dd removes.
type usersModal struct {
	m       *Model
	cfg     auth.Config
	users   []auth.User
	sel     int
	pending string // the first key of gg or dd
	box     string
	top     int // the row of the first user in the box
}

func (u *usersModal) reload() {
	u.users = u.m.authm.Users(u.cfg)
	u.sel = max(0, min(u.sel, len(u.users)-1))
}

// close runs f after the dialog closes.
func (u *usersModal) close(f func() tea.Cmd) (bool, tea.Cmd) {
	return true, func() tea.Msg { return cmdMsg{f} }
}

func (u *usersModal) use(i int) (bool, tea.Cmd) {
	if i < 0 || i >= len(u.users) {
		return false, nil
	}
	name := u.users[i].Name
	if u.users[i].Current && u.users[i].Token.Valid(time.Now()) {
		return u.close(func() tea.Cmd { u.m.setStatus("you are "+name+" already", false); return nil })
	}
	return u.close(func() tea.Cmd { return u.m.switchUser(name) })
}

// Key implements modal.Modal.
func (u *usersModal) Key(k string) (bool, tea.Cmd) {
	pending := u.pending
	u.pending = ""
	n := len(u.users)
	switch k {
	case "<Esc>", "q", "<C-c>":
		return true, nil
	case "j", "<Down>", "<C-n>", "<Tab>":
		if n > 0 {
			u.sel = (u.sel + 1) % n
		}
	case "k", "<Up>", "<C-p>", "<S-Tab>":
		if n > 0 {
			u.sel = (u.sel - 1 + n) % n
		}
	case "g":
		if pending == "g" {
			u.sel = 0
		} else {
			u.pending = "g"
		}
	case "G":
		u.sel = max(0, n-1)
	case "<CR>", "l", " ":
		return u.use(u.sel)
	case "a", "o", "O", "i", "A", "I":
		return u.close(func() tea.Cmd { return u.m.switchUser("") })
	case "d":
		if pending != "d" {
			u.pending = "d"
			break
		}
		fallthrough
	case "D", "x":
		if u.sel < n {
			return false, u.m.removeUser(u, u.users[u.sel].Name)
		}
	default:
		if len(k) == 1 && k[0] >= '1' && k[0] <= '9' {
			return u.use(int(k[0] - '1'))
		}
	}
	return false, nil
}

// status says how working as a user goes from here.
func userStatus(us auth.User, at time.Time) string {
	t := us.Token
	switch {
	case us.Current && t.Valid(at):
		left := "signed in"
		if !t.ExpiresAt.IsZero() {
			left += " · " + shortDuration(t.ExpiresAt.Sub(at)) + " left"
		}
		return theme.Success.Render(left)
	case us.Current && t.CanRefresh(at):
		return theme.Warn.Render("signed in · refreshes on the next request")
	case us.Current:
		return theme.Warn.Render("signs in again on the next request")
	case us.Ready(at):
		return "ready · switches at once"
	case us.Password:
		return theme.Dim.Render("signs in with the saved password")
	}
	return theme.Dim.Render("asks for the password")
}

// View implements modal.Modal.
func (u *usersModal) View(maxW, _ int) string {
	now := time.Now()
	nameW := 4
	for _, us := range u.users {
		nameW = max(nameW, ansi.StringWidth(us.Name))
	}
	var rows []string
	for i, us := range u.users {
		num := "  "
		if i < 9 {
			num = fmt.Sprintf("%d ", i+1)
		}
		mark := "  "
		if us.Current {
			mark = theme.Success.Render("● ")
		}
		rows = append(rows, theme.Dim.Render(num)+mark+theme.Bold.Render(theme.Fit(us.Name, nameW))+"   "+userStatus(us, now))
	}
	w := 52
	for _, r := range rows {
		w = max(w, ansi.StringWidth(r)+2)
	}
	w = max(30, min(w, maxW-6))
	head := []string{
		theme.AccentFg.Bold(true).Render("Users"),
		theme.Dim.Render(theme.Truncate(u.cfg.Issuer()+" · client "+u.cfg.ClientID, w)),
		"",
	}
	u.top = 1 + len(head)
	var lines []string
	if len(u.users) == 0 {
		lines = append(lines, theme.Dim.Render("  Nobody has signed in here yet."), theme.Dim.Render("  Press a to sign in."))
	}
	for i, r := range rows {
		r = theme.Fit(r, w-1)
		if i == u.sel {
			lines = append(lines, theme.AccentFg.Render("▌")+theme.Selected.Render(ansi.Strip(r)))
		} else {
			lines = append(lines, " "+r)
		}
	}
	hint := "⏎ use · 1-9 pick · a add · dd remove · esc close"
	if len(u.users) == 0 {
		hint = "a sign in · esc close"
	}
	lines = append(lines, "", theme.Dim.Render(theme.Truncate(hint, w)))
	u.box = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(theme.Accent).Padding(0, 1).
		Render(strings.Join(head, "\n") + "\n" + strings.Join(lines, "\n"))
	return u.box
}

// Mouse implements modal.Mouser: a click on a user works as them, the
// wheel moves, and a click outside closes.
func (u *usersModal) Mouse(ev mouse.Event) (bool, tea.Cmd) {
	if dy, _ := ev.WheelDelta(); dy != 0 && len(u.users) > 0 {
		u.sel = max(0, min(len(u.users)-1, u.sel+dy))
		return false, nil
	}
	if ev.Action != mouse.Press || ev.Button != tea.MouseLeft {
		return false, nil
	}
	if !ev.In(lipgloss.Width(u.box), lipgloss.Height(u.box)) {
		return true, nil
	}
	if i := ev.Y - u.top; i >= 0 && i < len(u.users) {
		u.sel = i
		return u.use(i)
	}
	return false, nil
}

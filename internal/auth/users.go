package auth

import (
	"context"
	"os"
	"sort"
	"strings"
	"time"
)

// User is someone a config can log in as: a login saved at its identity
// provider, or who one of its tokens is for.
type User struct {
	Name string
	// Password says posthero has the user's password for this config's
	// logins, so it logs in as them by itself.
	Password bool
	// Current says the config's token is the user's now.
	Current bool
	// Last says the user logged in last at the identity provider: logins
	// that don't say who log in as them.
	Last bool
	// Token is the user's token for the config: the one in use when
	// Current, else the one kept since switching away from them. nil when
	// there is none.
	Token *Token
}

// Ready says switching to the user needs no login: their token is good,
// or can be refreshed.
func (u User) Ready(at time.Time) bool {
	return u.Token.Valid(at) || u.Token.CanRefresh(at)
}

// Pinned says a config always logs in as the same one: it names its user
// (a login_hint, or the password grant's settings), or has no user at all
// (client credentials). Switching users leaves it be, unless it is the
// one switched.
func (c Config) Pinned() bool {
	return c.Grant == GrantClient || c.Hint() != "" || c.Grant == GrantPassword && c.Password != ""
}

// HasUsers says the config logs in as a person, so it has users to switch
// between.
func (c Config) HasUsers() bool { return c.Fetches() && c.Grant != GrantClient }

// keptKey is where a config's token for a user is kept while another
// user's is in use.
func keptKey(c Config, user string) string { return c.Key() + "@" + strings.ToLower(user) }

// keptSession is where a user's identity provider session is kept while
// another user's is in use.
func keptSession(c Config, user string) string { return c.session() + "\x00" + strings.ToLower(user) }

// whose is who a token is for: who logged in, else the token's subject.
func whose(t *Token) string {
	if t == nil {
		return ""
	}
	return firstNonEmpty(t.Login, t.Subject)
}

// Users lists who a config can log in as, by name: the users saved at its
// identity provider, who its token is for, and those whose tokens were
// kept when switching away from them.
func (m *Manager) Users(c Config) []User {
	idp, key := c.IdP(), c.Key()
	byName := map[string]*User{}
	add := func(name string) *User {
		k := strings.ToLower(name)
		u, ok := byName[k]
		if !ok {
			u = &User{Name: name}
			byName[k] = u
		}
		return u
	}
	for _, s := range m.Logins.users(idp, c.tokenFrom()) {
		add(s.name).Password = s.password
	}
	prefix := key + "@"
	for _, t := range m.Tokens.All() {
		if strings.HasPrefix(t.Key, prefix) && whose(t) != "" {
			add(whose(t)).Token = t
		}
	}
	m.Tokens.Sync(key)
	if t := m.Tokens.Get(key); whose(t) != "" {
		u := add(whose(t))
		u.Current, u.Token = true, t
	}
	if last := m.Logins.Username(idp); last != "" {
		add(last).Last = true
	}
	out := make([]User, 0, len(byName))
	for _, u := range byName {
		out = append(out, *u)
	}
	sort.Slice(out, func(a, b int) bool {
		x, y := strings.ToLower(out[a].Name), strings.ToLower(out[b].Name)
		if x != y {
			return x < y
		}
		return out[a].Name < out[b].Name
	})
	return out
}

// SwitchUser makes a config log in as user from now on: with the token
// kept for them since switching away (refreshed when it ran out), else by
// logging in as them, with their saved password or asking p. The token of
// whoever was logged in is kept, with their session, so switching back
// needs no login. An empty user logs in as someone new, asking who.
func (m *Manager) SwitchUser(ctx context.Context, c Config, p Prompter, user string) (*Token, error) {
	if !c.Fetches() {
		return nil, missingSettings(c)
	}
	before := m.Tokens.Get(c.Key())
	restored, err := m.swap(ctx, c, user)
	if err != nil {
		return nil, err
	}
	if restored {
		t, _, err := m.Token(ctx, c, p, "")
		if err == nil || ctx.Err() != nil {
			return t, err
		}
		// The kept token is no good after all (its session ended at the
		// identity provider): log in as them.
	}
	var t *Token
	if user == "" {
		t, err = m.ForceLogin(ctx, c, p, newUser)
	} else {
		t, err = m.loginAs(ctx, c, p, user)
	}
	if err != nil {
		m.unswap(c, before)
	}
	return t, err
}

// loginAs logs a config in as user, with their saved password when there
// is one (unlike ForceLogin, which asks for it).
func (m *Manager) loginAs(ctx context.Context, c Config, p Prompter, user string) (*Token, error) {
	unlock, err := m.lock(ctx, c.Key())
	if err != nil {
		return nil, err
	}
	defer unlock()
	switch c.Grant {
	case GrantPassword:
		return m.loginPassword(ctx, c, p, user)
	case GrantCode, GrantPKCE, GrantImplicit:
		// Logged out of the session by swap: the login page asks, and is
		// filled in for user.
		return m.authorize(ctx, c, p, &Token{Login: user}, "")
	}
	return m.login(ctx, c, p, nil)
}

// unswap puts back who a config was logged in as, with their session, when
// logging in as someone else didn't happen (it failed, or was cancelled).
func (m *Manager) unswap(c Config, before *Token) {
	who := whose(before)
	if who == "" || !strings.EqualFold(whose(m.Tokens.Get(c.Key())), who) {
		return
	}
	m.Tokens.Delete(keptKey(c, who))
	_ = m.Sessions.write(c.session(), m.Sessions.Jar(keptSession(c, who)).All())
	m.Sessions.Drop(keptSession(c, who))
	m.Logins.Remember(c.IdP(), who)
}

// Follow makes a config log in as user too, after another config switched
// to them, without logging in now: it takes the token kept for user, or
// lets go of the one it has (keeping it for its user), so its next request
// logs in as user, who logged in last.
func (m *Manager) Follow(ctx context.Context, c Config, user string) error {
	restored, err := m.swap(ctx, c, user)
	if err != nil || restored {
		return err
	}
	m.Tokens.Delete(c.Key())
	m.Sessions.Clear(c.session())
	return nil
}

// swap keeps the token and session of whoever a config is logged in as,
// and puts user's kept ones in their place. restored says user's token is
// in place now (or was already); otherwise user has to log in.
func (m *Manager) swap(ctx context.Context, c Config, user string) (restored bool, err error) {
	key := c.Key()
	unlock, err := m.lock(ctx, key)
	if err != nil {
		return false, err
	}
	defer unlock()
	m.Tokens.Sync(key)
	cur := m.Tokens.Get(key)
	who := whose(cur)
	if user != "" && strings.EqualFold(who, user) {
		return true, nil
	}
	if who != "" {
		kept := *cur
		kept.Key = keptKey(c, who)
		// Kept now: newer than when another switch took it last.
		kept.IssuedAt = time.Now()
		m.Tokens.Put(&kept)
		_ = m.Sessions.write(keptSession(c, who), m.Sessions.Jar(c.session()).All())
	}
	if user == "" {
		m.Sessions.Clear(c.session())
		return false, nil
	}
	m.Logins.Remember(c.IdP(), user)
	at := m.now()
	kept := m.Tokens.Get(keptKey(c, user))
	if kept == nil || !kept.Valid(at) && !kept.CanRefresh(at) {
		m.Sessions.Clear(c.session())
		return false, nil
	}
	t := *kept
	t.Key = key
	t.IssuedAt = time.Now()
	t.LastUsed = at
	m.Tokens.Put(&t)
	m.Tokens.Delete(keptKey(c, user))
	_ = m.Sessions.write(c.session(), m.Sessions.Jar(keptSession(c, user)).All())
	m.Sessions.Drop(keptSession(c, user))
	return true, nil
}

// RemoveUser forgets a user at a config's identity provider: the saved
// password, the token kept for them and their session. When the config is
// logged in as them, it is logged out. The account at the identity
// provider stays as it is.
func (m *Manager) RemoveUser(c Config, user string) {
	if strings.EqualFold(whose(m.Tokens.Get(c.Key())), user) {
		m.Forget(c, false)
	}
	if t := m.Tokens.Get(keptKey(c, user)); t != nil {
		if t.RefreshToken != "" {
			m.logoutRemote(c, t)
		}
		m.Tokens.Delete(keptKey(c, user))
	}
	m.Sessions.Drop(keptSession(c, user))
	m.Logins.Forget(c.IdP(), user)
}

// savedUser is a user saved at an identity provider.
type savedUser struct {
	name     string
	password bool // for logins that send to tokenURL
}

// users lists the users saved at an identity provider, saying whether
// their password is filled in for a login that sends what it gets to
// tokenURL.
func (l *Logins) users(idp, tokenURL string) []savedUser {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sync()
	var out []savedUser
	for key, s := range l.all {
		if !strings.HasPrefix(key, idp+"\x00") || s.gone() || s.Username == "" {
			continue
		}
		out = append(out, savedUser{name: s.Username, password: (s.Keyring || s.Password != "") && s.sendsTo(idp, tokenURL)})
	}
	return out
}

// Drop forgets a session's cookies, here and on disk.
func (s *Sessions) Drop(name string) {
	s.mu.Lock()
	delete(s.jars, name)
	s.mu.Unlock()
	if f := s.file(name); f != "" {
		_ = os.Remove(f)
	}
}

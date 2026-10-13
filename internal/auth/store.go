package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/shinyvision/posthero/internal/jsonx"
	"github.com/shinyvision/posthero/internal/secret"
	"github.com/shinyvision/posthero/internal/send"
)

// Token is an OAuth 2.0 token posthero got, kept between runs.
type Token struct {
	Key          string    `json:"key"`
	Name         string    `json:"name,omitempty"`
	AccessToken  string    `json:"accessToken"`
	RefreshToken string    `json:"refreshToken,omitempty"`
	IDToken      string    `json:"idToken,omitempty"`
	TokenType    string    `json:"tokenType,omitempty"`
	Scope        string    `json:"scope,omitempty"`
	ExpiresAt    time.Time `json:"expiresAt,omitzero"`
	// RefreshExpiresAt is when the refresh token stops working; zero is
	// unknown (or never).
	RefreshExpiresAt time.Time `json:"refreshExpiresAt,omitzero"`
	ObtainedAt       time.Time `json:"obtainedAt"`
	// IssuedAt is when this access token came, by login or refresh.
	IssuedAt time.Time `json:"issuedAt,omitzero"`
	LastUsed time.Time `json:"lastUsed,omitzero"`
	// Subject is who the token is for.
	Subject string `json:"subject,omitempty"`
	// TokenURL, ClientID and Grant say where it came from.
	TokenURL string `json:"tokenUrl,omitempty"`
	ClientID string `json:"clientId,omitempty"`
	Grant    string `json:"grant,omitempty"`
	// Issuer is the identity provider (its realm, for Keycloak).
	Issuer string `json:"issuer,omitempty"`
	// Login is the user name posthero logged in with, to log in the same way
	// next time.
	Login string `json:"login,omitempty"`
	// Refreshed counts the refreshes since the token was obtained.
	Refreshed int `json:"refreshed,omitempty"`
}

// slack is how long before it ends a token counts as ended, so it never
// runs out in the middle of a request.
const slack = 30 * time.Second

// Valid reports whether the access token can be used now.
func (t *Token) Valid(at time.Time) bool {
	return t != nil && t.AccessToken != "" && (t.ExpiresAt.IsZero() || t.ExpiresAt.After(at.Add(slack)))
}

// dueForRefresh reports whether a token should be refreshed in the
// background now: in its last 90 seconds, or in the second half of a
// short life (a one-minute token is refreshed every half minute or so, not
// on every check).
func (t *Token) dueForRefresh(at time.Time) bool {
	if t == nil || t.ExpiresAt.IsZero() {
		return false
	}
	ahead := 90 * time.Second
	if !t.IssuedAt.IsZero() {
		if life := t.ExpiresAt.Sub(t.IssuedAt); life > 0 {
			ahead = min(ahead, life/2)
		}
	}
	return t.ExpiresAt.Sub(at) <= max(ahead, slack)
}

// CanRefresh reports whether the refresh token can be used now.
func (t *Token) CanRefresh(at time.Time) bool {
	return t != nil && t.RefreshToken != "" && (t.RefreshExpiresAt.IsZero() || t.RefreshExpiresAt.After(at.Add(5*time.Second)))
}

// tokenFile keeps tokens: Store.
type tokenFile struct {
	Tokens map[string]*Token `json:"tokens"`
	// Deleted says when tokens were forgotten (logged out), so another
	// posthero doesn't bring back an older one.
	Deleted map[string]time.Time `json:"deleted,omitempty"`
}

// Store keeps tokens in a file readable only by the user. Several posthero
// instances can share the file: each write merges with what is there, and
// a token another instance refreshed is taken before refreshing it again
// (a refresh token can often be used only once). Logging out leaves a note
// in the file, so the other instances let go of the token too.
type Store struct {
	mu     sync.Mutex
	path   string
	tokens map[string]*Token
	// deleted are tokens forgotten, and when.
	deleted map[string]time.Time
}

// NewStore reads the token file; an empty path keeps tokens in memory.
func NewStore(path string) *Store {
	s := &Store{path: path, tokens: map[string]*Token{}, deleted: map[string]time.Time{}}
	if f, ok := s.read(); ok {
		for k, t := range f.Tokens {
			if !gone(f.Deleted, k, t) {
				s.tokens[k] = t
			}
		}
		for k, at := range f.Deleted {
			s.deleted[k] = at
		}
	}
	return s
}

// read reads the file.
func (s *Store) read() (tokenFile, bool) {
	var f tokenFile
	if s.path == "" {
		return f, false
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		return f, false
	}
	if json.Unmarshal(data, &f) != nil || f.Tokens == nil {
		return f, false
	}
	return f, true
}

// issued is when a token's access token came.
func issued(t *Token) time.Time {
	if !t.IssuedAt.IsZero() {
		return t.IssuedAt
	}
	return t.ObtainedAt
}

// gone reports whether a token was forgotten after it came.
func gone(deleted map[string]time.Time, key string, t *Token) bool {
	at, ok := deleted[key]
	return ok && !issued(t).After(at)
}

// Sync takes a token from the file when another instance got a newer one,
// and lets go of one another instance forgot.
func (s *Store) Sync(key string) {
	f, ok := s.read()
	if !ok {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, have := s.tokens[key]
	if have && gone(f.Deleted, key, cur) {
		delete(s.tokens, key)
		s.deleted[key] = f.Deleted[key]
		return
	}
	disk := f.Tokens[key]
	if disk == nil || gone(f.Deleted, key, disk) || gone(s.deleted, key, disk) {
		return
	}
	if !have || issued(disk).After(issued(cur)) {
		if have && cur.LastUsed.After(disk.LastUsed) {
			disk.LastUsed = cur.LastUsed
		}
		s.tokens[key] = disk
	}
}

// Get returns a copy of a token.
func (s *Store) Get(key string) *Token {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tokens[key]
	if !ok {
		return nil
	}
	c := *t
	return &c
}

// Put saves a token.
func (s *Store) Put(t *Token) {
	s.mu.Lock()
	c := *t
	if issued(&c).IsZero() {
		c.IssuedAt = time.Now()
	}
	s.tokens[t.Key] = &c
	s.mu.Unlock()
	_ = s.save()
}

// Touch marks a token as just used.
func (s *Store) Touch(key string, at time.Time) {
	s.mu.Lock()
	if t, ok := s.tokens[key]; ok {
		t.LastUsed = at
	}
	s.mu.Unlock()
}

// Delete forgets a token, here and in the other instances.
func (s *Store) Delete(key string) {
	s.mu.Lock()
	delete(s.tokens, key)
	s.deleted[key] = time.Now()
	s.mu.Unlock()
	_ = s.save()
}

// All returns every token, most recently used first.
func (s *Store) All() []*Token {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Token, 0, len(s.tokens))
	for _, t := range s.tokens {
		c := *t
		out = append(out, &c)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].LastUsed.After(out[b].LastUsed) })
	return out
}

// Save writes the tokens to the file (Touch doesn't, to spare the disk).
func (s *Store) Save() error { return s.save() }

// save merges the tokens with the file's, keeping the newer of each and
// leaving out forgotten ones, and writes them.
func (s *Store) save() error {
	if s.path == "" {
		return nil
	}
	unlock := lockFile(s.path)
	defer unlock()
	disk, _ := s.read()
	s.mu.Lock()
	deleted := map[string]time.Time{}
	for k, at := range disk.Deleted {
		deleted[k] = at
	}
	for k, at := range s.deleted {
		if at.After(deleted[k]) {
			deleted[k] = at
		}
	}
	merged := map[string]*Token{}
	for k, t := range disk.Tokens {
		if !gone(deleted, k, t) {
			merged[k] = t
		}
	}
	for k, t := range s.tokens {
		if gone(deleted, k, t) {
			delete(s.tokens, k) // another instance logged out
			continue
		}
		if d, ok := merged[k]; ok && issued(d).After(issued(t)) {
			// Another instance got a newer one: take it here too.
			if t.LastUsed.After(d.LastUsed) {
				d.LastUsed = t.LastUsed
			}
			s.tokens[k] = d
			continue
		}
		merged[k] = t
	}
	// Notes older than a month have done their work.
	for k, at := range deleted {
		if time.Since(at) > 30*24*time.Hour {
			delete(deleted, k)
		}
	}
	s.deleted = deleted
	data, err := jsonx.MarshalIndent(tokenFile{Tokens: merged, Deleted: deleted}, "", "  ")
	s.mu.Unlock()
	if err != nil {
		return err
	}
	return writePrivate(s.path, data)
}

// lockFile takes a lock next to path for a moment, so two posthero
// instances don't write a file at the same time. A lock left behind by one
// that died is taken over after a few seconds.
func lockFile(path string) (unlock func()) {
	lock := path + ".lock"
	_ = os.MkdirAll(filepath.Dir(lock), 0o700)
	for i := 0; i < 200; i++ {
		f, err := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			f.Close()
			return func() { os.Remove(lock) }
		}
		if fi, err := os.Stat(lock); err == nil && time.Since(fi.ModTime()) > 5*time.Second {
			os.Remove(lock)
			continue
		}
		time.Sleep(10 * time.Millisecond)
	}
	return func() {}
}

func writePrivate(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Logins remembers how to log in to identity providers: per identity
// provider, the users that logged in there and who did last; per user, the
// password in the system keyring (or, without one, in the file, readable
// only by the user). Several posthero instances can share the file: each
// write merges with what is there, keeping the newer of each login, and
// forgetting one leaves a note in the file, so the others let go of it too.
type Logins struct {
	mu   sync.Mutex
	path string
	// all holds an entry per user, keyed by loginKey(idp, user), and per
	// identity provider (keyed by idp) one naming the last user. One that
	// was forgotten stays a while as a note of when.
	all map[string]*savedLogin
}

// savedLogin is a login kept. Entries in Logins are never changed, only
// replaced, so a copy can be read without the lock.
type savedLogin struct {
	Username string `json:"username"`
	Keyring  bool   `json:"keyring,omitempty"`
	Password string `json:"password,omitempty"`
	// TokenServers are where the logins this password was saved with sent
	// what they got for a token (the code, or the password itself), as
	// scheme://host:port. The password is filled in by itself only for
	// logins that send to one of them, or to the identity provider itself.
	TokenServers []string `json:"tokenServers,omitempty"`
	// Saved is when it was saved or last changed.
	Saved time.Time
	// Deleted is when it was forgotten.
	Deleted time.Time `json:"deleted,omitzero"`
}

func (s *savedLogin) gone() bool { return !s.Deleted.IsZero() }

// changed is when a login was last saved, changed or forgotten.
func (s *savedLogin) changed() time.Time {
	if s.gone() {
		return s.Deleted
	}
	return s.Saved
}

// sendsTo reports whether a saved password may be filled in by itself for
// a login that sends what it gets to tokenURL: a server it was saved for,
// or the identity provider's own, which gets the password anyway. (Logins
// saved before posthero kept their servers have only the latter.)
func (s *savedLogin) sendsTo(idp, tokenURL string) bool {
	if sameServer(tokenURL, idp) {
		return true
	}
	for _, srv := range s.TokenServers {
		if sameServer(tokenURL, srv) {
			return true
		}
	}
	return false
}

// server is the scheme, host and port of an address, as TokenServers keeps
// them.
func server(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + net.JoinHostPort(strings.ToLower(u.Hostname()), port(u))
}

func loginKey(idp, user string) string { return idp + "\x00" + strings.ToLower(user) }

// NewLogins reads the logins file; an empty path keeps them in memory.
func NewLogins(path string) *Logins {
	return &Logins{path: path, all: readLogins(path)}
}

// readLogins reads a logins file. Files from before logins were kept per
// user hold one login per identity provider: it becomes that user's, and
// the last one.
func readLogins(path string) map[string]*savedLogin {
	all := map[string]*savedLogin{}
	if path == "" {
		return all
	}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &all)
	}
	if all == nil {
		return map[string]*savedLogin{}
	}
	for key, s := range all {
		if strings.Contains(key, "\x00") || s.gone() || s.Username == "" || !s.Keyring && s.Password == "" {
			continue
		}
		if _, ok := all[loginKey(key, s.Username)]; !ok {
			copied := *s
			all[loginKey(key, s.Username)] = &copied
		}
		all[key] = &savedLogin{Username: s.Username, Saved: s.Saved}
	}
	return all
}

// mergeLogins combines two sets of logins, keeping the newer of each (b's,
// when they are as new): a login forgotten after it was saved stays
// forgotten, and one saved after it was forgotten is back.
func mergeLogins(a, b map[string]*savedLogin) map[string]*savedLogin {
	out := make(map[string]*savedLogin, len(a))
	for k, s := range a {
		out[k] = s
	}
	for k, s := range b {
		if o, ok := out[k]; !ok || !o.changed().After(s.changed()) {
			out[k] = s
		}
	}
	// Notes older than a month have done their work.
	for k, s := range out {
		if s.gone() && time.Since(s.Deleted) > 30*24*time.Hour {
			delete(out, k)
		}
	}
	return out
}

// sync takes in what other posthero instances saved and forgot. Callers
// hold l.mu.
func (l *Logins) sync() {
	if l.path != "" {
		l.all = mergeLogins(readLogins(l.path), l.all)
	}
}

// entry returns a login kept under key, unless it was forgotten.
func (l *Logins) entry(key string) (savedLogin, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sync()
	s, ok := l.all[key]
	if !ok || s.gone() {
		return savedLogin{}, false
	}
	return *s, true
}

func keyringID(idp, username string) string { return "login " + username + " at " + idp }

// Get returns the saved login of the user who logged in last at an
// identity provider, for a login that sends what it gets to tokenURL (see
// Password).
func (l *Logins) Get(idp, tokenURL string) (username, password string, ok bool) {
	user := l.Username(idp)
	if user == "" {
		return "", "", false
	}
	pw, ok := l.Password(idp, tokenURL, user)
	return user, pw, ok
}

// Password returns a user's saved password at an identity provider, for a
// login that sends what it gets for a token (the code, or the password
// itself) to tokenURL: only when the password was saved by a login that
// sent to that server, or it is the identity provider's own. Another
// server would get what the password unlocks without the person knowing.
func (l *Logins) Password(idp, tokenURL, user string) (string, bool) {
	pw, ok, _ := l.password(idp, tokenURL, user)
	return pw, ok
}

// password also says when there is a saved password, but not for tokenURL.
func (l *Logins) password(idp, tokenURL, user string) (pw string, ok, elsewhere bool) {
	s, found := l.entry(loginKey(idp, user))
	if !found || !s.Keyring && s.Password == "" {
		return "", false, false
	}
	if !s.sendsTo(idp, tokenURL) {
		return "", false, true
	}
	if !s.Keyring {
		return s.Password, true, false
	}
	pw, err := secret.Get(keyringID(idp, s.Username))
	if err != nil {
		return "", false, false
	}
	return pw, true, false
}

// Username returns the user name last used with an identity provider.
func (l *Logins) Username(idp string) string {
	s, _ := l.entry(idp)
	return s.Username
}

// Save remembers a login, as the last one at the identity provider, for
// logins that send what they get to tokenURL (besides those it was saved
// for before). note says where the password went when it couldn't go in
// the keyring.
func (l *Logins) Save(idp, tokenURL, username, password string) (note string, err error) {
	s := &savedLogin{Username: username, Saved: time.Now()}
	if err := secret.Set(keyringID(idp, username), password); err == nil {
		s.Keyring = true
	} else {
		s.Password = password
		note = "no system keyring here, so the password is kept in " + l.path + " (readable only by you)"
	}
	key := loginKey(idp, username)
	l.mu.Lock()
	l.sync()
	if old, ok := l.all[key]; ok && !old.gone() {
		s.TokenServers = append(s.TokenServers, old.TokenServers...)
	}
	if srv := server(tokenURL); srv != "" && !slices.Contains(s.TokenServers, srv) {
		s.TokenServers = append(s.TokenServers, srv)
	}
	l.all[key] = s
	l.all[idp] = &savedLogin{Username: username, Saved: s.Saved}
	l.mu.Unlock()
	return note, l.save()
}

// Remember keeps the user name as the last one (its password wasn't to
// be saved).
func (l *Logins) Remember(idp, username string) {
	l.mu.Lock()
	l.sync()
	if s, ok := l.all[idp]; ok && !s.gone() && s.Username == username {
		l.mu.Unlock()
		return
	}
	l.all[idp] = &savedLogin{Username: username, Saved: time.Now()}
	l.mu.Unlock()
	_ = l.save()
}

// Forget drops a user's saved login at an identity provider; an empty
// user drops every login there.
func (l *Logins) Forget(idp, user string) {
	now := time.Now()
	l.mu.Lock()
	l.sync()
	for key, s := range l.all {
		if key == idp || !strings.HasPrefix(key, idp+"\x00") || s.gone() {
			continue
		}
		if user != "" && key != loginKey(idp, user) {
			continue
		}
		if s.Keyring {
			_ = secret.Delete(keyringID(idp, s.Username))
		}
		l.all[key] = &savedLogin{Deleted: now}
	}
	if last, ok := l.all[idp]; ok && !last.gone() && (user == "" || strings.EqualFold(last.Username, user)) {
		l.all[idp] = &savedLogin{Deleted: now}
	}
	l.mu.Unlock()
	_ = l.save()
}

// ForgetPassword keeps a user's name but not the password, after it
// stopped working.
func (l *Logins) ForgetPassword(idp, user string) {
	key := loginKey(idp, user)
	l.mu.Lock()
	l.sync()
	s, ok := l.all[key]
	ok = ok && !s.gone() && (s.Keyring || s.Password != "")
	if ok {
		if s.Keyring {
			_ = secret.Delete(keyringID(idp, s.Username))
		}
		l.all[key] = &savedLogin{Username: s.Username, TokenServers: s.TokenServers, Saved: time.Now()}
	}
	l.mu.Unlock()
	if ok {
		_ = l.save()
	}
}

// save merges the logins with the file's, keeping the newer of each, and
// writes them.
func (l *Logins) save() error {
	if l.path == "" {
		return nil
	}
	unlock := lockFile(l.path)
	defer unlock()
	l.mu.Lock()
	l.sync()
	data, err := jsonx.MarshalIndent(l.all, "", "  ")
	l.mu.Unlock()
	if err != nil {
		return err
	}
	return writePrivate(l.path, data)
}

// Sessions keeps the cookies of identity providers (the login session),
// one jar each, so posthero can log in again without a password while the
// session lasts. Several posthero instances share the files: a login
// starts from the cookies any of them saved last, so a session ended in
// one is ended in all.
type Sessions struct {
	mu   sync.Mutex
	dir  string
	jars map[string]*send.Jar
}

// NewSessions keeps jars in dir; empty keeps them in memory.
func NewSessions(dir string) *Sessions {
	return &Sessions{dir: dir, jars: map[string]*send.Jar{}}
}

// file is where a session's cookies are kept.
func (s *Sessions) file(idp string) string {
	if s.dir == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(idp))
	return filepath.Join(s.dir, hex.EncodeToString(sum[:8])+".json")
}

// Jar returns the cookie jar of an identity provider, with the cookies
// last saved for it.
func (s *Sessions) Jar(idp string) *send.Jar {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jars[idp]
	if !ok {
		j = send.NewJar("")
		s.jars[idp] = j
	}
	if f := s.file(idp); f != "" {
		if data, err := os.ReadFile(f); err == nil {
			var cookies []send.Cookie
			if json.Unmarshal(data, &cookies) == nil {
				j.Replace(cookies)
			}
		}
	}
	return j
}

// Save writes an identity provider's cookies.
func (s *Sessions) Save(idp string) error {
	s.mu.Lock()
	j := s.jars[idp]
	s.mu.Unlock()
	if j == nil {
		return nil
	}
	return s.write(idp, j.All())
}

// Clear ends an identity provider's session here.
func (s *Sessions) Clear(idp string) {
	s.mu.Lock()
	if j, ok := s.jars[idp]; ok {
		j.Replace(nil)
	}
	s.mu.Unlock()
	_ = s.write(idp, nil)
}

func (s *Sessions) write(idp string, cookies []send.Cookie) error {
	f := s.file(idp)
	if f == "" {
		return nil
	}
	if cookies == nil {
		cookies = []send.Cookie{}
	}
	data, err := jsonx.MarshalIndent(cookies, "", "  ")
	if err != nil {
		return err
	}
	unlock := lockFile(f)
	defer unlock()
	return writePrivate(f, data)
}

var errNoUser = errors.New("posthero has to ask you something to log in, and can't right now")

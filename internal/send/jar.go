package send

import (
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/publicsuffix"

	"github.com/shinyvision/posthero/internal/jsonx"
)

// Cookie is a cookie kept in the jar.
type Cookie struct {
	Name     string    `json:"name"`
	Value    string    `json:"value"`
	Domain   string    `json:"domain"`
	Path     string    `json:"path"`
	HostOnly bool      `json:"hostOnly,omitempty"`
	Expires  time.Time `json:"expires,omitzero"`
	Secure   bool      `json:"secure,omitempty"`
	HTTPOnly bool      `json:"httpOnly,omitempty"`
	SameSite string    `json:"sameSite,omitempty"`
	Created  time.Time `json:"created"`
}

// Expired reports whether the cookie has expired.
func (c *Cookie) Expired(now time.Time) bool { return !c.Expires.IsZero() && !c.Expires.After(now) }

// Jar keeps cookies across requests, and across runs when it has a file:
// Postman's cookie manager.
type Jar struct {
	mu      sync.Mutex
	cookies []*Cookie
	file    string
	now     func() time.Time
	// changed are the cookies (by name, domain and path) this posthero set
	// or removed since it last read the file: what it writes over another
	// instance's.
	changed map[string]bool
}

// NewJar returns an empty jar, saved to file when it is not empty.
func NewJar(file string) *Jar {
	j := &Jar{file: file, now: time.Now, changed: map[string]bool{}}
	if file != "" {
		if data, err := os.ReadFile(file); err == nil {
			_ = json.Unmarshal(data, &j.cookies)
		}
	}
	return j
}

// Save writes the jar to its file. Another posthero may have saved cookies
// meanwhile: the file is read again and only what this one changed is
// written over it, so neither loses the other's.
func (j *Jar) Save() error {
	if j == nil || j.file == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(j.file), 0o700); err != nil {
		return err
	}
	unlock := lockFile(j.file)
	defer unlock()
	var disk []*Cookie
	if data, err := os.ReadFile(j.file); err == nil {
		_ = json.Unmarshal(data, &disk)
	}
	j.mu.Lock()
	merged := make([]*Cookie, 0, len(disk)+len(j.cookies))
	for _, c := range disk {
		if !j.changed[cookieKey(c)] {
			merged = append(merged, c)
		}
	}
	for _, c := range j.cookies {
		if j.changed[cookieKey(c)] {
			merged = append(merged, c)
		}
	}
	j.cookies, j.changed = merged, map[string]bool{}
	j.dropExpired()
	data, err := jsonx.MarshalIndent(j.cookies, "", "  ")
	j.mu.Unlock()
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(j.file), filepath.Base(j.file)+".*.tmp")
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(f.Name(), 0o600)
	}
	if err != nil {
		os.Remove(f.Name())
		return err
	}
	return os.Rename(f.Name(), j.file)
}

func cookieKey(c *Cookie) string { return c.Name + "\x00" + c.Domain + "\x00" + c.Path }

// lockFile takes a lock next to path for a moment, so two posthero
// instances don't write a file at the same time. A lock left behind by one
// that died is taken over after a few seconds.
func lockFile(path string) (unlock func()) {
	lock := path + ".lock"
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

func (j *Jar) dropExpired() {
	now := j.now()
	out := j.cookies[:0]
	for _, c := range j.cookies {
		if !c.Expired(now) {
			out = append(out, c)
		}
	}
	j.cookies = out
}

// SetCookies stores the cookies a response from u set.
func (j *Jar) SetCookies(u *url.URL, cookies []*http.Cookie) {
	if j == nil {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	host := canonicalHost(u.Host)
	now := j.now()
	for _, hc := range cookies {
		c := &Cookie{Name: hc.Name, Value: hc.Value, Path: hc.Path, Secure: hc.Secure, HTTPOnly: hc.HttpOnly, Created: now}
		switch hc.SameSite {
		case http.SameSiteLaxMode:
			c.SameSite = "Lax"
		case http.SameSiteStrictMode:
			c.SameSite = "Strict"
		case http.SameSiteNoneMode:
			c.SameSite = "None"
		}
		domain := strings.TrimPrefix(strings.ToLower(hc.Domain), ".")
		switch {
		case domain == "":
			c.Domain, c.HostOnly = host, true
		case domainMatch(host, domain):
			// A cookie for a public suffix (co.uk) belongs to the host alone.
			if etld, err := publicsuffix.EffectiveTLDPlusOne(domain); err != nil || etld == "" {
				if domain != host {
					continue
				}
				c.HostOnly = true
			}
			c.Domain = domain
		default:
			continue // not ours to set
		}
		if c.Path == "" || c.Path[0] != '/' {
			c.Path = defaultPath(u.Path)
		}
		switch {
		case hc.MaxAge < 0:
			c.Expires = now.Add(-time.Second)
		case hc.MaxAge > 0:
			c.Expires = now.Add(time.Duration(hc.MaxAge) * time.Second)
		case !hc.Expires.IsZero():
			c.Expires = hc.Expires
		}
		j.put(c)
	}
	j.dropExpired()
}

// put replaces the cookie with the same name, domain and path, keeping
// when it was first made.
func (j *Jar) put(c *Cookie) {
	j.changed[cookieKey(c)] = true
	for i, old := range j.cookies {
		if old.Name == c.Name && old.Domain == c.Domain && old.Path == c.Path {
			c.Created = old.Created
			j.cookies[i] = c
			return
		}
	}
	j.cookies = append(j.cookies, c)
}

// Cookies returns the cookies to send to u, longest path first.
func (j *Jar) Cookies(u *url.URL) []*http.Cookie {
	if j == nil {
		return nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	host := canonicalHost(u.Host)
	path := u.Path
	if path == "" {
		path = "/"
	}
	secure := u.Scheme == "https" || u.Scheme == "wss" || isLocal(host)
	now := j.now()
	var match []*Cookie
	for _, c := range j.cookies {
		if c.Expired(now) || c.Secure && !secure {
			continue
		}
		if c.HostOnly && host != c.Domain || !c.HostOnly && !domainMatch(host, c.Domain) {
			continue
		}
		if !pathMatch(path, c.Path) {
			continue
		}
		match = append(match, c)
	}
	sort.SliceStable(match, func(a, b int) bool {
		if len(match[a].Path) != len(match[b].Path) {
			return len(match[a].Path) > len(match[b].Path)
		}
		return match[a].Created.Before(match[b].Created)
	})
	out := make([]*http.Cookie, len(match))
	for i, c := range match {
		out[i] = &http.Cookie{Name: c.Name, Value: c.Value}
	}
	return out
}

// All returns every cookie, by domain and name.
func (j *Jar) All() []Cookie {
	if j == nil {
		return nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	j.dropExpired()
	out := make([]Cookie, len(j.cookies))
	for i, c := range j.cookies {
		out[i] = *c
	}
	sort.SliceStable(out, func(a, b int) bool {
		if out[a].Domain != out[b].Domain {
			return out[a].Domain < out[b].Domain
		}
		return out[a].Name < out[b].Name
	})
	return out
}

// Replace sets the jar's cookies to these (the cookie editor's result).
func (j *Jar) Replace(cookies []Cookie) {
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, c := range j.cookies {
		j.changed[cookieKey(c)] = true // gone, unless it's among the new ones
	}
	j.cookies = nil
	now := j.now()
	for _, c := range cookies {
		c := c
		if c.Created.IsZero() {
			c.Created = now
		}
		if c.Path == "" {
			c.Path = "/"
		}
		j.changed[cookieKey(&c)] = true
		j.cookies = append(j.cookies, &c)
	}
}

// Set stores one cookie for a domain (scripts' pm.cookies.jar().set).
func (j *Jar) Set(domain string, c Cookie) {
	j.mu.Lock()
	defer j.mu.Unlock()
	c.Domain = canonicalHost(domain)
	if c.Path == "" {
		c.Path = "/"
	}
	c.HostOnly = true
	if c.Created.IsZero() {
		c.Created = j.now()
	}
	j.put(&c)
}

// Delete removes the cookies of a domain, only the one named name when it
// is not empty.
func (j *Jar) Delete(domain, name string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	domain = canonicalHost(domain)
	out := j.cookies[:0]
	for _, c := range j.cookies {
		if (c.Domain == domain || domainMatch(domain, c.Domain)) && (name == "" || c.Name == name) {
			j.changed[cookieKey(c)] = true
			continue
		}
		out = append(out, c)
	}
	j.cookies = out
}

func canonicalHost(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.ToLower(strings.TrimSuffix(strings.Trim(host, "[]"), "."))
}

func isLocal(host string) bool {
	return host == "localhost" || strings.HasSuffix(host, ".localhost") || host == "127.0.0.1" || host == "::1"
}

func domainMatch(host, domain string) bool {
	if host == domain {
		return true
	}
	if net.ParseIP(host) != nil {
		return false
	}
	return strings.HasSuffix(host, "."+domain)
}

func pathMatch(reqPath, cookiePath string) bool {
	if reqPath == cookiePath {
		return true
	}
	if strings.HasPrefix(reqPath, cookiePath) {
		return strings.HasSuffix(cookiePath, "/") || reqPath[len(cookiePath)] == '/'
	}
	return false
}

func defaultPath(p string) string {
	if p == "" || p[0] != '/' {
		return "/"
	}
	i := strings.LastIndex(p, "/")
	if i == 0 {
		return "/"
	}
	return p[:i]
}

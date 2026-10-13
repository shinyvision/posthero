package history

import (
	"bytes"
	"encoding/json"
	"net/url"
	"sort"
	"strings"

	"github.com/shinyvision/posthero/internal/jsonx"
	"github.com/shinyvision/posthero/internal/postman"
	"github.com/shinyvision/posthero/internal/script"
	"github.com/shinyvision/posthero/internal/send"
)

// Masked is what a secret is replaced with.
const Masked = "••••"

// minSecret is the shortest secret that is masked: shorter ones would mask
// half of every text (the screen leaves them alone too).
const minSecret = 4

// Redact masks secrets in a record, for keeping it on disk:
//   - the values of the headers the auth added (an Authorization header
//     keeps its scheme: "Basic ••••");
//   - the query parameters and tokens the auth added, when the record came
//     from FromExecution;
//   - every occurrence of the given secrets (the values of secret
//     variables), as they are or encoded for a URL or JSON, in the URL, the
//     redirects, the headers, the bodies, the cookies, the console and the
//     errors. Secrets shorter than 4 characters are left alone.
//
// It gives r new lists rather than changing the ones it has, so a copy of a
// record can be redacted without touching the original.
func Redact(r *Record, secrets []string) {
	m := newMasker(append(append([]string(nil), secrets...), r.authSecrets...))
	r.URL = m.text(maskQuery(r.URL, r.authQuery))
	r.RequestHeaders = maskHeaders(r.RequestHeaders, m)
	r.Headers = maskHeaders(r.Headers, m)
	r.RequestBody = m.text(r.RequestBody)
	r.Body = m.bytes(r.Body)
	r.Error = m.text(r.Error)
	if len(r.Redirects) > 0 {
		out := make([]send.Redirect, len(r.Redirects))
		for i, rd := range r.Redirects {
			rd.From, rd.To = m.text(maskQuery(rd.From, r.authQuery)), m.text(maskQuery(rd.To, r.authQuery))
			out[i] = rd
		}
		r.Redirects = out
	}
	if len(m.olds) == 0 {
		return
	}
	if len(r.Cookies) > 0 {
		out := make([]Cookie, len(r.Cookies))
		for i, c := range r.Cookies {
			c.Value = m.text(c.Value)
			out[i] = c
		}
		r.Cookies = out
	}
	if len(r.Logs) > 0 {
		out := make([]script.Log, len(r.Logs))
		for i, l := range r.Logs {
			l.Text = m.text(l.Text)
			out[i] = l
		}
		r.Logs = out
	}
	if len(r.Tests) > 0 {
		out := make([]script.Test, len(r.Tests))
		for i, t := range r.Tests {
			t.Name, t.Error = m.text(t.Name), m.text(t.Error)
			out[i] = t
		}
		r.Tests = out
	}
	if len(r.ScriptErrs) > 0 {
		out := make([]string, len(r.ScriptErrs))
		for i, e := range r.ScriptErrs {
			out[i] = m.text(e)
		}
		r.ScriptErrs = out
	}
}

// masker replaces secrets with Masked.
type masker struct {
	olds []string
	r    *strings.Replacer
}

// newMasker masks each secret as it is and as a URL or JSON writes it,
// the longest first, so a secret that holds another is masked whole.
func newMasker(secrets []string) masker {
	seen := map[string]bool{}
	var olds []string
	add := func(s string) {
		if len(s) >= minSecret && !seen[s] {
			seen[s] = true
			olds = append(olds, s)
		}
	}
	for _, s := range secrets {
		add(s)
		add(url.QueryEscape(s))
		add(url.PathEscape(s))
		for _, enc := range []func(any) ([]byte, error){json.Marshal, jsonx.Marshal} {
			if j, err := enc(s); err == nil {
				add(string(j[1 : len(j)-1]))
			}
		}
	}
	sort.SliceStable(olds, func(a, b int) bool { return len(olds[a]) > len(olds[b]) })
	pairs := make([]string, 0, 2*len(olds))
	for _, o := range olds {
		pairs = append(pairs, o, Masked)
	}
	return masker{olds: olds, r: strings.NewReplacer(pairs...)}
}

func (m masker) text(s string) string {
	for _, o := range m.olds {
		if strings.Contains(s, o) {
			return m.r.Replace(s)
		}
	}
	return s
}

func (m masker) bytes(b []byte) []byte {
	for _, o := range m.olds {
		if bytes.Contains(b, []byte(o)) {
			return []byte(m.r.Replace(string(b)))
		}
	}
	return b
}

// maskHeaders masks the values of the headers the auth added, and secrets
// in the others.
func maskHeaders(hs []send.Header, m masker) []send.Header {
	if len(hs) == 0 {
		return hs
	}
	out := make([]send.Header, len(hs))
	for i, h := range hs {
		switch {
		case h.Auth:
			h.Value = maskAuth(h.Name, h.Value)
		default:
			h.Value = m.text(h.Value)
		}
		out[i] = h
	}
	return out
}

// maskAuth masks an auth header's value; an Authorization header keeps its
// scheme ("Bearer ••••").
func maskAuth(name, value string) string {
	if strings.EqualFold(name, "Authorization") || strings.EqualFold(name, "Proxy-Authorization") {
		if scheme, _, ok := strings.Cut(value, " "); ok && scheme != "" && strings.IndexFunc(scheme, notSchemeRune) < 0 {
			return scheme + " " + Masked
		}
	}
	return Masked
}

func notSchemeRune(r rune) bool {
	return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-')
}

// maskQuery masks the values of the named query parameters of a URL.
func maskQuery(u string, names []string) string {
	if len(names) == 0 {
		return u
	}
	base, query, hash := postman.SplitRaw(u)
	if query == "" {
		return u
	}
	pairs := strings.Split(query, "&")
	for i, p := range pairs {
		k, _, _ := strings.Cut(p, "=")
		if containsKey(names, k) {
			pairs[i] = k + "=" + Masked
		}
	}
	out := base + "?" + strings.Join(pairs, "&")
	if hash != "" {
		out += "#" + hash
	}
	return out
}

// queryKeys are the names of a URL's query parameters, decoded.
func queryKeys(u string) []string {
	_, query, _ := postman.SplitRaw(u)
	if query == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(query, "&") {
		k, _, _ := strings.Cut(p, "=")
		if d, err := url.QueryUnescape(k); err == nil {
			k = d
		}
		out = append(out, k)
	}
	return out
}

func containsKey(names []string, k string) bool {
	if d, err := url.QueryUnescape(k); err == nil {
		k = d
	}
	for _, n := range names {
		if n == k {
			return true
		}
	}
	return false
}

// addedQuery are the query parameters of what was sent that the request
// itself doesn't have: the ones the auth added (an API key in the query).
func addedQuery(own *postman.URL, sent string) []string {
	var mine []string
	if own != nil {
		for _, q := range own.Query {
			mine = append(mine, q.Key)
		}
		mine = append(mine, queryKeys(own.Raw)...)
	}
	var out []string
	for _, k := range queryKeys(sent) {
		if !containsKey(mine, k) && !containsKey(out, k) {
			out = append(out, k)
		}
	}
	return out
}

package auth

import (
	"context"
	"crypto/md5" //nolint:gosec // Digest auth's MD5 is the protocol's
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"net/url"
	"strings"

	"github.com/Azure/go-ntlmssp"

	"github.com/shinyvision/posthero/internal/postman"
	"github.com/shinyvision/posthero/internal/send"
)

// Sender sends prepared requests.
type Sender interface {
	Do(ctx context.Context, p *send.Prepared) (*send.Response, error)
}

// Used is what the auth did for a request, to show.
type Used struct {
	// Type is the auth type, From where it was set (request, folder …,
	// collection).
	Type, From string
	// Token is the OAuth 2.0 token sent, and Fetch how it was got.
	Token *Token
	Fetch Fetch
	// Config is the OAuth 2.0 settings.
	Config *Config
}

// Do signs p with auth a and sends it. res fills in variables in the
// auth's settings; prompt asks the user what a login needs (nil asks
// nothing).
func (m *Manager) Do(ctx context.Context, s Sender, p *send.Prepared, a *postman.Auth, from string, res func(string) string, prompt Prompter) (*send.Response, *Used, error) {
	if a == nil || a.Type == "" || a.Type == "noauth" || a.Type == "inherit" {
		resp, err := s.Do(ctx, p)
		return resp, nil, err
	}
	prm := Resolve(a, res)
	used := &Used{Type: a.Type, From: from}
	var sign func(*send.Prepared, Params) error
	switch a.Type {
	case "basic":
		sign = basic
	case "bearer":
		sign = bearer
	case "apikey":
		sign = apiKey
	case "jwt":
		sign = jwtBearer
	case "asap":
		sign = asap
	case "oauth1":
		sign = oauth1
	case "awsv4":
		sign = awsv4
	case "hawk":
		sign = hawk
	case "edgegrid":
		sign = edgegrid
	case "digest":
		resp, err := digestDo(ctx, s, p, prm)
		return resp, used, err
	case "ntlm":
		resp, err := ntlmDo(ctx, s, p, prm)
		return resp, used, err
	case "oauth2":
		return m.oauth2Do(ctx, s, p, prm, used, prompt)
	default:
		return nil, used, fmt.Errorf("posthero can't sign requests with %q auth", a.Type)
	}
	if err := sign(p, prm); err != nil {
		return nil, used, fmt.Errorf("%s: %w", Names[a.Type], err)
	}
	resp, err := s.Do(ctx, p)
	return resp, used, err
}

// oauth2Do sends with a token, and when a kept token is refused (401) gets a
// fresh one and tries once more.
func (m *Manager) oauth2Do(ctx context.Context, s Sender, p *send.Prepared, prm Params, used *Used, prompt Prompter) (*send.Response, *Used, error) {
	c := ConfigFrom(prm)
	used.Config = &c
	t, how, err := m.Token(ctx, c, prompt, "")
	if err != nil {
		return nil, used, err
	}
	used.Token, used.Fetch = t, how
	signed := *p
	signed.Header = append([]send.Header(nil), p.Header...)
	place(&signed, c, t)
	resp, err := s.Do(ctx, &signed)
	if err != nil || resp.Status != 401 || how != Cached || !c.Fetches() {
		*p = signed
		return resp, used, err
	}
	// The server didn't take a token posthero thought was good (it was
	// revoked, or the server restarted): get a fresh one, once.
	t2, how2, err := m.Token(ctx, c, prompt, t.AccessToken)
	if err != nil {
		*p = signed
		return resp, used, nil
	}
	used.Token, used.Fetch = t2, how2
	retry := *p
	retry.Header = append([]send.Header(nil), p.Header...)
	place(&retry, c, t2)
	*p = retry
	resp, err = s.Do(ctx, &retry)
	return resp, used, err
}

// place puts a token where the settings say.
func place(p *send.Prepared, c Config, t *Token) {
	tok := t.AccessToken
	if c.UseIDToken && t.IDToken != "" {
		tok = t.IDToken
	}
	if c.AddTo == "queryParams" {
		p.AddQuery("access_token", tok)
		return
	}
	prefix := c.HeaderPrefix
	if prefix == "" {
		prefix = "Bearer"
	}
	p.Set("Authorization", strings.TrimSpace(prefix+" "+tok), true)
}

// digestDo sends with Digest auth: when the settings don't already have
// the server's realm and nonce, a first request asks for them (401).
func digestDo(ctx context.Context, s Sender, p *send.Prepared, prm Params) (*send.Response, error) {
	ch := map[string]string{}
	for _, k := range []string{"realm", "nonce", "algorithm", "qop", "opaque"} {
		if v := prm.Str(k); v != "" {
			ch[k] = v
		}
	}
	if ch["nonce"] == "" && !prm.Bool("disableRetryRequest") {
		resp, err := s.Do(ctx, p)
		if err != nil || resp.Status != 401 {
			return resp, err
		}
		// A server may offer several schemes, each in a header of its own.
		var got map[string]string
		for _, h := range resp.Header {
			if strings.EqualFold(h.Name, "WWW-Authenticate") {
				if got = parseChallenge(h.Value, "digest"); got != nil {
					break
				}
			}
		}
		if got == nil {
			return resp, nil
		}
		for k, v := range got {
			if _, set := ch[k]; !set || k == "nonce" || k == "opaque" {
				ch[k] = v
			}
		}
	}
	header, err := digestHeader(p, prm, ch)
	if err != nil {
		return nil, err
	}
	p.Set("Authorization", header, true)
	return s.Do(ctx, p)
}

// parseChallenge reads a WWW-Authenticate challenge of a scheme.
func parseChallenge(h, scheme string) map[string]string {
	i := strings.Index(strings.ToLower(h), scheme+" ")
	if i < 0 {
		return nil
	}
	rest := h[i+len(scheme)+1:]
	out := map[string]string{}
	for rest != "" {
		rest = strings.TrimLeft(rest, " ,")
		k, after, ok := strings.Cut(rest, "=")
		if !ok {
			break
		}
		k = strings.ToLower(strings.TrimSpace(k))
		var v string
		if strings.HasPrefix(after, `"`) {
			end := 1
			for end < len(after) && (after[end] != '"' || after[end-1] == '\\') {
				end++
			}
			v = strings.ReplaceAll(after[1:min(end, len(after))], `\"`, `"`)
			rest = after[min(end+1, len(after)):]
		} else {
			v, rest, _ = strings.Cut(after, ",")
			v = strings.TrimSpace(v)
		}
		out[k] = v
	}
	return out
}

func digestHeader(p *send.Prepared, prm Params, ch map[string]string) (string, error) {
	alg := ch["algorithm"]
	if alg == "" {
		alg = "MD5"
	}
	var newHash func() hash.Hash
	switch strings.ToUpper(strings.TrimSuffix(strings.ToUpper(alg), "-SESS")) {
	case "MD5":
		newHash = md5.New
	case "SHA-256":
		newHash = sha256.New
	case "SHA-512-256":
		newHash = sha512.New512_256
	default:
		return "", fmt.Errorf("posthero doesn't know the digest algorithm %s", alg)
	}
	h := func(s string) string {
		x := newHash()
		x.Write([]byte(s))
		return hex.EncodeToString(x.Sum(nil))
	}
	u, err := url.Parse(p.URL)
	if err != nil {
		return "", err
	}
	uri := u.RequestURI()
	user, pass := prm.Str("username"), prm.Str("password")
	cnonce := prm.Or("clientNonce", nonce(16))
	nc := prm.Or("nonceCount", "00000001")
	qop := ""
	if q := ch["qop"]; q != "" {
		for _, o := range strings.Split(q, ",") {
			o = strings.TrimSpace(o)
			if o == "auth" || (o == "auth-int" && qop == "") {
				qop = o
			}
		}
	}
	ha1 := h(user + ":" + ch["realm"] + ":" + pass)
	if strings.HasSuffix(strings.ToUpper(alg), "-SESS") {
		ha1 = h(ha1 + ":" + ch["nonce"] + ":" + cnonce)
	}
	ha2 := h(p.Method + ":" + uri)
	if qop == "auth-int" {
		ha2 = h(p.Method + ":" + uri + ":" + h(string(body(p))))
	}
	var response string
	if qop != "" {
		response = h(ha1 + ":" + ch["nonce"] + ":" + nc + ":" + cnonce + ":" + qop + ":" + ha2)
	} else {
		response = h(ha1 + ":" + ch["nonce"] + ":" + ha2)
	}
	parts := []string{
		fmt.Sprintf(`username="%s"`, user), fmt.Sprintf(`realm="%s"`, ch["realm"]), fmt.Sprintf(`nonce="%s"`, ch["nonce"]),
		fmt.Sprintf(`uri="%s"`, uri), "algorithm=" + alg,
	}
	if qop != "" {
		parts = append(parts, "qop="+qop, "nc="+nc, fmt.Sprintf(`cnonce="%s"`, cnonce))
	}
	parts = append(parts, fmt.Sprintf(`response="%s"`, response))
	if ch["opaque"] != "" {
		parts = append(parts, fmt.Sprintf(`opaque="%s"`, ch["opaque"]))
	}
	return "Digest " + strings.Join(parts, ", "), nil
}

// ntlmDo runs NTLM's handshake: negotiate, the server's challenge, then
// the answer, on the same connection.
func ntlmDo(ctx context.Context, s Sender, p *send.Prepared, prm Params) (*send.Response, error) {
	user, domain := prm.Str("username"), prm.Str("domain")
	if d, u, ok := strings.Cut(user, `\`); ok && domain == "" {
		domain, user = d, u
	}
	neg, err := ntlmssp.NewNegotiateMessage(domain, prm.Str("workstation"))
	if err != nil {
		return nil, err
	}
	first := *p
	first.Header = append([]send.Header(nil), p.Header...)
	first.Set("Authorization", "NTLM "+base64.StdEncoding.EncodeToString(neg), true)
	first.Set("Connection", "keep-alive", false)
	resp, err := s.Do(ctx, &first)
	if err != nil {
		return nil, err
	}
	if resp.Status != 401 {
		return resp, nil
	}
	var challenge []byte
	for _, h := range resp.Header {
		if strings.EqualFold(h.Name, "WWW-Authenticate") && strings.HasPrefix(h.Value, "NTLM ") {
			challenge, err = base64.StdEncoding.DecodeString(strings.TrimSpace(h.Value[5:]))
			if err != nil {
				return nil, fmt.Errorf("the server's NTLM challenge isn't base64: %w", err)
			}
		}
	}
	if challenge == nil {
		return resp, nil
	}
	answer, err := ntlmssp.ProcessChallenge(challenge, user, prm.Str("password"), domain != "")
	if err != nil {
		return nil, fmt.Errorf("NTLM: %w", err)
	}
	p.Set("Authorization", "NTLM "+base64.StdEncoding.EncodeToString(answer), true)
	return s.Do(ctx, p)
}

// IsNoUser reports whether a login stopped because it had to ask the user
// and couldn't.
func IsNoUser(err error) bool { return errors.Is(err, errNoUser) }

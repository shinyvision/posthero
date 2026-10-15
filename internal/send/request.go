// Package send builds HTTP requests from Postman requests and sends them
// the way Postman does: variables filled in, Postman's default headers,
// every body mode, redirects, cookies, TLS settings and timings.
package send

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/shinyvision/posthero/internal/jsonx"
	"github.com/shinyvision/posthero/internal/postman"
	"github.com/shinyvision/posthero/internal/vars"
)

// Version goes in the User-Agent header.
var Version = "dev"

// Header is a header to send or that was received, in order.
type Header struct {
	Name, Value string
	// System marks a header posthero adds by itself (User-Agent, …).
	System bool
	// Auth marks a header the auth added.
	Auth bool
}

// Request is a request ready to fill in: what a script can still change
// before it is sent.
type Request struct {
	Method string
	URL    *postman.URL
	Header postman.Params
	Body   *postman.Body
	// Auth is the auth that applies, inherited ones worked out.
	Auth     *postman.Auth
	Settings Settings
}

// FromItem makes a Request from a request item, with the auth that applies
// (see EffectiveAuth).
func FromItem(it *postman.Item, auth *postman.Auth, defaults Settings) *Request {
	req := it.Request
	if req == nil {
		req = &postman.Request{Method: "GET", URL: &postman.URL{}}
	}
	r := &Request{
		Method:   strings.ToUpper(req.Method),
		Header:   append(postman.Params(nil), req.Header...),
		Auth:     auth.Clone(),
		Settings: defaults.With(it.ProtocolProfileBehavior),
	}
	if r.Method == "" {
		r.Method = "GET"
	}
	if req.URL != nil {
		u := *req.URL
		u.Query = append(postman.Params(nil), req.URL.Query...)
		u.Variable = append([]postman.Variable(nil), req.URL.Variable...)
		r.URL = &u
	} else {
		r.URL = &postman.URL{}
	}
	if req.Body != nil {
		b := *req.Body
		r.Body = &b
	}
	return r
}

// EffectiveAuth is the auth a request or folder uses: its own, or the
// nearest parent's, or the collection's ("inherit" walks up). Nil means none.
func EffectiveAuth(it *postman.Item, parents []*postman.Item, c *postman.Collection) (*postman.Auth, string) {
	if it.Folder && it.Auth != nil && it.Auth.Type != "" && it.Auth.Type != "inherit" {
		return it.Auth, "folder " + it.Name
	}
	if it.Request != nil && it.Request.Auth != nil && it.Request.Auth.Type != "" && it.Request.Auth.Type != "inherit" {
		return it.Request.Auth, "request"
	}
	for i := len(parents) - 1; i >= 0; i-- {
		if a := parents[i].Auth; a != nil && a.Type != "" && a.Type != "inherit" {
			return a, "folder " + parents[i].Name
		}
	}
	if c != nil && c.Auth != nil && c.Auth.Type != "" && c.Auth.Type != "inherit" {
		return c.Auth, "collection"
	}
	return nil, ""
}

// Prepared is a request with every variable filled in, ready to send.
type Prepared struct {
	Method string
	URL    string
	Header []Header
	// Body is what is sent; BodyFile streams a file instead.
	Body     []byte
	BodyFile string
	// BodyText describes the body for showing (files by name).
	BodyText string
	Settings Settings
	// Missing are variables that weren't set.
	Missing []string
}

// Get returns the value of a header, ignoring case.
func (p *Prepared) Get(name string) string {
	for _, h := range p.Header {
		if strings.EqualFold(h.Name, name) {
			return h.Value
		}
	}
	return ""
}

// Set replaces a header (or adds it) and marks where it came from.
func (p *Prepared) Set(name, value string, auth bool) {
	for i, h := range p.Header {
		if strings.EqualFold(h.Name, name) {
			p.Header[i] = Header{Name: name, Value: value, Auth: auth}
			return
		}
	}
	p.Header = append(p.Header, Header{Name: name, Value: value, Auth: auth})
}

// Del removes a header.
func (p *Prepared) Del(name string) {
	out := p.Header[:0]
	for _, h := range p.Header {
		if !strings.EqualFold(h.Name, name) {
			out = append(out, h)
		}
	}
	p.Header = out
}

// AddQuery adds a query parameter to the URL.
func (p *Prepared) AddQuery(key, value string) {
	base, query, hash := postman.SplitRaw(p.URL)
	pair := encodeQueryPart(key) + "=" + encodeQueryPart(value)
	if query != "" {
		query += "&" + pair
	} else {
		query = pair
	}
	p.URL = base + "?" + query
	if hash != "" {
		p.URL += "#" + hash
	}
}

// unresolved finds {{variables}} left in text.
var unresolved = regexp.MustCompile(`\{\{([^{}]+)\}\}`)

// hostVariables explains a server address that still has variables in
// it: nobody set them (often the environment that does isn't picked, or
// isn't in this workspace), and there is nowhere to send to.
func hostVariables(raw string) error {
	_, rest, _ := strings.Cut(raw, "://")
	host := rest
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		host = rest[:i]
	}
	var names []string
	for _, m := range unresolved.FindAllStringSubmatch(host, -1) {
		if n := "{{" + m[1] + "}}"; !slices.Contains(names, n) {
			names = append(names, n)
		}
	}
	switch len(names) {
	case 0:
		return nil
	case 1:
		return fmt.Errorf("%s isn't set, so there's no server to send to: pick an environment that sets it (E), or give it a value (:vars)", names[0])
	}
	return fmt.Errorf("%s and %s aren't set, so there's no server to send to: pick an environment that sets them (E), or give them values (:vars)", strings.Join(names[:len(names)-1], ", "), names[len(names)-1])
}

// Options are what preparing needs besides the request.
type Options struct {
	// Dir is where relative file paths start.
	Dir string
	// Token is the value of the Postman-Token header; empty makes one.
	Token string
}

// prunedMethods send no body unless disableBodyPruning is set.
var prunedMethods = map[string]bool{"GET": true, "COPY": true, "HEAD": true, "PURGE": true, "UNLOCK": true}

// Prepare fills in variables and builds the body and headers.
func (r *Request) Prepare(v *vars.Set, opts Options) (*Prepared, error) {
	missing := map[string]bool{}
	res := func(s string) string {
		out, miss := v.ResolveReport(s)
		for _, m := range miss {
			missing[m] = true
		}
		return out
	}
	p := &Prepared{Method: strings.ToUpper(res(r.Method)), Settings: r.Settings}

	// The URL: variables, then path variables, then encoding.
	raw := ResolveURL(r.URL, res)
	if raw == "" {
		return nil, fmt.Errorf("the request has no URL")
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw // as Postman does
	}
	if err := hostVariables(raw); err != nil {
		return nil, err
	}
	if !r.Settings.DisableURLEncoding {
		raw = EncodeURL(raw)
	}
	if _, err := url.Parse(raw); err != nil {
		return nil, fmt.Errorf("the URL %q isn't valid: %w", raw, err)
	}
	p.URL = raw

	// Headers as written, then the body's Content-Type, then Postman's own.
	for _, h := range r.Header {
		if h.Disabled || strings.TrimSpace(h.Key) == "" {
			continue
		}
		p.Header = append(p.Header, Header{Name: res(h.Key), Value: res(string(h.Value))})
	}
	body := r.Body
	if body != nil && (body.Disabled || prunedMethods[p.Method] && !r.Settings.DisableBodyPruning) {
		body = nil
	}
	if err := p.buildBody(body, res, opts.Dir); err != nil {
		return nil, err
	}
	p.systemHeaders(opts.Token)
	p.Missing = sortedKeys(missing)
	return p, nil
}

// ResolveURL is a URL with its variables and path variables filled in,
// before encoding.
func ResolveURL(u *postman.URL, res func(string) string) string {
	if u == nil {
		return ""
	}
	raw := strings.TrimSpace(res(u.SendRaw()))
	for _, pv := range u.Variable {
		raw = replacePathVar(raw, pv.Key, res(string(pv.Value)))
	}
	return raw
}

// replacePathVar fills in a :name path segment.
func replacePathVar(raw, name, value string) string {
	base, query, hash := postman.SplitRaw(raw)
	segs := strings.Split(base, "/")
	start := 0
	if i := strings.Index(base, "://"); i >= 0 {
		start = 3 // scheme:, "", host
	}
	for i := start; i < len(segs); i++ {
		if segs[i] == ":"+name {
			segs[i] = value
		}
	}
	out := strings.Join(segs, "/")
	if query != "" {
		out += "?" + query
	}
	if hash != "" {
		out += "#" + hash
	}
	return out
}

func (p *Prepared) has(name string) bool {
	for _, h := range p.Header {
		if strings.EqualFold(h.Name, name) {
			return true
		}
	}
	return false
}

// rawContentType is the Content-Type of a raw body by language.
var rawContentType = map[string]string{
	"json":       "application/json",
	"javascript": "application/javascript",
	"html":       "text/html",
	"xml":        "application/xml",
	"text":       "text/plain",
}

func (p *Prepared) buildBody(b *postman.Body, res func(string) string, dir string) error {
	if b == nil || b.Mode == "" {
		return nil
	}
	contentType := ""
	switch b.Mode {
	case "raw":
		p.Body = []byte(res(b.Raw))
		lang := b.Language()
		if lang == "" {
			lang = "text"
		}
		contentType = rawContentType[lang]
		if contentType == "" {
			contentType = "text/plain"
		}
	case "graphql":
		g := b.GraphQL
		if g == nil {
			g = &postman.GraphQL{}
		}
		payload := map[string]any{"query": res(string(g.Query))}
		if vtext := strings.TrimSpace(res(string(g.Variables))); vtext != "" {
			var vv any
			dec := json.NewDecoder(strings.NewReader(vtext))
			dec.UseNumber()
			if err := dec.Decode(&vv); err != nil {
				return fmt.Errorf("the GraphQL variables aren't valid JSON: %v", err)
			}
			payload["variables"] = vv
		} else {
			payload["variables"] = map[string]any{}
		}
		data, err := jsonx.Marshal(payload)
		if err != nil {
			return err
		}
		p.Body, contentType = data, "application/json"
	case "urlencoded":
		var parts []string
		for _, f := range b.URLEncoded {
			if f.Disabled || f.Key == "" {
				continue
			}
			parts = append(parts, formEncode(res(f.Key))+"="+formEncode(res(string(f.Value))))
		}
		p.Body, contentType = []byte(strings.Join(parts, "&")), "application/x-www-form-urlencoded"
	case "formdata":
		var buf bytes.Buffer
		w := multipart.NewWriter(&buf)
		var shown []string
		for _, f := range b.FormData {
			if f.Disabled || f.Key == "" {
				continue
			}
			key := res(f.Key)
			if f.Type == "file" {
				for _, src := range f.Files() {
					path := resolvePath(res(src), dir)
					data, err := os.ReadFile(path)
					if err != nil {
						return fmt.Errorf("form field %s: %v", key, err)
					}
					ct := f.ContentType
					if ct == "" {
						ct = mime.TypeByExtension(filepath.Ext(path))
					}
					if ct == "" {
						ct = "application/octet-stream"
					}
					h := textproto.MIMEHeader{}
					h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"; filename="%s"`, escapeQuotes(key), escapeQuotes(filepath.Base(path))))
					h.Set("Content-Type", ct)
					part, err := w.CreatePart(h)
					if err != nil {
						return err
					}
					_, _ = part.Write(data)
					shown = append(shown, fmt.Sprintf("%s: @%s (%s)", key, path, Size(int64(len(data)))))
				}
				continue
			}
			val := res(string(f.Value))
			h := textproto.MIMEHeader{}
			h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"`, escapeQuotes(key)))
			if f.ContentType != "" {
				h.Set("Content-Type", f.ContentType)
			}
			part, err := w.CreatePart(h)
			if err != nil {
				return err
			}
			_, _ = io.WriteString(part, val)
			shown = append(shown, key+": "+val)
		}
		_ = w.Close()
		p.Body = buf.Bytes()
		p.BodyText = strings.Join(shown, "\n")
		contentType = w.FormDataContentType()
		if ct := p.Get("Content-Type"); strings.HasPrefix(strings.ToLower(ct), "multipart/form-data") && !strings.Contains(ct, "boundary=") {
			p.Set("Content-Type", contentType, false) // the boundary is needed
		}
	case "file":
		src := ""
		if b.File != nil {
			src = res(string(b.File.Src))
		}
		if src == "" {
			return nil
		}
		path := resolvePath(src, dir)
		fi, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("the body file: %v", err)
		}
		p.BodyFile = path
		p.BodyText = fmt.Sprintf("@%s (%s)", path, Size(fi.Size()))
		contentType = mime.TypeByExtension(filepath.Ext(path))
		if contentType == "" {
			contentType = "application/octet-stream"
		}
	default:
		return fmt.Errorf("posthero can't send a %q body", b.Mode)
	}
	if contentType != "" && !p.has("Content-Type") {
		p.Header = append(p.Header, Header{Name: "Content-Type", Value: contentType, System: true})
	}
	return nil
}

func escapeQuotes(s string) string { return strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) }

// formEncode encodes a form field like a browser (and Postman): spaces as
// +, but {{ }} and the like left alone when already encoded.
func formEncode(s string) string { return url.QueryEscape(s) }

// resolvePath makes a relative path relative to dir.
func resolvePath(path, dir string) string {
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, path[2:])
		}
	}
	if filepath.IsAbs(path) || dir == "" {
		return path
	}
	return filepath.Join(dir, path)
}

// systemHeaders adds the headers Postman sends by itself, unless written or
// switched off with disabledSystemHeaders.
func (p *Prepared) systemHeaders(token string) {
	if token == "" {
		token = vars.UUID()
	}
	add := func(name, value string) {
		if p.has(name) || p.Settings.DisabledSystemHeaders[strings.ToLower(name)] {
			return
		}
		p.Header = append(p.Header, Header{Name: name, Value: value, System: true})
	}
	add("User-Agent", "posthero/"+Version)
	add("Accept", "*/*")
	if !p.Settings.NoCacheHeaderOff {
		add("Cache-Control", "no-cache")
	}
	add("Postman-Token", token)
	add("Accept-Encoding", "gzip, deflate, br")
	add("Connection", "keep-alive")
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Size writes a byte count for people: 512 B, 1.2 KB, 3.4 MB.
func Size(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	case n < 1024*1024*1024:
		return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
	}
	return fmt.Sprintf("%.1f GB", float64(n)/(1024*1024*1024))
}

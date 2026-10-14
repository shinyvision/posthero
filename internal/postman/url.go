package postman

import (
	"fmt"
	"maps"
	"strings"
)

// String is the URL as typed: Raw, or when there is none, the parts put
// together.
func (u *URL) String() string {
	if u == nil {
		return ""
	}
	if u.Raw != "" {
		return u.Raw
	}
	var b strings.Builder
	if u.Protocol != "" {
		b.WriteString(u.Protocol + "://")
	}
	b.WriteString(strings.Join(u.Host, "."))
	if u.Port != "" {
		b.WriteString(":" + u.Port)
	}
	if len(u.Path) > 0 {
		b.WriteString("/" + strings.Join(u.Path, "/"))
	}
	if q := u.Query.Enabled(); len(q) > 0 {
		b.WriteString("?" + QueryString(q))
	}
	if u.Hash != "" {
		b.WriteString("#" + u.Hash)
	}
	return b.String()
}

// Base is the URL without its query and fragment.
func (u *URL) Base() string {
	base, _, _ := SplitRaw(u.String())
	return base
}

// SendRaw is the URL to send before variables are filled in: its base, the
// enabled query parameters (the list, when there is one, is what counts)
// and the fragment.
func (u *URL) SendRaw() string {
	if u == nil {
		return ""
	}
	base, query, hash := SplitRaw(u.String())
	if len(u.Query) > 0 {
		query = QueryString(u.Query.Enabled())
	}
	out := base
	if query != "" {
		out += "?" + query
	}
	if hash != "" {
		out += "#" + hash
	}
	return out
}

// QueryString writes parameters as key=value pairs joined with &, leaving
// out the = for a parameter with no value written as key alone. Like
// Postman, it percent-encodes the & and # in a value, and the = in a key
// too, so a parameter stays one parameter; {{variables}} and what is
// encoded already stay as they are.
func QueryString(ps Params) string {
	parts := make([]string, 0, len(ps))
	for _, p := range ps {
		key := escapeQuery(p.Key, "&=#")
		if p.NoValue && p.Value == "" {
			parts = append(parts, key)
			continue
		}
		parts = append(parts, key+"="+escapeQuery(string(p.Value), "&#"))
	}
	return strings.Join(parts, "&")
}

// escapeQuery percent-encodes the given characters of s, outside
// {{variables}}.
func escapeQuery(s, chars string) string {
	if !strings.ContainsAny(s, chars) {
		return s
	}
	var b strings.Builder
	depth := 0
	for i := 0; i < len(s); i++ {
		switch {
		case strings.HasPrefix(s[i:], "{{"):
			depth++
			b.WriteString("{{")
			i++
		case strings.HasPrefix(s[i:], "}}") && depth > 0:
			depth--
			b.WriteString("}}")
			i++
		case depth == 0 && strings.IndexByte(chars, s[i]) >= 0:
			fmt.Fprintf(&b, "%%%02X", s[i])
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// SplitRaw splits a raw URL into what comes before the query, the query
// and the fragment. Braces of {{variables}} may hold ? and #.
func SplitRaw(raw string) (base, query, hash string) {
	q, h := -1, -1
	depth := 0
	for i := 0; i < len(raw); i++ {
		switch {
		case strings.HasPrefix(raw[i:], "{{"):
			depth++
			i++
		case strings.HasPrefix(raw[i:], "}}") && depth > 0:
			depth--
			i++
		case depth > 0:
		case raw[i] == '?' && q < 0 && h < 0:
			q = i
		case raw[i] == '#' && h < 0:
			h = i
		}
	}
	end := len(raw)
	if h >= 0 {
		hash = raw[h+1:]
		end = h
	}
	if q >= 0 {
		query = raw[q+1 : end]
		end = q
	}
	return raw[:end], query, hash
}

// ParseQuery splits a query string into parameters, keeping their order
// and values as written (not decoded).
func ParseQuery(query string) Params {
	if query == "" {
		return nil
	}
	var out Params
	for _, part := range strings.Split(query, "&") {
		if part == "" {
			continue
		}
		k, v, hasValue := strings.Cut(part, "=")
		out = append(out, Param{Key: k, Value: Str(v), NoValue: !hasValue})
	}
	return out
}

// ParseURL turns a raw URL into its parts the way Postman does: protocol,
// host segments, port, path segments, query and fragment.
func ParseURL(raw string) *URL {
	u := &URL{Raw: raw}
	base, query, hash := SplitRaw(raw)
	u.Hash = hash
	u.Query = ParseQuery(query)
	rest := base
	if i := strings.Index(rest, "://"); i >= 0 && !strings.Contains(rest[:i], "{{") && !strings.Contains(rest[:i], "/") {
		u.Protocol, rest = rest[:i], rest[i+3:]
	}
	host, path := rest, ""
	if i := indexOutsideBraces(rest, '/'); i >= 0 {
		host, path = rest[:i], rest[i+1:]
	}
	if at := strings.LastIndex(host, "@"); at >= 0 && !strings.Contains(host[at:], "}}") {
		host = host[at+1:] // user:password@ stays only in Raw
	}
	if i := strings.LastIndex(host, ":"); i >= 0 && !strings.Contains(host[i:], "}}") && !strings.Contains(host[i:], "]") {
		u.Port, host = host[i+1:], host[:i]
	}
	if host != "" {
		// The dots in {{variables}} don't split the host:
		// api{{suffix}}.example.com is api{{suffix}}, example and com.
		u.Host = splitOutsideBraces(host, '.')
	}
	if path != "" || strings.HasSuffix(base, "/") {
		u.Path = strings.Split(path, "/")
	}
	return u
}

// splitOutsideBraces splits s at each c that isn't in {{braces}}.
func splitOutsideBraces(s string, c byte) []string {
	var out []string
	for {
		i := indexOutsideBraces(s, c)
		if i < 0 {
			return append(out, s)
		}
		out = append(out, s[:i])
		s = s[i+1:]
	}
}

func indexOutsideBraces(s string, c byte) int {
	depth := 0
	for i := 0; i < len(s); i++ {
		switch {
		case strings.HasPrefix(s[i:], "{{"):
			depth++
			i++
		case strings.HasPrefix(s[i:], "}}") && depth > 0:
			depth--
			i++
		case depth == 0 && s[i] == c:
			return i
		}
	}
	return -1
}

// PathVariables lists the :name segments of a raw URL's path, in order.
func PathVariables(raw string) []string {
	base, _, _ := SplitRaw(raw)
	if i := strings.Index(base, "://"); i >= 0 {
		base = base[i+3:]
	}
	var out []string
	seen := map[string]bool{}
	for _, seg := range strings.Split(base, "/")[1:] {
		if len(seg) > 1 && seg[0] == ':' && !seen[seg[1:]] {
			seen[seg[1:]] = true
			out = append(out, seg[1:])
		}
	}
	return out
}

// Rebuild makes a URL from a raw string, keeping what the old one knew
// that the string doesn't say: the descriptions and other fields of query
// parameters, the values and descriptions of path variables, and fields
// posthero doesn't know. query lists every parameter, disabled ones
// included, in order; nil takes them from raw.
func Rebuild(old *URL, raw string, query Params, pathValues map[string]string) *URL {
	u := ParseURL(raw)
	if query != nil {
		u.Query = query
	}
	if old != nil {
		u.Extra = old.Extra
		// Parameters take what they don't say from the old ones with the
		// same key, in order.
		used := map[int]bool{}
		for i := range u.Query {
			for j, q := range old.Query {
				if !used[j] && q.Key == u.Query[i].Key {
					used[j] = true
					u.Query[i] = keepParam(q, u.Query[i])
					break
				}
			}
		}
	}
	oldVars := map[string]Variable{}
	if old != nil {
		for _, v := range old.Variable {
			oldVars[v.Key] = v
		}
	}
	for _, name := range PathVariables(raw) {
		v, ok := oldVars[name]
		if !ok {
			v = Variable{Key: name}
		}
		if val, ok := pathValues[name]; ok {
			v.Value = Str(val)
		}
		u.Variable = append(u.Variable, v)
	}
	return u
}

// keepParam is the old query parameter with what the new one says: its
// key, value and switch, and any other field it sets.
func keepParam(old, p Param) Param {
	out := old
	out.Key, out.Value, out.Disabled, out.NoValue = p.Key, p.Value, p.Disabled, p.NoValue
	if p.Description != nil {
		out.Description = p.Description
	}
	if p.Type != "" {
		out.Type = p.Type
	}
	if len(p.Src) > 0 {
		out.Src = p.Src
	}
	if p.ContentType != "" {
		out.ContentType = p.ContentType
	}
	if len(p.Extra) > 0 {
		out.Extra = Extra{}
		maps.Copy(out.Extra, old.Extra)
		maps.Copy(out.Extra, p.Extra)
	}
	return out
}

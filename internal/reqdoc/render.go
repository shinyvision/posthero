// Package reqdoc turns requests, folders and collections into text
// documents to edit in vim, and the edited text back into them.
//
// A request reads like an .http file: the method and URL first, query
// parameters and path variables on indented lines under it, then headers,
// a blank line and the body. Everything else is a section that starts with
// a "### name" line, and only what is set is shown:
//
//	POST {{base}}/users/:team
//	    ?notify=true
//	    # &dry=1
//	    :team = 7
//	Content-Type: application/json
//	# X-Debug: 1
//
//	{"name": "Ada"}
//
//	### tests
//	pm.test("created", () => pm.response.to.have.status(201))
//
// A # in front of a parameter, header or form field switches it off, and
// so does off after a body's section name (### json off).
package reqdoc

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/shinyvision/posthero/internal/jsonx"
	"github.com/shinyvision/posthero/internal/postman"
)

// Section names as written.
const (
	secVariables = "variables"
	secGraphQL   = "graphql"
	secForm      = "form"
	secMultipart = "multipart"
	secFile      = "file"
	secAuth      = "auth"
	secPre       = "pre-request"
	secTests     = "tests"
	secSettings  = "settings"
	secDesc      = "description"
)

// rawLanguages are the raw body languages, by section name.
var rawLanguages = map[string]string{"json": "json", "text": "text", "xml": "xml", "html": "html", "javascript": "javascript"}

// Ext is the extension posthero's documents have, for their filetype.
const Ext = ".posthero"

// Render writes a request item as a document.
func Render(it *postman.Item) string {
	var b strings.Builder
	req := it.Request
	if req == nil {
		req = &postman.Request{Method: "GET"}
	}
	writeURL(&b, req)
	for _, h := range req.Header {
		if blank(h) {
			continue
		}
		if h.Disabled {
			b.WriteString("# ")
		}
		b.WriteString(headerKey(h.Key) + ": " + quoteValue(string(h.Value)) + "\n")
	}
	writeBody(&b, req.Body)
	writeAuth(&b, req.Auth, false)
	writeScripts(&b, it.Event)
	writeSettings(&b, it.ProtocolProfileBehavior)
	desc := req.Description
	if desc == nil {
		desc = it.Description
	}
	if desc != nil && strings.TrimSpace(desc.Content) != "" {
		section(&b, secDesc, desc.Content)
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

// RenderFolder writes a folder's own settings: auth, scripts and its
// description.
func RenderFolder(it *postman.Item) string {
	var b strings.Builder
	b.WriteString(folderHint)
	writeAuth(&b, it.Auth, true)
	writeScripts(&b, it.Event)
	if it.Description != nil && strings.TrimSpace(it.Description.Content) != "" {
		section(&b, secDesc, it.Description.Content)
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

// RenderCollection writes a collection's own settings: auth, variables,
// scripts and description.
func RenderCollection(c *postman.Collection) string {
	var b strings.Builder
	b.WriteString(collectionHint)
	writeAuth(&b, c.Auth, true)
	if len(c.Variable) > 0 {
		var vb strings.Builder
		for _, v := range c.Variable {
			if v.Disabled {
				vb.WriteString("# ")
			}
			vb.WriteString(fieldKey(v.Key) + ": " + quoteValue(string(v.Value)) + "\n")
		}
		section(&b, secVariables, vb.String())
	}
	writeScripts(&b, c.Event)
	if c.Info.Description != nil && strings.TrimSpace(c.Info.Description.Content) != "" {
		section(&b, secDesc, c.Info.Description.Content)
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

const folderHint = `# A folder's settings apply to every request in it. Sections:
# ### auth TYPE, ### pre-request, ### tests, ### description
`

const collectionHint = `# The collection's settings apply to every request in it. Sections:
# ### auth TYPE, ### variables, ### pre-request, ### tests, ### description
`

// multiLine says the query goes on lines of its own under the URL: there
// are many parameters, or one that the URL line can't show as it is.
func multiLine(base string, query postman.Params) bool {
	if len(query) == 0 {
		return false
	}
	if len(query) >= 3 || len(base) > 90 {
		return true
	}
	for _, q := range query {
		if q.Disabled || strings.ContainsAny(q.Key+string(q.Value), " \t\n\r#&") {
			return true
		}
	}
	return false
}

func writeURL(b *strings.Builder, req *postman.Request) {
	method := strings.ToUpper(req.Method)
	if method == "" {
		method = "GET"
	}
	u := req.URL
	if u == nil {
		u = &postman.URL{}
	}
	base, rawQuery, hash := postman.SplitRaw(u.String())
	query := u.Query
	if len(query) == 0 {
		query = postman.ParseQuery(rawQuery)
	}
	b.WriteString(method + " " + base)
	if multiLine(base, query) {
		if hash != "" {
			b.WriteString("#" + hash)
		}
		b.WriteByte('\n')
		first := true
		for _, q := range query {
			if blank(q) {
				continue
			}
			sep := "&"
			if first {
				sep = "?"
			}
			b.WriteString("    ")
			if q.Disabled {
				b.WriteString("# ")
			} else {
				first = false
			}
			b.WriteString(sep + paramText(q) + "\n")
		}
	} else {
		if len(query) > 0 {
			b.WriteString("?" + postman.QueryString(query))
		}
		if hash != "" {
			b.WriteString("#" + hash)
		}
		b.WriteByte('\n')
	}
	// Path variables the URL doesn't use do nothing; they aren't shown.
	used := map[string]bool{}
	for _, name := range postman.PathVariables(u.String()) {
		used[name] = true
	}
	for _, v := range u.Variable {
		if used[v.Key] {
			b.WriteString("    :" + v.Key + " = " + urlValue(string(v.Value)) + "\n")
		}
	}
}

// blank reports whether a parameter is an empty row, which Postman keeps
// and posthero leaves out.
func blank(p postman.Param) bool {
	return p.Key == "" && p.Value == "" && len(p.Src) == 0
}

func paramText(q postman.Param) string {
	if q.NoValue && q.Value == "" {
		return q.Key
	}
	return q.Key + "=" + urlValue(string(q.Value))
}

// writeBody writes the body after the headers, or in its section. A body
// that is switched off (and not sent) is in its section, with off after
// the section's name.
func writeBody(b *strings.Builder, body *postman.Body) {
	if body == nil || body.Mode == "" {
		return
	}
	off := ""
	if body.Disabled {
		off = " off"
	}
	switch body.Mode {
	case "raw":
		if strings.TrimSpace(body.Raw) == "" {
			return
		}
		lang := body.Language()
		if lang == "" {
			lang = "text"
		}
		if detectRaw(body.Raw) == lang && off == "" {
			b.WriteString("\n" + escape(trimEnd(body.Raw)) + "\n")
			return
		}
		section(b, langSection(lang)+off, body.Raw)
	case "graphql":
		g := body.GraphQL
		if g == nil {
			g = &postman.GraphQL{}
		}
		q := string(g.Query)
		if looksGraphQL(q) && off == "" {
			b.WriteString("\n" + escape(trimEnd(q)) + "\n")
		} else {
			section(b, secGraphQL+off, q)
		}
		if strings.TrimSpace(string(g.Variables)) != "" {
			section(b, secVariables, string(g.Variables))
		}
	case "urlencoded":
		section(b, secForm+off, fields(body.URLEncoded, false))
	case "formdata":
		section(b, secMultipart+off, fields(body.FormData, true))
	case "file":
		src := ""
		if body.File != nil {
			src = string(body.File.Src)
		}
		section(b, secFile+off, "@"+src)
	}
}

// langSection is the section name for a raw body language.
func langSection(lang string) string {
	if _, ok := rawLanguages[lang]; ok {
		return lang
	}
	return "text"
}

// fields writes form fields as key: value lines. In a multipart body,
// files are key: @path, and a content type is ;type=… after the value.
func fields(ps postman.Params, multipart bool) string {
	var b strings.Builder
	for _, p := range ps {
		if blank(p) {
			continue
		}
		start := fieldKey(p.Key) + ": "
		if p.Disabled {
			start = "# " + start
		}
		if !multipart {
			b.WriteString(start + quoteValue(string(p.Value)) + "\n")
			continue
		}
		suffix := ""
		if p.ContentType != "" {
			suffix = ";type=" + p.ContentType
		}
		if p.Type == "file" {
			files := p.Files()
			if len(files) == 0 {
				files = []string{""}
			}
			for _, f := range files {
				b.WriteString(start + "@" + formText(f) + suffix + "\n")
			}
			continue
		}
		b.WriteString(start + formText(string(p.Value)) + suffix + "\n")
	}
	return b.String()
}

// formText writes a form-data value or file; one that would read as
// something else (a file, a content type) is in quotes.
func formText(v string) string {
	if strings.HasPrefix(v, "@") || strings.Contains(v, ";type=") {
		q, _ := jsonx.Marshal(v)
		return string(q)
	}
	return quoteValue(v)
}

func writeAuth(b *strings.Builder, a *postman.Auth, showInherit bool) {
	if a == nil || a.Type == "" || a.Type == "inherit" {
		return
	}
	if a.Type == "noauth" {
		section(b, secAuth+" none", "")
		return
	}
	var ab strings.Builder
	for _, p := range a.Params[a.Type] {
		ab.WriteString(fieldKey(p.Key) + ": " + quoteValue(authText(p.Value)) + "\n")
	}
	section(b, secAuth+" "+a.Type, ab.String())
}

// authText is a setting as the document shows it: text as itself, other
// JSON as JSON.
func authText(raw json.RawMessage) string {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return ""
	}
	if s[0] == '"' {
		var str string
		if json.Unmarshal(raw, &str) == nil {
			return str
		}
	}
	var v any
	if json.Unmarshal(raw, &v) == nil {
		compact, _ := jsonx.Marshal(v)
		return string(compact)
	}
	return s
}

func writeScripts(b *strings.Builder, events []postman.Event) {
	if s := postman.Scripts(events, "prerequest"); strings.TrimSpace(s) != "" {
		section(b, secPre, s)
	}
	if s := postman.Scripts(events, "test"); strings.TrimSpace(s) != "" {
		section(b, secTests, s)
	}
}

func writeSettings(b *strings.Builder, raw json.RawMessage) {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil || len(m) == 0 {
		return
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sb strings.Builder
	for _, k := range keys {
		var v any
		if json.Unmarshal(m[k], &v) != nil {
			continue
		}
		// Settings at their defaults say nothing; they are not shown.
		if isDefaultSetting(k, v) {
			continue
		}
		val, _ := jsonx.Marshal(v)
		sb.WriteString(fieldKey(k) + ": " + string(val) + "\n")
	}
	if sb.Len() > 0 {
		section(b, secSettings, sb.String())
	}
}

// isDefaultSetting reports whether a request setting has the value it has
// when it isn't set at all.
func isDefaultSetting(k string, v any) bool {
	switch x := v.(type) {
	case bool:
		switch k {
		case "followRedirects", "strictSSL":
			return x
		}
		return !x
	case float64:
		return k == "maxRedirects" && x == 10
	case []any:
		return len(x) == 0
	case map[string]any:
		return len(x) == 0
	}
	return false
}

// section writes "### name" and its text, after a blank line.
func section(b *strings.Builder, name, text string) {
	b.WriteString("\n### " + name + "\n")
	text = escape(trimEnd(text))
	if text != "" {
		b.WriteString(text + "\n")
	}
}

// trimEnd drops the blank lines a text starts with and the blank space it
// ends with; the document can't show them, and they never matter.
func trimEnd(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.TrimRight(s, "\n\r\t ")
	for {
		line, rest, ok := strings.Cut(s, "\n")
		if !ok || strings.TrimSpace(line) != "" {
			return s
		}
		s = rest
	}
}

// escape protects lines of content that would start a section: a line of
// ###, after any backslashes, gets one backslash more.
func escape(text string) string {
	if !strings.Contains(text, "###") {
		return text
	}
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		if sectionLike(l) {
			lines[i] = `\` + l
		}
	}
	return strings.Join(lines, "\n")
}

// sectionLike reports whether a line starts with ###, after any
// backslashes.
func sectionLike(l string) bool {
	return strings.HasPrefix(strings.TrimLeft(l, `\`), "###")
}

// quoteValue writes a value that wouldn't survive as plain text on one
// line (it has a line break, starts with a quote, or starts or ends with
// blank space) as a JSON string.
func quoteValue(v string) string {
	if v == "" {
		return ""
	}
	if strings.ContainsAny(v, "\n\r") || v[0] == '"' || strings.TrimSpace(v) != v {
		q, _ := jsonx.Marshal(v)
		return string(q)
	}
	return v
}

// urlValue writes a value under the URL (a query value, a path variable's
// value) as it is, unless a line can't keep it: it has a line break, or
// blank space at an end. Then it is a JSON string, and so is a value that
// would be read as one.
func urlValue(v string) string {
	if needsQuotes(v) {
		q, _ := jsonx.Marshal(v)
		return string(q)
	}
	return v
}

// needsQuotes reports whether urlValue writes a value as a JSON string.
func needsQuotes(v string) bool {
	if strings.ContainsAny(v, "\n\r") || strings.TrimSpace(v) != v {
		return true
	}
	s, rest, ok := cutQuoted(v)
	return ok && rest == "" && needsQuotes(s)
}

// headerKey writes a header's name: as it is, or in quotes when it has
// characters a header name doesn't, or starts with # (which switches a
// line off).
func headerKey(k string) string {
	if headerName.MatchString(k) && !strings.HasPrefix(k, "#") {
		return k
	}
	q, _ := jsonx.Marshal(k)
	return string(q)
}

// fieldKey writes the key of a form field, variable or setting: as it is,
// or in quotes when a line couldn't keep it (it is empty, has ": " or a
// line break in it, starts with #, // or a quote, or with blank space, or
// ends with blank space).
func fieldKey(k string) string {
	if k == "" || strings.Contains(k, ": ") || strings.ContainsAny(k, "\n\r") || strings.TrimSpace(k) != k ||
		strings.HasPrefix(k, "#") || strings.HasPrefix(k, "//") || strings.HasPrefix(k, `"`) {
		q, _ := jsonx.Marshal(k)
		return string(q)
	}
	return k
}

// detectRaw guesses the language of a raw body from its text: what a
// document's body without a section header is taken as.
func detectRaw(text string) string {
	t := strings.TrimSpace(text)
	switch {
	case t == "":
		return "text"
	case strings.HasPrefix(t, "["):
		return "json"
	case strings.HasPrefix(t, "{"):
		if looksGraphQL(t) {
			return "graphql"
		}
		return "json"
	case strings.HasPrefix(t, "<"):
		lower := strings.ToLower(t)
		if strings.HasPrefix(lower, "<!doctype html") || strings.HasPrefix(lower, "<html") {
			return "html"
		}
		return "xml"
	case looksGraphQL(t):
		return "graphql"
	}
	return "text"
}

// looksGraphQL reports whether text reads as a GraphQL document: it starts
// with an operation or fragment keyword, or with a selection set ({ field
// … }) rather than a JSON object.
func looksGraphQL(text string) bool {
	t := strings.TrimSpace(text)
	for strings.HasPrefix(t, "#") {
		// GraphQL comments come before the operation.
		_, rest, ok := strings.Cut(t, "\n")
		if !ok {
			return false
		}
		t = strings.TrimSpace(rest)
	}
	for _, kw := range []string{"query", "mutation", "subscription", "fragment"} {
		if rest, ok := strings.CutPrefix(t, kw); ok && (rest == "" || strings.ContainsAny(rest[:1], " \t\n({@")) {
			return true
		}
	}
	if !strings.HasPrefix(t, "{") {
		return false
	}
	inner := strings.TrimSpace(t[1:])
	if inner == "" || inner[0] == '"' || inner[0] == '}' || strings.HasPrefix(inner, "{{") {
		return false
	}
	c := inner[0]
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

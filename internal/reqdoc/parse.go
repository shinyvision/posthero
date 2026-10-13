package reqdoc

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// Error is a mistake in a document, at a line (1-based).
type Error struct {
	Line int
	Msg  string
}

func (e *Error) Error() string { return fmt.Sprintf("line %d: %s", e.Line, e.Msg) }

func errorf(line int, format string, args ...any) error {
	return &Error{Line: line, Msg: fmt.Sprintf(format, args...)}
}

// doc is a parsed request document.
type doc struct {
	method, url string
	line        int     // of the method and URL
	query       []field // nil when the URL line holds the query
	paths       []field
	headers     []field
	body        string // the body after the headers, without a section
	hasBody     bool
	bodyLine    int
	sections    []sec
}

// field is a key and value from a line, switched off with #.
type field struct {
	key, value  string
	raw         string // the value as written, quotes and all
	off         bool
	noValue     bool
	file        bool
	contentType string
	line        int
}

// sec is a "### name arg" section and its text.
type sec struct {
	name, arg string
	text      string
	line      int // of the ### line
	first     int // of the text's first line
}

var (
	sectionLine = regexp.MustCompile(`^###\s+(\S+)(?:\s+(.*?))?\s*$`)
	methodToken = regexp.MustCompile(`^[A-Za-z]+$`)
	// headerName is a header name written without quotes: the characters
	// a header name has, and {{variables}}.
	headerName = regexp.MustCompile("^(?:[!#$%&'*+.^_`|~0-9A-Za-z-]|\\{\\{[^{}:\n]*\\}\\})+$")
)

// sectionAliases maps what people may write to the section names.
var sectionAliases = map[string]string{
	"variables": secVariables, "vars": secVariables,
	"graphql": secGraphQL, "query": secGraphQL,
	"form": secForm, "urlencoded": secForm, "x-www-form-urlencoded": secForm,
	"multipart": secMultipart, "formdata": secMultipart, "form-data": secMultipart,
	"file": secFile, "binary": secFile,
	"auth": secAuth, "authorization": secAuth,
	"pre-request": secPre, "prerequest": secPre, "pre-request-script": secPre, "pre": secPre,
	"tests": secTests, "test": secTests, "post-response": secTests, "postresponse": secTests,
	"settings":    secSettings,
	"description": secDesc, "docs": secDesc,
	"json": "json", "text": "text", "xml": "xml", "html": "html", "javascript": "javascript", "js": "javascript",
}

// splitSections cuts text into what comes before the first section and the
// sections.
func splitSections(text string) (head []string, sections []sec, err error) {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	cur := -1
	var body []string
	flush := func() {
		if cur >= 0 {
			// Blank lines around the text are layout, not content.
			sections[cur].text = unescape(trimEnd(strings.Join(body, "\n")))
			sections[cur].first = sections[cur].line + 1 + leadingBlank(body)
		}
		body = nil
	}
	for i, l := range lines {
		if m := sectionLine.FindStringSubmatch(l); m != nil {
			name, ok := sectionAliases[strings.ToLower(m[1])]
			if !ok {
				return nil, nil, errorf(i+1, "### %s isn't a section posthero knows; sections are: %s", m[1], sectionList)
			}
			flush()
			sections = append(sections, sec{name: name, arg: strings.TrimSpace(m[2]), line: i + 1})
			cur = len(sections) - 1
			continue
		}
		if cur < 0 {
			head = append(head, l)
		} else {
			body = append(body, l)
		}
	}
	flush()
	return head, sections, nil
}

const sectionList = "variables, graphql, json, text, xml, html, javascript, form, multipart, file, auth, pre-request, tests, settings, description"

// leadingBlank counts the blank lines lines start with.
func leadingBlank(lines []string) int {
	n := 0
	for n < len(lines) && strings.TrimSpace(lines[n]) == "" {
		n++
	}
	return n
}

// unescape undoes escape: a line of backslashes and ### loses one
// backslash.
func unescape(text string) string {
	if !strings.Contains(text, `\###`) {
		return text
	}
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		if strings.HasPrefix(l, `\`) && sectionLike(l) {
			lines[i] = l[1:]
		}
	}
	return strings.Join(lines, "\n")
}

// parse reads a request document.
func parse(text string) (*doc, error) {
	head, sections, err := splitSections(text)
	if err != nil {
		return nil, err
	}
	d := &doc{sections: sections}
	i := 0
	// Blank lines and comments may come first.
	for i < len(head) && (strings.TrimSpace(head[i]) == "" || isComment(head[i])) {
		i++
	}
	if i == len(head) {
		return nil, errorf(1, "the first line is the method and URL, like GET https://example.com/users")
	}
	d.requestLine(head[i], i+1)
	i++
	// Indented lines under the URL: query parameters and path variables.
	for ; i < len(head); i++ {
		l := head[i]
		if l == "" || (l[0] != ' ' && l[0] != '\t') {
			break
		}
		if err := d.urlLine(strings.TrimSpace(l), i+1); err != nil {
			return nil, err
		}
	}
	// Headers, up to the first blank line.
	for ; i < len(head); i++ {
		l := head[i]
		if strings.TrimSpace(l) == "" {
			i++
			break
		}
		if l[0] == ' ' || l[0] == '\t' {
			return nil, errorf(i+1, "query parameters and path variables go right under the URL, before the headers")
		}
		h, ok, err := headerLine(l, i+1)
		if err != nil {
			return nil, err
		}
		if ok {
			d.headers = append(d.headers, h)
		}
	}
	// The rest, up to the first section, is the body.
	rest := head[i:]
	start := i
	for len(rest) > 0 && strings.TrimSpace(rest[0]) == "" {
		rest, start = rest[1:], start+1
	}
	body := trimEnd(strings.Join(rest, "\n"))
	if body != "" {
		d.body, d.hasBody, d.bodyLine = unescape(body), true, start+1
	}
	return d, nil
}

func isComment(l string) bool {
	t := strings.TrimSpace(l)
	return strings.HasPrefix(t, "#") || strings.HasPrefix(t, "//")
}

func (d *doc) requestLine(l string, n int) {
	l = strings.TrimSpace(l)
	method, rest, _ := strings.Cut(l, " ")
	if methodToken.MatchString(method) && strings.ToUpper(method) == method || methodToken.MatchString(method) && rest != "" && isHTTPMethod(method) {
		d.method, d.url = strings.ToUpper(method), strings.TrimSpace(rest)
	} else {
		d.method, d.url = "GET", l
	}
	d.line = n
}

func isHTTPMethod(m string) bool {
	switch strings.ToUpper(m) {
	case "GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS", "COPY", "LINK", "UNLINK", "PURGE", "LOCK", "UNLOCK", "PROPFIND", "VIEW", "TRACE", "CONNECT":
		return true
	}
	return false
}

// urlLine reads "?key=value", "&key=value", "# &key=value" (switched off)
// or ":name = value".
func (d *doc) urlLine(l string, n int) error {
	off := false
	if strings.HasPrefix(l, "#") {
		off = true
		l = strings.TrimSpace(l[1:])
	}
	switch {
	case strings.HasPrefix(l, "?"), strings.HasPrefix(l, "&"):
		k, v, hasValue := strings.Cut(l[1:], "=")
		if k == "" && !hasValue {
			return errorf(n, "a query parameter needs a name, like ?page=1")
		}
		d.query = append(d.query, field{key: k, value: readURLValue(v), off: off, noValue: !hasValue, line: n})
	case strings.HasPrefix(l, ":"):
		if off {
			return errorf(n, "a path variable can't be switched off; remove :%s from the URL instead", strings.TrimSpace(l[1:]))
		}
		k, v, ok := strings.Cut(l[1:], " = ")
		if !ok {
			k, v, _ = strings.Cut(l[1:], "=")
		}
		d.paths = append(d.paths, field{key: strings.TrimSpace(k), value: readURLValue(strings.TrimSpace(v)), line: n})
	default:
		return errorf(n, "under the URL go query parameters (?page=1, &size=20) and path variables (:id = 42)")
	}
	return nil
}

// headerLine reads "Name: value" or "# Name: value" (switched off). A
// name with other characters than a header name has is in quotes, like
// "X Custom": 1. Other comment lines are skipped.
func headerLine(l string, n int) (field, bool, error) {
	off := false
	t := l
	if strings.HasPrefix(t, "#") {
		off = true
		t = strings.TrimSpace(t[1:])
	} else if strings.HasPrefix(t, "//") {
		return field{}, false, nil
	}
	k, v, ok := cutHeader(t)
	if !ok {
		if off {
			return field{}, false, nil // a comment
		}
		return field{}, false, errorf(n, `a header is a name and a value, like Accept: application/json (a blank line comes before the body; an unusual name goes in quotes, like "X Custom": 1)`)
	}
	val, err := unquote(strings.TrimPrefix(v, " "), n)
	if err != nil {
		return field{}, false, err
	}
	return field{key: k, value: val, off: off, line: n}, true, nil
}

// cutHeader splits a header line into its name and value.
func cutHeader(t string) (key, value string, ok bool) {
	if s, rest, quoted := cutQuoted(t); quoted {
		value, ok = strings.CutPrefix(rest, ":")
		return s, value, ok
	}
	key, value, ok = strings.Cut(t, ":")
	key = strings.TrimSpace(key)
	return key, value, ok && headerName.MatchString(key)
}

// cutQuoted reads the JSON string text starts with, and returns it and
// what follows it.
func cutQuoted(text string) (s, rest string, ok bool) {
	if !strings.HasPrefix(text, `"`) {
		return "", "", false
	}
	dec := json.NewDecoder(strings.NewReader(text))
	if dec.Decode(&s) != nil {
		return "", "", false
	}
	return s, strings.TrimSpace(text[dec.InputOffset():]), true
}

// readURLValue undoes urlValue: a value in quotes is read as a JSON string
// only when it had to be written so.
func readURLValue(v string) string {
	if s, rest, ok := cutQuoted(v); ok && rest == "" && needsQuotes(s) {
		return s
	}
	return v
}

// unquote reads a value written as a JSON string ("…") or as itself.
func unquote(v string, n int) (string, error) {
	t := strings.TrimSpace(v)
	if len(t) >= 2 && t[0] == '"' && t[len(t)-1] == '"' {
		var s string
		if err := json.Unmarshal([]byte(t), &s); err != nil {
			return "", errorf(n, "a value in quotes is read as a JSON string: %v", err)
		}
		return s, nil
	}
	return strings.TrimRight(v, " \t"), nil
}

// fieldLines reads key: value lines of a form, variables or settings
// section. # switches a line off; blank lines are skipped.
func fieldLines(text string, first int, files bool) ([]field, error) {
	var out []field
	for i, l := range strings.Split(text, "\n") {
		n := first + i
		t := strings.TrimSpace(l)
		if t == "" || strings.HasPrefix(t, "//") {
			continue
		}
		off := false
		if strings.HasPrefix(t, "#") {
			off = true
			t = strings.TrimSpace(t[1:])
		}
		k, v, ok := cutField(t)
		if !ok {
			if off {
				continue // a comment
			}
			return nil, errorf(n, "write a key and a value, like name: Ada")
		}
		v = strings.TrimPrefix(v, " ")
		f := field{key: k, raw: strings.TrimSpace(v), off: off, line: n}
		var err error
		if files {
			f.value, f.file, f.contentType, err = formValue(v, n)
		} else {
			f.value, err = unquote(v, n)
		}
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, nil
}

// cutField splits "key: value" (or key:value, or key = value). The first
// ": " counts, so a key may have colons in it; a key that can't be written
// so is in quotes (see fieldKey).
func cutField(t string) (key, value string, ok bool) {
	if s, rest, quoted := cutQuoted(t); quoted && rest != "" && (rest[0] == ':' || rest[0] == '=') {
		return s, rest[1:], true
	}
	if i := strings.Index(t, ": "); i >= 0 {
		return strings.TrimSpace(t[:i]), t[i+1:], true
	}
	if k, ok := strings.CutSuffix(t, ":"); ok {
		return strings.TrimSpace(k), "", true
	}
	i := strings.IndexAny(t, ":=")
	if i < 0 {
		return "", "", false
	}
	return strings.TrimSpace(t[:i]), t[i+1:], true
}

// formValue reads a form-data value: text, or @ and a file, either in
// quotes when it has to be, and ;type= and a content type after it.
func formValue(v string, n int) (value string, file bool, contentType string, err error) {
	if rest, ok := strings.CutPrefix(v, "@"); ok {
		file, v = true, strings.TrimSpace(rest)
	}
	if s, rest, ok := cutQuoted(v); ok && (rest == "" || strings.HasPrefix(rest, ";type=")) {
		return s, file, strings.TrimSpace(strings.TrimPrefix(rest, ";type=")), nil
	}
	if i := strings.LastIndex(v, ";type="); i >= 0 {
		contentType = strings.TrimSpace(v[i+len(";type="):])
		v = v[:i]
	}
	if file {
		return strings.TrimSpace(v), true, contentType, nil
	}
	value, err = unquote(v, n)
	return value, false, contentType, err
}

// GraphQLQuery finds a document's GraphQL query: its lines as they are in
// the document (untrimmed, so places in them map to the document's), and
// the line (counting from 0) it starts on. ok is false when the body isn't
// GraphQL or the document can't be read as it is.
func GraphQLQuery(text string) (query string, line int, ok bool) {
	d, err := parse(text)
	if err != nil {
		return "", 0, false
	}
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	// A region runs to the next section.
	region := func(from int) string {
		to := from
		for to < len(lines) && !sectionLine.MatchString(lines[to]) {
			to++
		}
		return strings.Join(lines[from:to], "\n")
	}
	if d.hasBody && looksGraphQL(d.body) {
		return region(d.bodyLine - 1), d.bodyLine - 1, true
	}
	for _, s := range d.sections {
		if s.name == secGraphQL {
			return region(s.line), s.line, true
		}
	}
	return "", 0, false
}

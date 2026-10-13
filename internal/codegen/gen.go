package codegen

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/shinyvision/posthero/internal/send"
)

// Languages are the snippet languages, by name.
var Languages = []string{"curl", "httpie", "wget", "fetch", "python", "go", "http"}

// Snippet writes code that sends p. System headers (User-Agent,
// Postman-Token, …) are left out, as Postman's snippets do.
func Snippet(lang string, p *send.Prepared) (string, error) {
	headers := userHeaders(p)
	switch lang {
	case "curl", "":
		return curl(p, headers), nil
	case "httpie":
		return httpie(p, headers), nil
	case "wget":
		return wget(p, headers), nil
	case "fetch", "js", "javascript":
		return fetch(p, headers), nil
	case "python", "py":
		return python(p, headers), nil
	case "go":
		return goCode(p, headers), nil
	case "http", "raw":
		return raw(p, headers), nil
	}
	return "", fmt.Errorf("posthero doesn't write %s; it writes %s", lang, strings.Join(Languages, ", "))
}

func userHeaders(p *send.Prepared) []send.Header {
	var out []send.Header
	for _, h := range p.Header {
		if h.System && !strings.EqualFold(h.Name, "Content-Type") && !strings.EqualFold(h.Name, "Cookie") {
			continue
		}
		out = append(out, h)
	}
	return out
}

// shellQuote quotes a word for a POSIX shell.
func shellQuote(s string) string {
	if s != "" && strings.IndexFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_./:=@,+%", r))
	}) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func bodyText(p *send.Prepared) string {
	if p.BodyFile != "" {
		data, err := os.ReadFile(p.BodyFile)
		if err == nil {
			return string(data)
		}
	}
	return string(p.Body)
}

func isMultipart(p *send.Prepared) bool {
	return strings.HasPrefix(strings.ToLower(p.Get("Content-Type")), "multipart/form-data")
}

// formField is a field of a multipart body: text, or a file sent from
// its path.
type formField struct {
	name, value string
	// file is the path of a file field; filename is the name it is sent as.
	file, filename string
	contentType    string
}

// formFields reads the fields of a multipart body back from the body
// itself, so values are exactly what is sent; the paths of files come
// from BodyText. ok is false when the body can't be read as a form.
func formFields(p *send.Prepared) (fields []formField, ok bool) {
	_, params, err := mime.ParseMediaType(p.Get("Content-Type"))
	if err != nil || params["boundary"] == "" {
		return nil, false
	}
	shown := p.BodyText
	r := multipart.NewReader(bytes.NewReader(p.Body), params["boundary"])
	for {
		part, err := r.NextRawPart()
		if err == io.EOF {
			return fields, true
		}
		if err != nil {
			return nil, false
		}
		data, err := io.ReadAll(part)
		if err != nil || part.FormName() == "" {
			return nil, false
		}
		f := formField{name: part.FormName(), filename: part.FileName(), contentType: part.Header.Get("Content-Type")}
		if f.filename == "" {
			f.value = string(data)
			shown = strings.TrimPrefix(strings.TrimPrefix(shown, f.name+": "+f.value), "\n")
			fields = append(fields, f)
			continue
		}
		// BodyText shows a file as "name: @path (size)"; the path may
		// have " (" in it, so it ends where its own size does.
		f.file = f.filename
		size := " (" + send.Size(int64(len(data))) + ")"
		if rest, ok := strings.CutPrefix(shown, f.name+": @"); ok {
			for i := 0; i < len(rest); {
				j := strings.Index(rest[i:], size)
				if j < 0 {
					break
				}
				end := i + j
				after := rest[end+len(size):]
				if (after == "" || after[0] == '\n') && filepath.Base(rest[:end]) == f.filename {
					f.file, shown = rest[:end], strings.TrimPrefix(after, "\n")
					break
				}
				i = end + 1
			}
		}
		fields = append(fields, f)
	}
}

// curlType is the Content-Type curl gives a file by its name.
func curlType(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".gif":
		return "image/gif"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".svg":
		return "image/svg+xml"
	case ".txt":
		return "text/plain"
	case ".htm", ".html":
		return "text/html"
	case ".pdf":
		return "application/pdf"
	case ".xml":
		return "application/xml"
	}
	return "application/octet-stream"
}

// curlQuote quotes a -F file name or value for curl: "…" with \" and \\.
func curlQuote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// curlFormArg writes a form field as a curl option: --form-string for text,
// which curl takes as it is, and -F name=@"path" for a file.
func curlFormArg(f formField) string {
	switch {
	case f.filename != "":
		arg := f.name + "=@" + curlQuote(f.file)
		if f.contentType != curlType(f.filename) {
			arg += ";type=" + f.contentType
		}
		return "-F " + shellQuote(arg)
	case f.contentType != "" && !strings.HasPrefix(f.value, "<"):
		// A quoted value can have a type; curl reads a file for "<…".
		return "-F " + shellQuote(f.name+"="+curlQuote(f.value)+";type="+f.contentType)
	}
	return "--form-string " + shellQuote(f.name+"="+f.value)
}

func curl(p *send.Prepared, headers []send.Header) string {
	first := "curl "
	switch {
	case p.Method == "HEAD":
		first += "-I " // with -X HEAD, curl waits for a body that never comes
	case p.Method != "GET" || p.Body != nil:
		first += "-X " + shellQuote(p.Method) + " "
	}
	parts := []string{first + shellQuote(p.URL)}
	fields, multipart := formFields(p)
	multipart = multipart && isMultipart(p) && p.BodyFile == ""
	for _, h := range headers {
		switch {
		case multipart && strings.EqualFold(h.Name, "Content-Type"):
			continue // curl writes its own, with its boundary
		case h.Value == "":
			parts = append(parts, "-H "+shellQuote(h.Name+";")) // Name: alone would remove it
		default:
			parts = append(parts, "-H "+shellQuote(h.Name+": "+h.Value))
		}
	}
	switch {
	case p.BodyFile != "":
		parts = append(parts, "--data-binary "+shellQuote("@"+p.BodyFile))
	case multipart:
		for _, f := range fields {
			parts = append(parts, curlFormArg(f))
		}
	case len(p.Body) > 0:
		parts = append(parts, "--data-raw "+shellQuote(string(p.Body)))
	}
	if strings.ContainsAny(p.URL, "[]{}") {
		parts = append(parts, "-g") // [ ] { } aren't curl's globs
	}
	if !p.Settings.StrictSSL {
		parts = append(parts, "-k")
	}
	if p.Settings.FollowRedirects {
		parts = append(parts, "-L")
	}
	return strings.Join(parts, " \\\n  ")
}

// httpieItem escapes what HTTPie reads as a separator (: = @ ;) in a
// request item's name or value.
func httpieItem(s string) string {
	return strings.NewReplacer(":", `\:`, "=", `\=`, "@", `\@`, ";", `\;`).Replace(s)
}

func httpie(p *send.Prepared, headers []send.Header) string {
	parts := []string{"http"}
	fields, multipart := formFields(p)
	multipart = multipart && isMultipart(p) && p.BodyFile == ""
	if multipart {
		parts = append(parts, "--multipart")
	}
	if multipart || p.BodyFile == "" && len(p.Body) == 0 {
		parts = append(parts, "--ignore-stdin") // or, in a script, HTTPie reads it
	}
	if !p.Settings.StrictSSL {
		parts = append(parts, "--verify=no")
	}
	if p.Settings.FollowRedirects {
		parts = append(parts, "--follow")
	}
	parts = append(parts, shellQuote(p.Method), shellQuote(p.URL))
	for _, h := range headers {
		switch {
		case multipart && strings.EqualFold(h.Name, "Content-Type"):
			continue // HTTPie writes its own, with its boundary
		case h.Value == "":
			parts = append(parts, shellQuote(h.Name+";")) // Name: alone would remove it
		default:
			parts = append(parts, shellQuote(h.Name+":"+httpieItem(h.Value)))
		}
	}
	for _, f := range fields {
		if !multipart {
			break
		}
		if f.filename == "" {
			parts = append(parts, shellQuote(httpieItem(f.name)+"="+httpieItem(f.value)))
			continue
		}
		arg := httpieItem(f.name) + "@" + httpieItem(f.file)
		if f.contentType != curlType(f.filename) {
			arg += ";type=" + f.contentType
		}
		parts = append(parts, shellQuote(arg))
	}
	out := strings.Join(parts, " ")
	switch {
	case multipart:
	case p.BodyFile != "":
		out += " < " + shellQuote(p.BodyFile)
	case len(p.Body) > 0:
		out = "printf '%s' " + shellQuote(string(p.Body)) + " | " + out
	}
	return out
}

func wget(p *send.Prepared, headers []send.Header) string {
	parts := []string{"wget --no-verbose -O - --method=" + shellQuote(p.Method)}
	for _, h := range headers {
		parts = append(parts, "--header="+shellQuote(h.Name+": "+h.Value))
	}
	switch {
	case p.BodyFile != "":
		parts = append(parts, "--body-file="+shellQuote(p.BodyFile))
	case len(p.Body) > 0:
		parts = append(parts, "--body-data="+shellQuote(string(p.Body)))
	}
	if !p.Settings.StrictSSL {
		parts = append(parts, "--no-check-certificate")
	}
	if !p.Settings.FollowRedirects {
		parts = append(parts, "--max-redirect=0")
	}
	parts = append(parts, shellQuote(p.URL))
	return strings.Join(parts, " \\\n  ")
}

// jsString writes a JavaScript string literal: the characters that print
// as they are, the rest as \u escapes.
func jsString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case strconv.IsPrint(r) && r != utf8.RuneError:
			b.WriteRune(r)
		case r <= 0xffff:
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			fmt.Fprintf(&b, `\u{%x}`, r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func fetch(p *send.Prepared, headers []send.Header) string {
	var b strings.Builder
	if !p.Settings.StrictSSL {
		b.WriteString("// The certificate isn't checked: in Node, run with NODE_TLS_REJECT_UNAUTHORIZED=0.\n")
	}
	fmt.Fprintf(&b, "const response = await fetch(%s, {\n  method: %s,\n", jsString(p.URL), jsString(p.Method))
	cookie := false
	if len(headers) > 0 {
		b.WriteString("  headers: {\n")
		for _, h := range headers {
			fmt.Fprintf(&b, "    %s: %s,\n", jsString(h.Name), jsString(h.Value))
			cookie = cookie || strings.EqualFold(h.Name, "Cookie")
		}
		b.WriteString("  },\n")
	}
	if body := bodyText(p); body != "" {
		if utf8.ValidString(body) {
			fmt.Fprintf(&b, "  body: %s,\n", jsString(body))
		} else {
			// Bytes that aren't text would be sent as UTF-8 from a string.
			fmt.Fprintf(&b, "  body: Uint8Array.from(atob(%q), (c) => c.charCodeAt(0)),\n", base64.StdEncoding.EncodeToString([]byte(body)))
		}
	}
	if cookie {
		b.WriteString("  credentials: \"include\", // a browser sends its own cookies, not a Cookie header\n")
	}
	if !p.Settings.FollowRedirects {
		b.WriteString("  redirect: \"manual\",\n")
	}
	b.WriteString("});\nconsole.log(response.status, await response.text());")
	return b.String()
}

// pyBytes writes a Python bytes literal.
func pyBytes(s string) string {
	var b strings.Builder
	b.WriteString(`b"`)
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '"' || c == '\\':
			b.WriteByte('\\')
			b.WriteByte(c)
		case c == '\n':
			b.WriteString(`\n`)
		case c == '\r':
			b.WriteString(`\r`)
		case c == '\t':
			b.WriteString(`\t`)
		case c >= 0x20 && c < 0x7f:
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, `\x%02x`, c)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func python(p *send.Prepared, headers []send.Header) string {
	var b strings.Builder
	b.WriteString("import requests\n\n")
	if len(headers) > 0 {
		b.WriteString("headers = {\n")
		for _, h := range headers {
			fmt.Fprintf(&b, "    %s: %s,\n", strconv.Quote(h.Name), strconv.Quote(h.Value))
		}
		b.WriteString("}\n")
	}
	args := []string{strconv.Quote(p.Method), strconv.Quote(p.URL)}
	if len(headers) > 0 {
		args = append(args, "headers=headers")
	}
	if body := bodyText(p); body != "" {
		if utf8.ValidString(body) {
			fmt.Fprintf(&b, "data = %s\n", strconv.Quote(body))
			args = append(args, "data=data.encode()")
		} else {
			fmt.Fprintf(&b, "data = %s\n", pyBytes(body))
			args = append(args, "data=data")
		}
	}
	if !p.Settings.StrictSSL {
		args = append(args, "verify=False")
	}
	if !p.Settings.FollowRedirects {
		args = append(args, "allow_redirects=False")
	}
	fmt.Fprintf(&b, "\nresponse = requests.request(%s)\nprint(response.status_code, response.text)", strings.Join(args, ", "))
	return b.String()
}

// goString writes a Go string literal: a raw one when the text can be
// one, quoted when it has carriage returns, NULs or bytes that aren't
// UTF-8, which a raw string can't keep.
func goString(s string) string {
	for _, r := range s {
		if r != '\n' && r != '\t' && (!strconv.IsPrint(r) || r == utf8.RuneError || r == '\ufeff') {
			return strconv.Quote(s)
		}
	}
	return "`" + strings.ReplaceAll(s, "`", "`+\"`\"+`") + "`"
}

func goCode(p *send.Prepared, headers []send.Header) string {
	var b strings.Builder
	b.WriteString("package main\n\nimport (\n")
	if !p.Settings.StrictSSL {
		b.WriteString("\t\"crypto/tls\"\n")
	}
	b.WriteString("\t\"fmt\"\n\t\"io\"\n\t\"net/http\"\n")
	body := bodyText(p)
	if body != "" {
		b.WriteString("\t\"strings\"\n")
	}
	b.WriteString(")\n\nfunc main() {\n")
	bodyArg := "nil"
	if body != "" {
		fmt.Fprintf(&b, "\tbody := strings.NewReader(%s)\n", goString(body))
		bodyArg = "body"
	}
	fmt.Fprintf(&b, "\treq, err := http.NewRequest(%q, %q, %s)\n\tif err != nil {\n\t\tpanic(err)\n\t}\n", p.Method, p.URL, bodyArg)
	for _, h := range headers {
		if strings.EqualFold(h.Name, "Host") {
			fmt.Fprintf(&b, "\treq.Host = %q\n", h.Value) // Go sends Host from here
			continue
		}
		fmt.Fprintf(&b, "\treq.Header.Add(%q, %q)\n", h.Name, h.Value)
	}
	client := "http.DefaultClient"
	if !p.Settings.StrictSSL || !p.Settings.FollowRedirects {
		client = "client"
		b.WriteString("\tclient := &http.Client{}\n")
		if !p.Settings.StrictSSL {
			b.WriteString("\tclient.Transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}\n")
		}
		if !p.Settings.FollowRedirects {
			b.WriteString("\tclient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }\n")
		}
	}
	fmt.Fprintf(&b, "\tresp, err := %s.Do(req)\n\tif err != nil {\n\t\tpanic(err)\n\t}\n\tdefer resp.Body.Close()\n\tdata, _ := io.ReadAll(resp.Body)\n\tfmt.Println(resp.Status, string(data))\n}", client)
	return b.String()
}

func raw(p *send.Prepared, headers []send.Header) string {
	u, err := url.Parse(p.URL)
	target, host := p.URL, ""
	if err == nil {
		target = u.RequestURI()
		host = u.Host
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s HTTP/1.1\nHost: %s\n", p.Method, target, host)
	for _, h := range headers {
		b.WriteString(h.Name + ": " + h.Value + "\n")
	}
	if body := bodyText(p); body != "" {
		fmt.Fprintf(&b, "Content-Length: %d\n\n%s", len(body), body)
	}
	return strings.TrimRight(b.String(), "\n")
}

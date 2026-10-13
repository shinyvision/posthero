// Package codegen turns requests into code that sends them (curl, HTTPie,
// fetch, Python, Go, raw HTTP) and reads curl commands into requests.
package codegen

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/shinyvision/posthero/internal/jsonx"
	"github.com/shinyvision/posthero/internal/postman"
)

// ParseCurl reads a curl command (as copied from a browser's developer
// tools, for bash or for cmd.exe, or written by hand) into a request item.
func ParseCurl(cmd string) (*postman.Item, error) {
	var args []string
	if isCmdLine(cmd) {
		args = cmdWords(cmd)
	} else {
		var err error
		if args, err = shellWords(cmd); err != nil {
			return nil, err
		}
	}
	if len(args) == 0 || args[0] != "curl" {
		return nil, errors.New("that isn't a curl command")
	}
	opts, err := curlOptions(args[1:])
	if err != nil {
		return nil, err
	}
	req := &postman.Request{}
	settings := map[string]any{}
	var rawURL string
	var data []curlData
	var isJSON bool
	var forms postman.Params
	var get bool
	user := ""
	for _, o := range opts {
		v := o.value
		switch o.name {
		case "":
			// curl sends to every URL it is given; posthero keeps the first.
			if rawURL == "" {
				rawURL = v
			}
		case "-X", "--request":
			req.Method = strings.ToUpper(v)
		case "-H", "--header":
			k, val, ok := strings.Cut(v, ":")
			switch {
			case !ok && strings.HasSuffix(strings.TrimSpace(v), ";"):
				// Name; sends the header with no value.
				k = strings.TrimSuffix(strings.TrimSpace(v), ";")
			case strings.TrimSpace(val) == "":
				continue // Name: with no value keeps curl from sending it
			}
			req.Header = append(req.Header, postman.Param{Key: strings.TrimSpace(k), Value: postman.Str(strings.TrimSpace(val))})
		case "-d", "--data", "--data-raw", "--data-ascii", "--data-binary", "--json", "--data-urlencode":
			d := curlData{text: v}
			switch o.name {
			case "--json":
				isJSON = true
				fallthrough
			case "-d", "--data", "--data-ascii", "--data-binary":
				// @file reads the data from a file (--data-raw sends the @).
				if strings.HasPrefix(v, "@") {
					d = curlData{file: v[1:], strip: o.name != "--data-binary" && o.name != "--json"}
				}
			case "--data-urlencode":
				if k, val, ok := strings.Cut(v, "="); ok {
					d.text = url.QueryEscape(k) + "=" + url.QueryEscape(val)
				} else {
					d.text = url.QueryEscape(v)
				}
			}
			data = append(data, d)
		case "-F", "--form", "--form-string":
			p, err := curlForm(v, o.name == "--form-string")
			if err != nil {
				return nil, err
			}
			forms = append(forms, p)
		case "-u", "--user":
			user = v
		case "-b", "--cookie":
			// Without an =, it names a file of cookies.
			if strings.Contains(v, "=") {
				req.Header = append(req.Header, postman.Param{Key: "Cookie", Value: postman.Str(v)})
			}
		case "-A", "--user-agent":
			req.Header = append(req.Header, postman.Param{Key: "User-Agent", Value: postman.Str(v)})
		case "-e", "--referer":
			req.Header = append(req.Header, postman.Param{Key: "Referer", Value: postman.Str(v)})
		case "--url":
			if rawURL == "" {
				rawURL = v
			}
		case "--oauth2-bearer":
			req.Auth = &postman.Auth{Type: "bearer", Params: map[string][]postman.AuthParam{"bearer": {
				{Key: "token", Value: jsonString(v), Type: "string"},
			}}}
		case "-G", "--get":
			get = true
		case "-I", "--head":
			req.Method = "HEAD"
		case "-k", "--insecure":
			settings["strictSSL"] = false
		case "-L", "--location":
			settings["followRedirects"] = true
		}
	}
	if rawURL == "" {
		return nil, errors.New("the curl command has no URL")
	}
	// One data file is the body; more are read and joined, as curl does.
	fileBody := ""
	if len(data) == 1 && data[0].file != "" && !get {
		fileBody = data[0].file
	} else {
		for i, d := range data {
			if d.file == "" {
				continue
			}
			b, err := os.ReadFile(d.file)
			if err != nil {
				return nil, fmt.Errorf("the data file: %v", err)
			}
			if d.strip {
				b = bytes.ReplaceAll(bytes.ReplaceAll(b, []byte("\r"), nil), []byte("\n"), nil)
			}
			data[i].text = string(b)
		}
	}
	texts := make([]string, len(data))
	for i, d := range data {
		texts[i] = d.text
	}
	if get && len(data) > 0 {
		sep := "?"
		if strings.Contains(rawURL, "?") {
			sep = "&"
		}
		rawURL += sep + strings.Join(texts, "&")
		data = nil
	}
	if user != "" {
		u, p, _ := strings.Cut(user, ":")
		req.Auth = &postman.Auth{Type: "basic", Params: map[string][]postman.AuthParam{"basic": {
			{Key: "username", Value: jsonString(u), Type: "string"}, {Key: "password", Value: jsonString(p), Type: "string"},
		}}}
	}
	ct, hasCT, hasAccept := "", false, false
	for _, h := range req.Header {
		switch {
		case strings.EqualFold(h.Key, "Content-Type"):
			ct, hasCT = strings.ToLower(string(h.Value)), true
		case strings.EqualFold(h.Key, "Accept"):
			hasAccept = true
		}
	}
	if isJSON && len(data) > 0 {
		// --json sends both headers, unless they're given.
		if !hasCT {
			req.Header = append(req.Header, postman.Param{Key: "Content-Type", Value: "application/json"})
			ct, hasCT = "application/json", true
		}
		if !hasAccept {
			req.Header = append(req.Header, postman.Param{Key: "Accept", Value: "application/json"})
		}
	}
	// formType adds the Content-Type curl sends -d data with.
	formType := func() {
		if !hasCT {
			req.Header = append(req.Header, postman.Param{Key: "Content-Type", Value: "application/x-www-form-urlencoded"})
		}
	}
	switch {
	case len(forms) > 0:
		req.Body = &postman.Body{Mode: "formdata", FormData: forms}
		dropHeader(req, "Content-Type")
	case len(data) > 0 && fileBody != "":
		req.Body = &postman.Body{Mode: "file", File: &postman.BodyFile{Src: postman.Str(fileBody)}}
		formType()
	case len(data) > 0:
		body := strings.Join(texts, "&")
		if ps := formPairs(body); ps != nil && (ct == "" || strings.HasPrefix(ct, "application/x-www-form-urlencoded")) {
			req.Body = &postman.Body{Mode: "urlencoded", URLEncoded: ps}
			dropHeader(req, "Content-Type")
			break
		}
		// Anything else is sent as it is.
		formType()
		req.Body = &postman.Body{Mode: "raw", Raw: body}
		lang := "text"
		switch {
		case strings.Contains(ct, "json") || json.Valid([]byte(body)):
			lang = "json"
		case strings.Contains(ct, "xml"):
			lang = "xml"
		case strings.Contains(ct, "html"):
			lang = "html"
		case strings.Contains(ct, "javascript"):
			lang = "javascript"
		}
		req.Body.SetLanguage(lang)
		// A GraphQL request posted as JSON becomes a GraphQL body.
		if strings.Contains(ct, "json") {
			var g struct {
				Query     string          `json:"query"`
				Variables json.RawMessage `json:"variables"`
			}
			if json.Unmarshal([]byte(body), &g) == nil && g.Query != "" && looksLikeGraphQLBody(body) {
				vars := ""
				if len(g.Variables) > 0 && string(g.Variables) != "null" {
					var pretty any
					_ = json.Unmarshal(g.Variables, &pretty)
					out, _ := jsonx.MarshalIndent(pretty, "", "  ")
					vars = string(out)
				}
				req.Body = &postman.Body{Mode: "graphql", GraphQL: &postman.GraphQL{Query: postman.Str(g.Query), Variables: postman.Str(vars)}}
			}
		}
	}
	if req.Method == "" {
		req.Method = "GET"
		if req.Body != nil {
			req.Method = "POST"
		}
	}
	req.URL = postman.Rebuild(nil, rawURL, nil, nil)
	it := &postman.Item{Name: nameFor(req.Method, rawURL), Request: req}
	if len(settings) > 0 {
		it.ProtocolProfileBehavior, _ = jsonx.Marshal(settings)
	}
	return it, nil
}

// curlData is the value of a -d option: text, or a file to read it from.
type curlData struct {
	text, file string
	// strip drops the file's newlines, as -d does (--data-binary doesn't).
	strip bool
}

// formPairs reads a -d body as form fields when it is written as them,
// every &-part a name=value pair; otherwise it is nil and the body is
// sent as it is, as curl does.
func formPairs(body string) postman.Params {
	if body == "" || json.Valid([]byte(body)) {
		return nil
	}
	var ps postman.Params
	for _, part := range strings.Split(body, "&") {
		k, v, ok := strings.Cut(part, "=")
		if !ok || k == "" || strings.ContainsAny(k, " \t\r\n\"<>{}") {
			return nil
		}
		k, err := url.QueryUnescape(k)
		if err != nil {
			return nil
		}
		v, err = url.QueryUnescape(v)
		if err != nil {
			return nil
		}
		ps = append(ps, postman.Param{Key: k, Value: postman.Str(v)})
	}
	return ps
}

// curlForm reads a -F field the way curl does: name=value, name=@file
// (sends the file) or name=<file (the file's text is the value), each
// with ;type= and the like. --form-string takes the value as it is.
func curlForm(arg string, literal bool) (postman.Param, error) {
	name, v, ok := strings.Cut(arg, "=")
	if !ok {
		return postman.Param{}, fmt.Errorf("the form field %q has no =", arg)
	}
	p := postman.Param{Key: name, Type: "text"}
	if literal {
		p.Value = postman.Str(v)
		return p, nil
	}
	if rest, ok := strings.CutPrefix(v, "@"); ok {
		// A file, or files: @a.png,b.png.
		var files []string
		for {
			file, typ, sep, next := formPart(rest, ',')
			files = append(files, file)
			if typ != "" {
				p.ContentType = typ
			}
			if sep != ',' {
				break
			}
			rest = next
		}
		p.Type = "file"
		if len(files) == 1 {
			p.Src, _ = jsonx.Marshal(files[0])
		} else {
			p.Src, _ = jsonx.Marshal(files)
		}
		return p, nil
	}
	value, typ, _, _ := formPart(v, 0)
	p.ContentType = typ
	if path, ok := strings.CutPrefix(value, "<"); ok {
		if text, err := os.ReadFile(path); err == nil {
			p.Value = postman.Str(text)
			return p, nil
		}
		// The file isn't here: the field sends it from where it is.
		p.Type = "file"
		p.Src, _ = jsonx.Marshal(path)
		return p, nil
	}
	p.Value = postman.Str(value)
	return p, nil
}

// formPart reads a -F value as curl does: a word, "quoted" or up to a ;
// (or end), then ;type=…, ;filename=… and the like. It returns the word,
// the type, and the character it stopped at with what follows it.
func formPart(s string, end byte) (word, typ string, sep byte, rest string) {
	blanks := func(i int) int {
		for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
			i++
		}
		return i
	}
	word, i := formWord(s, blanks(0), end)
	typStart, typEnd := -1, -1
	for i < len(s) && s[i] == ';' {
		i = blanks(i + 1)
		param := s[i:]
		switch {
		case typStart < 0 && strings.HasPrefix(param, "type="):
			typStart = blanks(i + len("type="))
			for i = typStart; i < len(s) && s[i] != ';' && s[i] != end; i++ {
			}
			typEnd = i
		case strings.HasPrefix(param, "filename="), strings.HasPrefix(param, "headers="), strings.HasPrefix(param, "encoder="):
			eq := strings.IndexByte(param, '=')
			_, i = formWord(s, i+eq+1, end)
		case typStart >= 0:
			// More of the type: ;charset=utf-8.
			for ; i < len(s) && s[i] != ';' && s[i] != end; i++ {
			}
			typEnd = i
		default:
			_, i = formWord(s, i, end) // curl skips what it doesn't know
		}
	}
	if typStart >= 0 {
		typ = strings.TrimSpace(s[typStart:typEnd])
	}
	if i < len(s) {
		sep, rest = s[i], s[i+1:]
	}
	return word, typ, sep, rest
}

// formWord reads a word of a -F value from s[i:]: "quoted" (with \" and
// \\) or up to a ; or end. It returns the word and where it stopped.
func formWord(s string, i int, end byte) (string, int) {
	if i < len(s) && s[i] == '"' {
		var b strings.Builder
		for j := i + 1; j < len(s); j++ {
			switch {
			case s[j] == '\\' && j+1 < len(s) && (s[j+1] == '\\' || s[j+1] == '"'):
				j++
				b.WriteByte(s[j])
			case s[j] == '"':
				// curl ignores anything between the quote and the next ;.
				for j++; j < len(s) && s[j] != ';' && s[j] != end; j++ {
				}
				return b.String(), j
			default:
				b.WriteByte(s[j])
			}
		}
		// With no closing quote, the quote is part of the word.
	}
	j := i
	for j < len(s) && s[j] != ';' && s[j] != end {
		j++
	}
	return strings.TrimRight(s[i:j], " \t"), j
}

// curlOpt is an option of a curl command with its value; a word that
// isn't an option (a URL) has no name.
type curlOpt struct{ name, value string }

// curlOptions reads curl's options: --name value (or --name=value),
// -X value, -Xvalue and short options run together, like -sSL.
func curlOptions(args []string) ([]curlOpt, error) {
	var out []curlOpt
	value := func(i *int, name string) (string, error) {
		if *i+1 >= len(args) {
			return "", fmt.Errorf("%s needs a value", name)
		}
		*i++
		return args[*i], nil
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			for _, w := range args[i+1:] {
				out = append(out, curlOpt{value: w})
			}
			return out, nil
		case strings.HasPrefix(a, "--"):
			name, v, inline := strings.Cut(a, "=")
			if !curlArgOptions[name] {
				out = append(out, curlOpt{name: name})
				continue
			}
			if !inline {
				var err error
				if v, err = value(&i, name); err != nil {
					return nil, err
				}
			}
			out = append(out, curlOpt{name, v})
		case len(a) > 1 && a[0] == '-':
			for j := 1; j < len(a); j++ {
				name := "-" + a[j:j+1]
				if strings.IndexByte(curlShortArgOptions, a[j]) < 0 {
					out = append(out, curlOpt{name: name})
					continue
				}
				v := a[j+1:]
				if v == "" {
					var err error
					if v, err = value(&i, name); err != nil {
						return nil, err
					}
				}
				out = append(out, curlOpt{name, v})
				break
			}
		default:
			out = append(out, curlOpt{value: a})
		}
	}
	return out, nil
}

// curlShortArgOptions are the one-letter options that take a value.
const curlShortArgOptions = "AbcCdDeEFHKmoPQrtTuUwxXyYz"

// curlArgOptions are curl's long options that take a value (curl --help
// all), so that a value isn't taken for the URL.
var curlArgOptions = map[string]bool{}

func init() {
	for _, name := range strings.Fields(`abstract-unix-socket alt-svc aws-sigv4 cacert capath cert cert-type
		ciphers config connect-timeout connect-to continue-at cookie cookie-jar create-file-mode crlfile curves
		data data-ascii data-binary data-raw data-urlencode delegation dns-interface dns-ipv4-addr dns-ipv6-addr
		dns-servers doh-url dump-header ech egd-file engine etag-compare etag-save expect100-timeout form
		form-string ftp-account ftp-alternative-to-user ftp-method ftp-port ftp-ssl-ccc-mode
		happy-eyeballs-timeout-ms haproxy-clientip header hostpubmd5 hostpubsha256 hsts interface ip-tos
		ipfs-gateway json keepalive-cnt keepalive-time key key-type knownhosts krb krb4 libcurl limit-rate
		local-port login-options mail-auth mail-from mail-rcpt max-filesize max-redirs max-time netrc-file
		noproxy oauth2-bearer output output-dir parallel-max parallel-max-host pass pinnedpubkey preproxy proto
		proto-default proto-redir proxy proxy-cacert proxy-capath proxy-cert proxy-cert-type proxy-ciphers
		proxy-crlfile proxy-header proxy-key proxy-key-type proxy-pass proxy-pinnedpubkey proxy-service-name
		proxy-tls13-ciphers proxy-tlsauthtype proxy-tlspassword proxy-tlsuser proxy-user proxy1.0 pubkey quote
		random-file range rate referer request request-target resolve retry retry-delay retry-max-time
		sasl-authzid service-name sigalgs socks4 socks4a socks5 socks5-gssapi-service socks5-hostname
		speed-limit speed-time ssl-sessions stderr telnet-option tftp-blksize time-cond tls-max tls13-ciphers
		tlsauthtype tlspassword tlsuser trace trace-ascii trace-config unix-socket upload-file upload-flags url
		url-query user user-agent variable vlan-priority write-out`) {
		curlArgOptions["--"+name] = true
	}
}

// looksLikeGraphQLBody says a JSON body has just the keys of a GraphQL
// request.
func looksLikeGraphQLBody(body string) bool {
	var m map[string]json.RawMessage
	if json.Unmarshal([]byte(body), &m) != nil {
		return false
	}
	for k := range m {
		if k != "query" && k != "variables" && k != "operationName" && k != "extensions" {
			return false
		}
	}
	return true
}

func dropHeader(req *postman.Request, name string) {
	out := req.Header[:0]
	for _, h := range req.Header {
		if !strings.EqualFold(h.Key, name) {
			out = append(out, h)
		}
	}
	req.Header = out
}

func jsonString(s string) json.RawMessage {
	b, _ := jsonx.Marshal(s)
	return b
}

// nameFor names a pasted request after its method and path.
func nameFor(method, raw string) string {
	base, _, _ := postman.SplitRaw(raw)
	if i := strings.Index(base, "://"); i >= 0 {
		base = base[i+3:]
	}
	if i := strings.IndexByte(base, '/'); i >= 0 {
		base = base[i:]
	}
	if base == "" {
		base = "/"
	}
	return method + " " + base
}

// shellWords splits a command line like a POSIX shell: quotes, escapes and
// line continuations.
func shellWords(s string) ([]string, error) {
	s = strings.ReplaceAll(s, "\\\r\n", " ")
	s = strings.ReplaceAll(s, "\\\n", " ")
	var out []string
	var cur strings.Builder
	inWord := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\'':
			inWord = true
			j := strings.IndexByte(s[i+1:], '\'')
			if j < 0 {
				return nil, errors.New("a quote (') isn't closed")
			}
			cur.WriteString(s[i+1 : i+1+j])
			i += j + 1
		case c == '$' && i+1 < len(s) && s[i+1] == '\'':
			// $'…' with backslash escapes, as browsers copy it.
			inWord = true
			word, n, err := ansiC(s[i+2:])
			if err != nil {
				return nil, err
			}
			cur.WriteString(word)
			i += 2 + n
		case c == '"':
			inWord = true
			i++
			for ; i < len(s) && s[i] != '"'; i++ {
				if s[i] == '\\' && i+1 < len(s) && strings.IndexByte("\"\\$`", s[i+1]) >= 0 {
					i++
				}
				cur.WriteByte(s[i])
			}
			if i >= len(s) {
				return nil, errors.New(`a quote (") isn't closed`)
			}
		case c == '\\' && i+1 < len(s):
			inWord = true
			i++
			cur.WriteByte(s[i])
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			if inWord {
				out = append(out, cur.String())
				cur.Reset()
				inWord = false
			}
		default:
			inWord = true
			cur.WriteByte(c)
		}
	}
	if inWord {
		out = append(out, cur.String())
	}
	return out, nil
}

// ansiC reads the inside of a $'…' word up to its closing quote, with
// bash's escapes: \n, \t, \xHH, \uHHHH, \UHHHHHHHH, octal \NNN and the
// rest. It returns the word and where its closing quote is.
func ansiC(s string) (string, int, error) {
	var b []byte
	var high []int // where a \x or octal escape wrote a byte over 0x7f
	digits := func(i, most int, base uint32) (uint32, int) {
		var v uint32
		n := 0
		for ; n < most && i+n < len(s); n++ {
			d := uint32(strings.IndexByte("0123456789abcdef", s[i+n]|0x20))
			if s[i+n] < '0' || d >= base {
				break
			}
			v = v*base + d
		}
		return v, n
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\'' {
			// Firefox writes é as \xe9, which bash makes a Latin-1 byte:
			// when the bytes aren't UTF-8, they are those characters.
			if !utf8.Valid(b) && len(high) > 0 {
				var u []byte
				last := 0
				for _, at := range high {
					u = utf8.AppendRune(append(u, b[last:at]...), rune(b[at]))
					last = at + 1
				}
				b = append(u, b[last:]...)
			}
			return string(b), i, nil
		}
		if c != '\\' || i+1 >= len(s) {
			b = append(b, c)
			continue
		}
		i++
		switch e := s[i]; e {
		case 'a':
			b = append(b, '\a')
		case 'b':
			b = append(b, '\b')
		case 'e', 'E':
			b = append(b, 0x1b)
		case 'f':
			b = append(b, '\f')
		case 'n':
			b = append(b, '\n')
		case 'r':
			b = append(b, '\r')
		case 't':
			b = append(b, '\t')
		case 'v':
			b = append(b, '\v')
		case '\\', '\'', '"', '?':
			b = append(b, e)
		case 'c':
			// \cX is control-X.
			if i+1 < len(s) && s[i+1] != '\'' {
				i++
				if s[i] == '?' {
					b = append(b, 0x7f)
				} else {
					b = append(b, s[i]&0x1f)
				}
			} else {
				b = append(b, '\\', e)
			}
		case 'x', 'u', 'U':
			most := map[byte]int{'x': 2, 'u': 4, 'U': 8}[e]
			v, n := digits(i+1, most, 16)
			switch {
			case n == 0:
				b = append(b, '\\', e)
			case e == 'x':
				if v > 0x7f {
					high = append(high, len(b))
				}
				b = append(b, byte(v))
			default:
				b = utf8.AppendRune(b, rune(v))
			}
			i += n
		case '0', '1', '2', '3', '4', '5', '6', '7':
			v, n := digits(i, 3, 8)
			if v&0xff > 0x7f {
				high = append(high, len(b))
			}
			b = append(b, byte(v))
			i += n - 1
		default:
			// bash keeps the backslash of an escape it doesn't know.
			b = append(b, '\\', e)
		}
	}
	return "", 0, errors.New("a quote ($') isn't closed")
}

// isCmdLine says a command was copied for Windows cmd.exe: it has words
// in ^"…^" or lines that end in ^.
func isCmdLine(s string) bool {
	return cmdMarks.MatchString(s)
}

var cmdMarks = regexp.MustCompile(`(^|\s)\^"|\^\r?\n`)

// cmdWords splits a command line copied for cmd.exe, as Windows does: cmd
// first takes ^ as an escape outside quotes (and ^ at the end of a line
// joins the next one, its first character escaped), then the program
// splits its command line by its quoting rules: "…", \" for a quote, ""
// for a quote inside quotes.
func cmdWords(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	// cmd.exe's pass. An escaped newline is kept as part of a word;
	// newline marks those that end a line.
	var line []byte
	var newline []bool
	add := func(c byte, ends bool) {
		line = append(line, c)
		newline = append(newline, ends)
	}
	quoted := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"':
			quoted = !quoted
			add(c, false)
		case c == '^' && !quoted:
			if i+1 < len(s) && s[i+1] == '\n' {
				i++ // a continuation: the next line's first character is escaped
			}
			if i+1 < len(s) {
				i++
				add(s[i], false)
			}
		default:
			add(c, c == '\n')
		}
	}
	// The program's pass (the Microsoft C runtime's rules).
	var out []string
	var cur strings.Builder
	inWord, inQuote := false, false
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case c == '\\':
			// Backslashes are themselves, unless a quote follows them.
			j := i
			for j < len(line) && line[j] == '\\' {
				j++
			}
			n := j - i
			inWord = true
			if j < len(line) && line[j] == '"' {
				cur.WriteString(strings.Repeat(`\`, n/2))
				if n%2 == 1 {
					cur.WriteByte('"')
					j++
				}
			} else {
				cur.WriteString(strings.Repeat(`\`, n))
			}
			i = j - 1
		case c == '"':
			inWord = true
			if inQuote && i+1 < len(line) && line[i+1] == '"' {
				cur.WriteByte('"')
				i++
			} else {
				inQuote = !inQuote
			}
		case !inQuote && (c == ' ' || c == '\t' || newline[i]):
			if inWord {
				out = append(out, cur.String())
				cur.Reset()
				inWord = false
			}
		default:
			inWord = true
			cur.WriteByte(c)
		}
	}
	if inWord {
		out = append(out, cur.String())
	}
	return out
}

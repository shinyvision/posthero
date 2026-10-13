package respview

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"

	"github.com/shinyvision/posthero/internal/history"
	"github.com/shinyvision/posthero/internal/jsonx"
	"github.com/shinyvision/posthero/internal/send"
	"github.com/shinyvision/posthero/internal/ui/theme"
)

// line is a line of the page.
type line struct {
	text  string // styled
	plain string // for searching and copying
	// end is the last line of the fold this line starts (0: none).
	end int
	// summary replaces the line while it is folded.
	summary string
	// path is the JSON path of the value on the line; value its text (a
	// string without quotes).
	path, value string
	// section marks the first line of a part of the page ([[ and ]]).
	section bool
	// body says the line is part of the body.
	body bool
}

type builder struct {
	lines []line
}

func (b *builder) add(l line) int {
	if l.plain == "" {
		l.plain = ansi.Strip(l.text)
	}
	b.lines = append(b.lines, l)
	return len(b.lines) - 1
}

// fold starts a fold at a line; closeFold ends it at the last line added.
func (b *builder) closeFold(start int, summary string) {
	b.lines[start].end = len(b.lines) - 1
	b.lines[start].summary = summary
}

// build lays out a record. folded returns the folds to start closed.
func build(r *history.Record, jqOut []string, jqErr string) (lines []line, closed []int) {
	b := &builder{}
	switch {
	case r.Error != "":
		for i, l := range strings.Split(r.Error, "\n") {
			st := theme.Error
			if i > 0 {
				st = theme.Dim
			}
			b.add(line{text: st.Render(l), section: i == 0})
		}
	case r.Skipped:
		b.add(line{text: theme.Dim.Render("a pre-request script skipped this request (pm.execution.skipRequest)"), section: true})
	}
	if len(r.Missing) > 0 {
		vars := make([]string, len(r.Missing))
		for i, m := range r.Missing {
			vars[i] = "{{" + m + "}}"
		}
		b.add(line{text: theme.Warn.Render("not set: " + strings.Join(vars, ", ") + " — :vars sets them"), section: true})
	}
	for _, e := range r.ScriptErrs {
		for i, l := range strings.Split(e, "\n") {
			st := theme.Error
			if i > 0 {
				st = theme.Dim
			}
			text := l
			if i == 0 {
				text = "script error: " + l
			}
			b.add(line{text: st.Render(text), section: i == 0})
		}
	}
	if len(r.Tests) > 0 {
		passed, failed, skipped := 0, 0, 0
		for _, t := range r.Tests {
			switch {
			case t.Skipped:
				skipped++
			case t.Passed:
				passed++
			default:
				failed++
			}
		}
		var parts []string
		if passed > 0 {
			parts = append(parts, theme.Success.Render(fmt.Sprintf("✓ %d passed", passed)))
		}
		if failed > 0 {
			parts = append(parts, theme.Error.Render(fmt.Sprintf("✗ %d failed", failed)))
		}
		if skipped > 0 {
			parts = append(parts, theme.Dim.Render(fmt.Sprintf("%d skipped", skipped)))
		}
		head := strings.Join(parts, theme.Dim.Render(" · "))
		start := b.add(line{text: theme.Dim.Render(theme.Expanded) + "tests " + head, section: true})
		for _, t := range r.Tests {
			switch {
			case t.Skipped:
				b.add(line{text: theme.Dim.Render("    - " + t.Name)})
			case t.Passed:
				b.add(line{text: "    " + theme.Success.Render("✓") + " " + t.Name})
			default:
				b.add(line{text: "    " + theme.Error.Render("✗") + " " + t.Name})
				for _, el := range strings.Split(t.Error, "\n") {
					b.add(line{text: theme.Dim.Render("      " + el)})
				}
			}
		}
		b.closeFold(start, theme.Dim.Render(theme.Collapsed)+"tests "+head)
		if failed == 0 {
			closed = append(closed, start)
		}
	}
	if len(r.Logs) > 0 {
		errors := 0
		for _, l := range r.Logs {
			if l.Level == "error" || l.Level == "warn" {
				errors++
			}
		}
		head := fmt.Sprintf("console · %d %s", len(r.Logs), plural(len(r.Logs), "line"))
		start := b.add(line{text: theme.Dim.Render(theme.Expanded) + head, section: true})
		for _, l := range r.Logs {
			st := theme.Plain
			switch l.Level {
			case "error":
				st = theme.Error
			case "warn":
				st = theme.Warn
			case "info", "debug":
				st = theme.Dim
			}
			for _, tl := range strings.Split(l.Text, "\n") {
				b.add(line{text: "    " + st.Render(tl), value: tl})
			}
		}
		b.closeFold(start, theme.Dim.Render(theme.Collapsed)+head)
		if errors == 0 && len(r.Logs) > 3 {
			closed = append(closed, start)
		}
	}
	if r.Visual != nil {
		b.add(line{text: theme.Dim.Render("▪ ") + "visualization " + theme.Dim.Render("· gV shows it in the browser"), section: true})
	}
	if r.Status > 0 || len(r.Headers) > 0 {
		head := fmt.Sprintf("%d %s", len(r.Headers), plural(len(r.Headers), "header"))
		if len(r.Cookies) > 0 {
			head += fmt.Sprintf(" · %d %s set", len(r.Cookies), plural(len(r.Cookies), "cookie"))
		}
		start := b.add(line{text: theme.Dim.Render(theme.Expanded) + head, section: true})
		for _, h := range r.Headers {
			b.add(line{text: "    " + theme.Key.Render(h.Name) + theme.Dim.Render(": ") + h.Value, value: h.Value, path: h.Name})
		}
		for _, c := range r.Cookies {
			extra := ""
			if !c.Expires.IsZero() {
				extra += " · until " + c.Expires.Local().Format("2 Jan 15:04")
			}
			if c.HTTPOnly {
				extra += " · httponly"
			}
			b.add(line{text: "    " + theme.Dim.Render("cookie ") + theme.Key.Render(c.Name) + theme.Dim.Render("=") + c.Value + theme.Dim.Render(extra), value: c.Value})
		}
		b.closeFold(start, theme.Dim.Render(theme.Collapsed)+head)
		closed = append(closed, start)
	}
	if r.Method != "" {
		head := theme.Dim.Render("sent ") + r.Method + " " + r.URL
		start := b.add(line{text: theme.Dim.Render(theme.Expanded) + head, section: true, value: r.URL})
		if r.Auth != "" {
			b.add(line{text: "    " + theme.Dim.Render("auth  ") + r.Auth})
		}
		for _, rd := range r.Redirects {
			b.add(line{text: "    " + theme.Dim.Render(fmt.Sprintf("%d → ", rd.Status)) + rd.To, value: rd.To})
		}
		for _, h := range r.RequestHeaders {
			name := h.Name
			st := theme.Key
			if h.System {
				st = theme.Dim
			}
			b.add(line{text: "    " + st.Render(name) + theme.Dim.Render(": ") + h.Value, value: h.Value, path: name})
		}
		if r.RequestBody != "" {
			b.add(line{text: ""})
			body := r.RequestBody
			if pretty, ok := prettyJSON(body); ok {
				body = pretty
			}
			for _, bl := range strings.Split(body, "\n") {
				b.add(line{text: "    " + bl, value: bl})
			}
		}
		b.closeFold(start, theme.Dim.Render(theme.Collapsed)+head)
		closed = append(closed, start)
	}
	if r.Status > 0 {
		t := r.Timings
		head := fmt.Sprintf("%s · %s", Duration(r.Duration), send.Size(r.HeaderSize+r.WireSize))
		if r.Proto != "" {
			head += " · " + r.Proto
		}
		start := b.add(line{text: theme.Dim.Render(theme.Expanded) + head, section: true})
		var phases []string
		add := func(name string, d time.Duration) {
			if d > 0 {
				phases = append(phases, name+" "+Duration(d))
			}
		}
		add("dns", t.DNS)
		add("connect", t.Connect)
		add("tls", t.TLS)
		add("waiting", t.Wait)
		add("download", t.Download)
		if t.Reused {
			phases = append(phases, "connection reused")
		}
		if len(phases) > 0 {
			b.add(line{text: "    " + theme.Dim.Render(strings.Join(phases, " · "))})
		}
		if r.WireSize != r.BodySize && r.WireSize > 0 {
			b.add(line{text: "    " + theme.Dim.Render(fmt.Sprintf("body %s, %s as sent (compressed)", send.Size(r.BodySize), send.Size(r.WireSize)))})
		}
		if r.Remote != "" {
			b.add(line{text: "    " + theme.Dim.Render("from "+r.Remote)})
		}
		if tls := r.TLS; tls != nil {
			text := tls.Version + " · " + tls.Cipher
			if tls.Subject != "" {
				text += " · " + tls.Subject + " by " + tls.Issuer + " until " + tls.NotAfter.Format("2 Jan 2006")
			}
			if !tls.Verified {
				text += " · not verified"
			}
			b.add(line{text: "    " + theme.Dim.Render(text)})
		}
		b.closeFold(start, theme.Dim.Render(theme.Collapsed)+head)
		closed = append(closed, start)
	}
	if b.lines != nil && (r.Status > 0 || len(jqOut) > 0) {
		b.add(line{text: ""})
	}
	if jqOut != nil || jqErr != "" {
		// What jq made stands for the body: the cursor starts on it and
		// ]] finds it.
		first := len(b.lines)
		if jqErr != "" {
			b.add(line{text: theme.Error.Render("jq: " + jqErr)})
		}
		for _, out := range jqOut {
			jsonLines(b, []byte(out))
		}
		if first < len(b.lines) {
			b.lines[first].section = true
		}
		for i := first; i < len(b.lines); i++ {
			b.lines[i].body = true
		}
		return b.lines, closed
	}
	if r.Status > 0 {
		body(b, r)
	}
	return b.lines, closed
}

// body lays out the response body by what it is.
func body(b *builder, r *history.Record) {
	ct := r.ContentType()
	data := r.Body
	first := len(b.lines)
	defer func() {
		if first < len(b.lines) {
			b.lines[first].section = true
		}
		for i := first; i < len(b.lines); i++ {
			b.lines[i].body = true
		}
	}()
	switch {
	case len(data) == 0:
		b.add(line{text: theme.Dim.Render("no body")})
		return
	case binary(ct, data):
		b.add(line{text: theme.Dim.Render(fmt.Sprintf("%s · %s · :w FILE saves it, gx opens it", orUnknown(ct), send.Size(int64(len(data)))))})
		return
	}
	if strings.Contains(ct, "json") || ct == "" || strings.HasPrefix(strings.TrimSpace(string(data)), "{") || strings.HasPrefix(strings.TrimSpace(string(data)), "[") {
		if json.Valid(data) {
			jsonLines(b, data)
			truncated(b, r)
			return
		}
	}
	if strings.Contains(ct, "xml") || (strings.HasPrefix(strings.TrimSpace(string(data)), "<?xml")) {
		if out, ok := prettyXML(data); ok {
			for _, l := range strings.Split(out, "\n") {
				b.add(line{text: xmlLine(l), plain: l, value: l})
			}
			truncated(b, r)
			return
		}
	}
	for _, l := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
		l = strings.ReplaceAll(l, "\t", "    ")
		b.add(line{text: l, value: l})
	}
	truncated(b, r)
}

func truncated(b *builder, r *history.Record) {
	if r.Truncated {
		b.add(line{text: theme.Warn.Render(fmt.Sprintf("… cut at %s (:set maxresponse raises the limit)", send.Size(int64(len(r.Body)))))})
	}
}

func orUnknown(ct string) string {
	if ct == "" {
		return "binary"
	}
	return ct
}

// binary reports whether a body isn't text.
func binary(ct string, data []byte) bool {
	switch {
	case strings.HasPrefix(ct, "image/"), strings.HasPrefix(ct, "audio/"), strings.HasPrefix(ct, "video/"),
		ct == "application/pdf", ct == "application/octet-stream", ct == "application/zip", strings.Contains(ct, "protobuf"):
		return true
	}
	sample := data[:min(len(data), 4096)]
	if bytes.IndexByte(sample, 0) >= 0 {
		return true
	}
	return !utf8.Valid(sample) && !utf8.Valid(data)
}

// jsonLines lays out JSON with a fold for every object and array, keys in
// the order they came.
func jsonLines(b *builder, data []byte) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return
		}
		jsonValue(b, dec, tok, "", "", 0, true, ".")
	}
}

// jsonValue adds the lines of the value that starts with tok. prefix is
// what comes before it on its line (a key), last says no comma follows.
func jsonValue(b *builder, dec *json.Decoder, tok json.Token, prefix, plainPrefix string, depth int, last bool, path string) {
	indent := strings.Repeat("  ", depth)
	comma, plainComma := "", ""
	if !last {
		comma, plainComma = theme.Punct.Render(","), ","
	}
	switch t := tok.(type) {
	case json.Delim:
		open, close := string(t), "}"
		if t == '[' {
			close = "]"
		}
		start := b.add(line{text: indent + prefix + theme.Punct.Render(open), plain: indent + plainPrefix + open, path: path})
		n := 0
		for dec.More() {
			var childPrefix, childPlain, childPath string
			if t == '{' {
				kt, err := dec.Token()
				if err != nil {
					return
				}
				key, _ := kt.(string)
				q, _ := jsonx.Marshal(key)
				childPrefix = theme.Key.Render(string(q)) + theme.Punct.Render(": ")
				childPlain = string(q) + ": "
				childPath = joinPath(path, key)
			} else {
				childPath = fmt.Sprintf("%s[%d]", strings.TrimSuffix(path, "."), n)
				if path == "." {
					childPath = fmt.Sprintf(".[%d]", n)
				}
			}
			vt, err := dec.Token()
			if err != nil {
				return
			}
			// Whether more follows is known only after the child.
			mark := len(b.lines)
			jsonValue(b, dec, vt, childPrefix, childPlain, depth+1, false, childPath)
			if !dec.More() {
				// The last child: drop its comma.
				l := &b.lines[mark]
				if l.end > 0 {
					l = &b.lines[l.end]
				}
				l.text = strings.TrimSuffix(l.text, theme.Punct.Render(","))
				l.plain = strings.TrimSuffix(l.plain, ",")
				if b.lines[mark].end > 0 {
					b.lines[mark].summary = strings.TrimSuffix(b.lines[mark].summary, theme.Punct.Render(","))
				}
			}
			n++
		}
		_, _ = dec.Token() // the closing delimiter
		b.add(line{text: indent + theme.Punct.Render(close) + comma, plain: indent + close + plainComma})
		what := plural(n, "key")
		if t == '[' {
			what = plural(n, "item")
		}
		if n == 0 {
			// {} and [] stay on one line.
			b.lines = b.lines[:start+1]
			b.lines[start].text = indent + prefix + theme.Punct.Render(open+close) + comma
			b.lines[start].plain = indent + plainPrefix + open + close + plainComma
			b.lines[start].value = open + close
			return
		}
		b.closeFold(start, indent+prefix+theme.Punct.Render(open+"…"+close)+theme.Dim.Render(fmt.Sprintf(" %d %s", n, what))+comma)
	case string:
		q, _ := jsonx.Marshal(t)
		b.add(line{text: indent + prefix + theme.String.Render(string(q)) + comma, plain: indent + plainPrefix + string(q) + plainComma, path: path, value: t})
	case json.Number:
		b.add(line{text: indent + prefix + theme.Number.Render(t.String()) + comma, plain: indent + plainPrefix + t.String() + plainComma, path: path, value: t.String()})
	case bool:
		v := strconv.FormatBool(t)
		b.add(line{text: indent + prefix + theme.Literal.Render(v) + comma, plain: indent + plainPrefix + v + plainComma, path: path, value: v})
	case nil:
		b.add(line{text: indent + prefix + theme.Literal.Render("null") + comma, plain: indent + plainPrefix + "null" + plainComma, path: path, value: "null"})
	}
}

// joinPath adds a key to a jq path.
func joinPath(path, key string) string {
	ident := key != ""
	for i, r := range key {
		if !(r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || i > 0 && r >= '0' && r <= '9') {
			ident = false
			break
		}
	}
	base := strings.TrimSuffix(path, ".")
	if ident {
		return base + "." + key
	}
	q, _ := jsonx.Marshal(key)
	return base + "[" + string(q) + "]"
}

func plural(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}

// prettyJSON indents a JSON text, keeping it as it is when it isn't JSON.
func prettyJSON(s string) (string, bool) {
	var buf bytes.Buffer
	if err := json.Indent(&buf, []byte(strings.TrimSpace(s)), "", "  "); err != nil {
		return s, false
	}
	return buf.String(), true
}

// prettyXML indents XML.
func prettyXML(data []byte) (string, bool) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = false
	var out bytes.Buffer
	enc := xml.NewEncoder(&out)
	enc.Indent("", "  ")
	for {
		tok, err := dec.RawToken()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", false
		}
		if cd, ok := tok.(xml.CharData); ok && len(bytes.TrimSpace(cd)) == 0 {
			continue
		}
		if err := enc.EncodeToken(xml.CopyToken(tok)); err != nil {
			return "", false
		}
	}
	if err := enc.Flush(); err != nil {
		return "", false
	}
	return out.String(), true
}

// xmlLine colors tags in a line of XML.
func xmlLine(l string) string {
	var b strings.Builder
	in := false
	for _, r := range l {
		switch {
		case r == '<':
			in = true
			b.WriteString(theme.Key.Render("<"))
		case r == '>':
			in = false
			b.WriteString(theme.Key.Render(">"))
		case in:
			b.WriteString(theme.Key.Render(string(r)))
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Duration writes a duration for people: 143 ms, 1.2 s, 2m 3s.
func Duration(d time.Duration) string {
	switch {
	case d < time.Millisecond:
		return fmt.Sprintf("%d µs", d.Microseconds())
	case d < time.Second:
		return fmt.Sprintf("%d ms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%.1f s", d.Seconds())
	}
	return fmt.Sprintf("%dm %ds", int(d.Minutes()), int(d.Seconds())%60)
}

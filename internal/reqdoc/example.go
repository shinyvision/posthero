package reqdoc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/shinyvision/posthero/internal/postman"
)

// An example is a response saved with its request: what was sent and what
// came back. Its document is the request as a request's reads (without
// scripts and settings: an example has none of its own), then the
// response after a ### response line with its status: headers, a blank
// line and the body, as HTTP has them.
//
//	GET {{base}}/products/p-1001
//	Accept: application/json
//
//	### response 200 OK
//	Content-Type: application/json
//
//	{
//	  "id": "p-1001"
//	}

const secResponse = "response"

// responseLine is the line an example's response starts with; it holds
// the status.
var responseLine = regexp.MustCompile(`^###\s+(?i:response)(?:\s+(.*?))?\s*$`)

// responseAt finds the ### response line, or -1.
func responseAt(lines []string) int {
	for i, l := range lines {
		if responseLine.MatchString(l) {
			return i
		}
	}
	return -1
}

// RequestPart is the request of a document: an example's text before its
// response, a request's whole text.
func RequestPart(text string) string {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	if at := responseAt(lines); at >= 0 {
		return strings.Join(lines[:at], "\n")
	}
	return text
}

// RenderExample writes an example of a request as a document. Its request
// is the one it was saved with, or the request's own when it has none.
func RenderExample(it *postman.Item, r *postman.Response) string {
	req := r.OriginalRequest
	if req == nil && it != nil {
		req = it.Request
	}
	var b strings.Builder
	b.WriteString(Render(&postman.Item{Request: req}))
	b.WriteString("\n### " + secResponse)
	if s := statusOf(r); s != "" {
		b.WriteString(" " + s)
	}
	b.WriteByte('\n')
	for _, h := range r.Header {
		if blank(h) {
			continue
		}
		if h.Disabled {
			b.WriteString("# ")
		}
		b.WriteString(headerKey(h.Key) + ": " + quoteValue(string(h.Value)) + "\n")
	}
	if body := ShowBody(r.Body); body != "" {
		b.WriteString("\n" + escape(body) + "\n")
	}
	return b.String()
}

// statusOf is an example's status as its ### response line has it.
func statusOf(r *postman.Response) string {
	if r.Code == 0 {
		return ""
	}
	return strings.TrimSpace(fmt.Sprintf("%d %s", r.Code, r.Status))
}

// ShowBody is a response body as a document shows it: JSON on one line
// spread out to read, other text as it is (but for blank lines around it).
func ShowBody(body string) string {
	t := trimEnd(body)
	if t == "" || strings.Contains(t, "\n") || t[0] != '{' && t[0] != '[' {
		return t
	}
	var buf bytes.Buffer
	if json.Indent(&buf, []byte(t), "", "  ") != nil {
		return t
	}
	return buf.String()
}

// ApplyExample reads an edited example document into a copy of the
// example. What the document doesn't show (descriptions of headers, the
// cookies, ids) is kept, and so is a body that only reads differently:
// JSON spread out to read, or without the blank space it ended with.
func ApplyExample(it *postman.Item, old *postman.Response, text string) (*postman.Response, error) {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	at := responseAt(lines)
	if at < 0 {
		return nil, errorf(max(1, len(lines)), "an example's response starts with ### response and its status, like ### response 200 OK")
	}
	reqText := strings.Join(lines[:at], "\n")
	_, sections, err := splitSections(reqText)
	if err != nil {
		return nil, err
	}
	for _, s := range sections {
		switch s.name {
		case secPre, secTests, secSettings:
			return nil, errorf(s.line, "an example has no ### %s of its own; sending it runs its request's", s.name)
		}
	}
	oldReq := old.OriginalRequest
	if oldReq == nil && it != nil {
		oldReq = it.Request
	}
	got, err := Apply(&postman.Item{Name: old.Name, Request: oldReq}, reqText)
	if err != nil {
		return nil, err
	}
	r := old.Clone()
	if Render(got) != Render(&postman.Item{Request: oldReq}) {
		// A request that reads as it did stays as it was, to the byte.
		r.OriginalRequest = got.Request
	}

	m := responseLine.FindStringSubmatch(lines[at])
	if r.Code, r.Status, err = parseStatus(m[1], at+1, old); err != nil {
		return nil, err
	}
	headers, body, err := parseResponse(lines[at+1:], at+2)
	if err != nil {
		return nil, err
	}
	r.Header = mergeParams(old.Header, headers)
	r.Body = keepBody(old.Body, body)
	if r.Body != old.Body {
		r.PreviewLanguage = previewLanguage(r)
	}
	return r, nil
}

// parseStatus reads the status after ### response: a code and maybe its
// text (the usual one for the code when there is none).
func parseStatus(arg string, line int, old *postman.Response) (int, string, error) {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		if old.Code == 0 {
			return 0, old.Status, nil // it had none, and still has none
		}
		return 0, "", errorf(line, "say the status after ### response, like ### response 200 OK")
	}
	num, text, _ := strings.Cut(arg, " ")
	code, err := strconv.Atoi(num)
	if err != nil || code < 100 || code > 999 {
		return 0, "", errorf(line, "the status starts with its code, like ### response 404 Not Found")
	}
	text = strings.TrimSpace(text)
	if text == "" {
		if code == old.Code {
			text = old.Status
		} else {
			text = http.StatusText(code)
		}
	}
	return code, text, nil
}

// parseResponse reads the lines under ### response: headers up to a blank
// line, then the body. A first line that isn't a header starts the body:
// a response without headers.
func parseResponse(lines []string, first int) ([]field, string, error) {
	var headers []field
	i := 0
	if len(lines) > 0 && strings.TrimSpace(lines[0]) != "" {
		if _, _, err := headerLine(lines[0], first); err != nil {
			return nil, body(lines), nil
		}
		for ; i < len(lines) && strings.TrimSpace(lines[i]) != ""; i++ {
			h, ok, err := headerLine(lines[i], first+i)
			if err != nil {
				return nil, "", errorf(first+i, "a response header is a name and a value, like Content-Type: application/json (a blank line comes before the body)")
			}
			if ok {
				headers = append(headers, h)
			}
		}
	}
	return headers, body(lines[i:]), nil
}

func body(lines []string) string {
	return unescape(trimEnd(strings.Join(lines, "\n")))
}

// keepBody is the body the document says, or the old one when the
// document only shows it differently.
func keepBody(old, now string) string {
	if trimEnd(old) == now || ShowBody(old) == now || sameJSON(old, now) {
		return old
	}
	return now
}

// sameJSON reports whether two texts are the same JSON, written the same
// but for the blank space between its parts.
func sameJSON(a, b string) bool {
	var ca, cb bytes.Buffer
	if json.Compact(&ca, []byte(a)) != nil || json.Compact(&cb, []byte(b)) != nil {
		return false
	}
	return bytes.Equal(ca.Bytes(), cb.Bytes())
}

// previewLanguage is the language Postman shows a body in: from the
// Content-Type header, or else from the body itself.
func previewLanguage(r *postman.Response) string {
	if ct, ok := r.Header.Get("Content-Type"); ok {
		ct = strings.ToLower(ct)
		switch {
		case strings.Contains(ct, "json"):
			return "json"
		case strings.Contains(ct, "html"):
			return "html"
		case strings.Contains(ct, "xml"):
			return "xml"
		case strings.Contains(ct, "javascript"):
			return "javascript"
		}
		return "text"
	}
	switch lang := detectRaw(r.Body); lang {
	case "json", "xml", "html":
		return lang
	}
	return "text"
}

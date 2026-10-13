package auth

import (
	"io"
	"net/url"
	"regexp"
	"strings"

	"golang.org/x/net/html"
)

// page is an HTML page of a login: its forms and what it says.
type page struct {
	url     *url.URL
	title   string
	message string
	forms   []*htmlForm
	scripts int
	text    string
}

type htmlForm struct {
	action  *url.URL
	method  string
	id      string
	inputs  []*htmlInput
	buttons []FormButton
}

type htmlInput struct {
	name, typ, value, id, label, placeholder, autocomplete string
	checked, readonly                                      bool
	// invalid says the page marks the input as wrongly filled in
	// (aria-invalid, as Keycloak does after a wrong password).
	invalid bool
	options []string // a select's values
}

// parsePage reads an HTML page.
func parsePage(base *url.URL, r io.Reader) (*page, error) {
	doc, err := html.Parse(r)
	if err != nil {
		return nil, err
	}
	p := &page{url: base}
	labels := map[string]string{}
	var messages []string
	var text strings.Builder
	var walk func(n *html.Node, f *htmlForm)
	walk = func(n *html.Node, f *htmlForm) {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "script":
				p.scripts++
				return
			case "style", "noscript", "template":
				return
			case "title":
				p.title = strings.TrimSpace(nodeText(n))
				return
			case "form":
				nf := &htmlForm{method: strings.ToUpper(attr(n, "method")), id: attr(n, "id")}
				if nf.method == "" {
					nf.method = "GET"
				}
				nf.action = base
				if a := attr(n, "action"); a != "" {
					if u, err := base.Parse(html.UnescapeString(a)); err == nil {
						nf.action = u
					}
				}
				p.forms = append(p.forms, nf)
				f = nf
			case "label":
				if id := attr(n, "for"); id != "" {
					labels[id] = cleanSpace(nodeText(n))
				}
			case "input":
				if f != nil {
					in := &htmlInput{name: attr(n, "name"), typ: strings.ToLower(attr(n, "type")), value: attr(n, "value"), id: attr(n, "id"),
						placeholder: attr(n, "placeholder"), autocomplete: attr(n, "autocomplete")}
					_, in.checked = attrOK(n, "checked")
					_, in.readonly = attrOK(n, "readonly")
					in.invalid = strings.EqualFold(attr(n, "aria-invalid"), "true")
					if in.typ == "" {
						in.typ = "text"
					}
					switch in.typ {
					case "submit", "image":
						if in.name != "" || in.value != "" {
							f.buttons = append(f.buttons, FormButton{Name: in.name, Value: in.value, Label: firstNonEmpty(in.value, in.name, "Submit")})
						}
					case "button", "reset", "file":
					default:
						f.inputs = append(f.inputs, in)
					}
				}
			case "button":
				if f != nil {
					typ := strings.ToLower(attr(n, "type"))
					if typ == "" || typ == "submit" {
						f.buttons = append(f.buttons, FormButton{Name: attr(n, "name"), Value: attr(n, "value"), Label: firstNonEmpty(cleanSpace(nodeText(n)), attr(n, "value"), "Submit")})
					}
				}
				return
			case "select":
				if f != nil {
					in := &htmlInput{name: attr(n, "name"), typ: "select", id: attr(n, "id")}
					for c := n.FirstChild; c != nil; c = c.NextSibling {
						if c.Type == html.ElementNode && c.Data == "option" {
							v, ok := attrOK(c, "value")
							if !ok {
								v = cleanSpace(nodeText(c))
							}
							in.options = append(in.options, v)
							if _, sel := attrOK(c, "selected"); sel || in.value == "" {
								in.value = v
							}
						}
					}
					f.inputs = append(f.inputs, in)
				}
				return
			case "textarea":
				if f != nil {
					f.inputs = append(f.inputs, &htmlInput{name: attr(n, "name"), typ: "textarea", value: nodeText(n), id: attr(n, "id")})
				}
				return
			}
			if isMessage(n) {
				if t := cleanSpace(nodeText(n)); t != "" && len(t) < 400 {
					messages = append(messages, t)
				}
			}
		}
		if n.Type == html.TextNode {
			text.WriteString(n.Data)
			text.WriteByte(' ')
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c, f)
		}
	}
	walk(doc, nil)
	for _, f := range p.forms {
		for _, in := range f.inputs {
			if l, ok := labels[in.id]; ok && in.id != "" {
				in.label = l
			}
		}
	}
	p.message = dedupe(messages)
	p.text = cleanSpace(text.String())
	return p, nil
}

// isMessage reports whether an element shows an error or notice, by the
// names login pages give them (Keycloak's input-error and kc-feedback-text,
// alerts, …).
var messageName = regexp.MustCompile(`(?i)(^|[-_ ])(error|errors|alert|feedback|message|notification|warning)([-_ ]|$)|input-error|kc-feedback`)

func isMessage(n *html.Node) bool {
	if n.Data == "input" || n.Data == "form" || n.Data == "body" || n.Data == "html" {
		return false
	}
	if attr(n, "role") == "alert" {
		return true
	}
	return messageName.MatchString(attr(n, "id")) || messageName.MatchString(attr(n, "class"))
}

func dedupe(list []string) string {
	var out []string
	for _, s := range list {
		dup := false
		for i, o := range out {
			switch {
			case strings.Contains(o, s):
				dup = true
			case strings.Contains(s, o):
				out[i], dup = s, true
			}
		}
		if !dup {
			out = append(out, s)
		}
	}
	return strings.Join(out, " ")
}

func attr(n *html.Node, name string) string {
	v, _ := attrOK(n, name)
	return v
}

func attrOK(n *html.Node, name string) (string, bool) {
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, name) {
			return a.Val, true
		}
	}
	return "", false
}

func nodeText(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		if n.Type == html.ElementNode && (n.Data == "script" || n.Data == "style") {
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return b.String()
}

func cleanSpace(s string) string { return strings.Join(strings.Fields(s), " ") }

func firstNonEmpty(list ...string) string {
	for _, s := range list {
		if s != "" {
			return s
		}
	}
	return ""
}

var userField = regexp.MustCompile(`(?i)user|e-?mail|login|identifier|j_username|loginfmt|account`)

// password returns a form's password input.
func (f *htmlForm) password() *htmlInput {
	for _, in := range f.inputs {
		if in.typ == "password" {
			return in
		}
	}
	return nil
}

// newPassword reports whether a form sets a password rather than logs in
// with one: it has several password inputs (a new one and its repetition),
// or one the page says is for a new password.
func (f *htmlForm) newPassword() bool {
	n := 0
	for _, in := range f.inputs {
		if in.typ == "password" {
			if strings.EqualFold(in.autocomplete, "new-password") {
				return true
			}
			n++
		}
	}
	return n > 1
}

// newPasswordValue is the new password in a filled-in form that sets one:
// what its new-password inputs agree on (or, without such inputs, its two
// password inputs); empty when it can't tell.
func (f *htmlForm) newPasswordValue(vals url.Values) string {
	var marked, all []*htmlInput
	for _, in := range f.inputs {
		if in.typ == "password" {
			all = append(all, in)
			if strings.EqualFold(in.autocomplete, "new-password") {
				marked = append(marked, in)
			}
		}
	}
	if len(marked) == 0 && len(all) == 2 {
		marked = all
	}
	pw := ""
	for i, in := range marked {
		v := vals.Get(in.name)
		if v == "" || i > 0 && v != pw {
			return ""
		}
		pw = v
	}
	return pw
}

// shape tells login forms apart: where they post, and their user name and
// password inputs. A login form that comes back has the same shape.
func (f *htmlForm) shape() string {
	var where, pw, un string
	if f.action != nil {
		where = f.action.Scheme + "://" + f.action.Host + f.action.Path
	}
	if in := f.password(); in != nil {
		pw = in.name
	}
	if in := f.username(); in != nil {
		un = in.name
	}
	return where + "\x00" + pw + "\x00" + un
}

// badCredentials is how login pages say the user name or password is wrong
// ("Invalid username or password.", "Your account or password is
// incorrect", "Bad credentials").
var badCredentials = regexp.MustCompile(`(?i)\b(invalid|incorrect|wrong|bad|unknown)\b[^.!]*\b(password|credentials)\b|\b(password|credentials)\b[^.!]*\b(invalid|incorrect|wrong|not (correct|valid|recogni[sz]ed))\b`)

// refusedLogin reports whether a login form that came back says the user
// name or password is wrong, rather than something else (the account is
// disabled): its user name or password input is marked wrongly filled in,
// which Keycloak does in any language, or the page says so.
func refusedLogin(pg *page, f *htmlForm) bool {
	for _, in := range []*htmlInput{f.password(), f.username()} {
		if in != nil && in.invalid {
			return true
		}
	}
	return badCredentials.MatchString(pg.message)
}

// username returns the input a user name goes in.
func (f *htmlForm) username() *htmlInput {
	var best *htmlInput
	for _, in := range f.inputs {
		if in.typ != "text" && in.typ != "email" {
			continue
		}
		if in.autocomplete == "username" || in.autocomplete == "email" {
			return in
		}
		if best == nil && (userField.MatchString(in.name) || userField.MatchString(in.id)) {
			best = in
		}
	}
	return best
}

// visible are the inputs a person fills in.
func (f *htmlForm) visible() []*htmlInput {
	var out []*htmlInput
	for _, in := range f.inputs {
		switch in.typ {
		case "hidden", "checkbox", "radio":
			continue
		}
		if in.name == "" || in.readonly {
			continue
		}
		out = append(out, in)
	}
	return out
}

// values are what the form sends as it is: hidden fields, checked boxes,
// filled inputs. Remember-me boxes are ticked, for a longer session.
func (f *htmlForm) values() url.Values {
	v := url.Values{}
	for _, in := range f.inputs {
		if in.name == "" {
			continue
		}
		switch in.typ {
		case "checkbox", "radio":
			if in.checked || in.typ == "checkbox" && regexp.MustCompile(`(?i)remember`).MatchString(in.name) {
				val := in.value
				if val == "" {
					val = "on"
				}
				v.Add(in.name, val)
			}
		default:
			v.Add(in.name, in.value)
		}
	}
	return v
}

// loginForm picks the form of a page that logs in: one with a password,
// else one asking for a user name, else the first.
func (p *page) loginForm() *htmlForm {
	for _, f := range p.forms {
		if f.password() != nil {
			return f
		}
	}
	for _, f := range p.forms {
		if f.username() != nil {
			return f
		}
	}
	if len(p.forms) > 0 {
		return p.forms[0]
	}
	return nil
}

// heading is what to call the page.
func (p *page) heading() string {
	t := p.title
	if t == "" {
		t = "Log in"
	}
	return t
}

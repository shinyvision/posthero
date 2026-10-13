package config

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/shinyvision/posthero/internal/send"
)

// Settings are posthero's options, changed with :set and read from
// postherorc at startup, the way vim reads vimrc.
type Settings struct {
	// Refresh is how often, in seconds, posthero asks Postman what changed;
	// 0 turns that off.
	Refresh int
	// Timeout ends a request after this many seconds; 0 waits forever.
	Timeout int
	// Verify checks servers' TLS certificates.
	Verify bool
	// FollowRedirects follows redirects.
	FollowRedirects bool
	// MaxResponse is the most of a response kept, in MB.
	MaxResponse int
	// Confirm asks before sending anything but a read to an environment
	// whose name says production.
	Confirm bool
	// Mouse turns the mouse on.
	Mouse bool
	// Wrap wraps long lines in the response.
	Wrap bool
	// ShowValues shows what variables are in the request editor.
	ShowValues bool
	// KeepAlive keeps OAuth 2.0 tokens used in the last this many hours
	// fresh, so logins don't run out while posthero is open.
	KeepAlive int
	// History is how many responses posthero remembers; 0 keeps none.
	History int
	// Proxy sends requests through a proxy (empty: $HTTPS_PROXY and
	// friends).
	Proxy string
	// CAFile adds certificate authorities to trust.
	CAFile string
	// Workdir is where relative file paths in requests start; empty is
	// where posthero started.
	Workdir string
	// Workspace is the Postman workspace to open (a name or id); empty
	// opens the last one.
	Workspace string
}

// DefaultSettings are the options before postherorc runs.
func DefaultSettings() Settings {
	return Settings{Refresh: 120, Verify: true, FollowRedirects: true, MaxResponse: 50, Confirm: true, Mouse: true, ShowValues: true, KeepAlive: 12, History: 1000}
}

// Send makes the request settings the options say.
func (s Settings) Send() send.Settings {
	out := send.DefaultSettings()
	out.StrictSSL = s.Verify
	out.FollowRedirects = s.FollowRedirects
	out.Timeout = time.Duration(s.Timeout) * time.Second
	out.MaxResponseSize = int64(s.MaxResponse) << 20
	out.Proxy = s.Proxy
	out.CAFile = s.CAFile
	return out
}

// option describes one :set option.
type option struct {
	name, short string
	help        string
	boolean     *bool
	number      *int
	text        *string
}

func (s *Settings) options() []option {
	return []option{
		{name: "cafile", help: "a PEM file of certificate authorities to trust too", text: &s.CAFile},
		{name: "confirm", short: "cf", help: "ask before changing things on an environment named prod", boolean: &s.Confirm},
		{name: "followredirects", short: "fr", help: "follow redirects", boolean: &s.FollowRedirects},
		{name: "history", short: "hi", help: "how many responses to remember; 0 keeps none", number: &s.History},
		{name: "keepalive", short: "ka", help: "hours after its last use a login is kept fresh", number: &s.KeepAlive},
		{name: "maxresponse", short: "mr", help: "the most of a response to keep, in MB", number: &s.MaxResponse},
		{name: "mouse", help: "use the mouse", boolean: &s.Mouse},
		{name: "proxy", help: "a proxy for every request (empty: $HTTPS_PROXY)", text: &s.Proxy},
		{name: "refresh", short: "rf", help: "seconds between checks for your team's changes, while posthero is in use; 0 is off", number: &s.Refresh},
		{name: "showvalues", short: "sv", help: "show what variables are next to them in the editor", boolean: &s.ShowValues},
		{name: "timeout", short: "to", help: "seconds before a request gives up; 0 waits forever", number: &s.Timeout},
		{name: "verify", short: "vf", help: "check servers' TLS certificates", boolean: &s.Verify},
		{name: "workdir", short: "wd", help: "where relative file paths start (empty: where posthero started)", text: &s.Workdir},
		{name: "workspace", short: "ws", help: "the Postman workspace to open; empty opens the last one", text: &s.Workspace},
		{name: "wrap", help: "wrap long lines in the response", boolean: &s.Wrap},
	}
}

// OptionNames lists every option, for completion.
func (s *Settings) OptionNames() []string {
	var out []string
	for _, o := range s.options() {
		out = append(out, o.name)
		if o.boolean != nil {
			out = append(out, "no"+o.name)
		}
	}
	sort.Strings(out)
	return out
}

func (s *Settings) find(name string) (option, bool) {
	for _, o := range s.options() {
		if name == o.name || (o.short != "" && name == o.short) {
			return o, true
		}
	}
	return option{}, false
}

// Set applies the arguments of a :set command, vim style: "name",
// "noname", "invname", "name!", "name?", "name=value", "all". A space in a
// value is written "\ ", and " starts a comment (see splitArgs). It returns
// what to show.
func (s *Settings) Set(args string) (string, error) {
	fields := splitArgs(args)
	if len(fields) == 0 {
		return s.describe(false), nil
	}
	var shown []string
	for i, f := range fields {
		if f == "all" {
			return s.describe(true), nil
		}
		msg, err := s.setOne(f)
		if err != nil {
			return "", s.spaceHint(err, fields, i)
		}
		if msg != "" {
			shown = append(shown, msg)
		}
	}
	return strings.Join(shown, "  "), nil
}

func (s *Settings) setOne(arg string) (string, error) {
	name, value, hasValue := strings.Cut(arg, "=")
	query := strings.HasSuffix(name, "?")
	name = strings.TrimSuffix(name, "?")
	toggle := strings.HasSuffix(name, "!")
	name = strings.TrimSuffix(name, "!")
	turn := 1
	if o, ok := s.find(name); ok {
		return s.apply(o, query, toggle, hasValue, value, turn)
	}
	switch {
	case strings.HasPrefix(name, "no"):
		name, turn = name[2:], 0
	case strings.HasPrefix(name, "inv"):
		name, toggle = name[3:], true
	}
	o, ok := s.find(name)
	if !ok {
		return "", fmt.Errorf("%w: %s", errUnknown, arg)
	}
	return s.apply(o, query, toggle, hasValue, value, turn)
}

var errUnknown = errors.New("E518: Unknown option")

// spaceHint explains an unknown option that is likely the rest of a value
// with a space in it ("set workspace=My Workspace").
func (s *Settings) spaceHint(err error, fields []string, i int) error {
	if !errors.Is(err, errUnknown) || i == 0 {
		return err
	}
	name, _, ok := strings.Cut(fields[i-1], "=")
	if o, found := s.find(name); !ok || !found || o.text == nil {
		return err
	}
	return fmt.Errorf("%w (a space in a value is written \\ : set %s\\ %s)", err, escapeArg(fields[i-1]), escapeArg(fields[i]))
}

// splitArgs splits the arguments of :set (and of cert) at white space, the
// way vim's :set does: "\ " is a space in a value, "\\" a backslash and
// "\"" a double quote, and a " starts a comment. A backslash before
// anything else stays, so Windows paths need no doubled backslashes.
func splitArgs(s string) []string {
	var out []string
	var b strings.Builder
	word := false
	end := func() {
		if word {
			out = append(out, b.String())
			b.Reset()
			word = false
		}
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\\' && i+1 < len(s) && escaped(s[i+1]):
			i++
			b.WriteByte(s[i])
			word = true
		case c == '"':
			end()
			return out
		case c == ' ' || c == '\t':
			end()
		default:
			b.WriteByte(c)
			word = true
		}
	}
	end()
	return out
}

// escaped reports whether a backslash before c escapes it.
func escaped(c byte) bool { return c == ' ' || c == '\t' || c == '\\' || c == '"' }

// escapeArg writes a value so splitArgs reads it back: white space and "
// get a backslash, and so does a backslash that would otherwise escape
// what follows it (or the space after the value).
func escapeArg(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\\' && (i+1 == len(s) || escaped(s[i+1])):
			b.WriteString(`\\`)
		case c != '\\' && escaped(c):
			b.WriteByte('\\')
			b.WriteByte(c)
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

func (s *Settings) apply(o option, query, toggle, hasValue bool, value string, turn int) (string, error) {
	switch {
	case query:
		return o.show(), nil
	case o.boolean != nil:
		if hasValue {
			return "", fmt.Errorf("E474: Invalid argument: %s=%s", o.name, value)
		}
		if toggle {
			*o.boolean = !*o.boolean
		} else {
			*o.boolean = turn == 1
		}
		return "", nil
	case !hasValue:
		return o.show(), nil
	case o.number != nil:
		n, err := strconv.Atoi(value)
		if err != nil || n < 0 {
			return "", fmt.Errorf("E521: Number required after =: %s=%s", o.name, value)
		}
		*o.number = n
	case o.text != nil:
		*o.text = value
	}
	return "", nil
}

func (o option) show() string {
	switch {
	case o.boolean != nil && *o.boolean:
		return "  " + o.name
	case o.boolean != nil:
		return "no" + o.name
	case o.number != nil:
		return fmt.Sprintf("  %s=%d", o.name, *o.number)
	}
	return "  " + o.name + "=" + *o.text
}

// describe lists the options that differ from the defaults, or all of
// them, with what they do.
func (s *Settings) describe(all bool) string {
	def := DefaultSettings()
	defOpts := def.options()
	table := s.Table()
	var lines []string
	for i, o := range s.options() {
		if !all && o.show() == defOpts[i].show() {
			continue
		}
		lines = append(lines, fmt.Sprintf("%-16s %-14s %s", table[i][0], table[i][1], table[i][2]))
	}
	if len(lines) == 0 {
		return "every option has its default value · :set all lists them"
	}
	return strings.Join(lines, "\n")
}

// Describe lists every option with its value and what it does.
func (s *Settings) Describe() string { return s.describe(true) }

// Table lists every option as name, current value ("on", "off" or the
// value) and what it does.
func (s *Settings) Table() [][3]string {
	var out [][3]string
	for _, o := range s.options() {
		val := ""
		switch {
		case o.boolean != nil && *o.boolean:
			val = "on"
		case o.boolean != nil:
			val = "off"
		case o.number != nil:
			val = strconv.Itoa(*o.number)
		default:
			val = *o.text
			if val == "" {
				val = "(empty)"
			}
		}
		out = append(out, [3]string{o.name, val, o.help})
	}
	return out
}

// Changed returns :set commands that recreate the options that differ from
// the defaults, for :mkpostherorc.
func (s *Settings) Changed() []string {
	def := DefaultSettings()
	defOpts := def.options()
	var out []string
	for i, o := range s.options() {
		switch {
		case o.show() == defOpts[i].show():
		case o.text != nil:
			out = append(out, "set "+o.name+"="+escapeArg(*o.text))
		default:
			out = append(out, "set "+strings.TrimSpace(o.show()))
		}
	}
	return out
}

// Mapping maps keys to other keys or a command, like vim's :map.
type Mapping struct{ LHS, RHS string }

// RC is what postherorc holds.
type RC struct {
	Settings Settings
	Maps     []Mapping
	// Certs are client certificates by host ("cert HOST CERT KEY").
	Certs []send.ClientCert
}

// RCPath is the startup file, like vimrc.
func RCPath() (string, error) { return configPath("postherorc") }

// LoadRC reads postherorc: ":set" lines, "map" lines and "cert" lines;
// lines starting with " or # are comments, and so is what follows a " on a
// set or cert line. Errors name the line; what could be read is returned
// all the same.
func LoadRC() (RC, error) {
	rc := RC{Settings: DefaultSettings()}
	path, err := RCPath()
	if err != nil {
		return rc, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return rc, nil
	}
	if err != nil {
		return rc, err
	}
	return rc, rc.parse(string(data))
}

// ParseRC reads postherorc text, for tests and :source.
func ParseRC(text string) (RC, error) {
	rc := RC{Settings: DefaultSettings()}
	return rc, rc.parse(text)
}

func (rc *RC) parse(text string) error {
	var errs []string
	sc := bufio.NewScanner(strings.NewReader(text))
	for n := 1; sc.Scan(); n++ {
		line := sc.Text()
		if n == 1 {
			line = strings.TrimPrefix(line, "\ufeff") // a byte order mark
		}
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), ":"))
		if line == "" || strings.HasPrefix(line, `"`) || strings.HasPrefix(line, "#") {
			continue
		}
		cmd, args := firstWord(line)
		var err error
		switch cmd {
		case "set", "se":
			_, err = rc.Settings.Set(args)
		case "map", "nmap", "nnoremap", "noremap":
			lhs, rhs := firstWord(args)
			if lhs == "" || rhs == "" {
				err = errors.New("map needs keys and what they do: map gE :env Shop - Local<CR>")
			} else {
				rc.Maps = append(rc.Maps, Mapping{LHS: lhs, RHS: rhs})
			}
		case "cert":
			f := splitArgs(args)
			switch len(f) {
			case 2:
				rc.Certs = append(rc.Certs, send.ClientCert{Host: f[0], PFX: f[1]})
			case 3:
				rc.Certs = append(rc.Certs, send.ClientCert{Host: f[0], Cert: f[1], Key: f[2]})
			default:
				err = errors.New("cert needs a host and the certificate and key files: cert api.example.com:443 cert.pem key.pem")
			}
		default:
			err = fmt.Errorf("unknown command %q (set, map and cert work here)", cmd)
		}
		if err != nil {
			errs = append(errs, fmt.Sprintf("postherorc line %d: %v", n, err))
		}
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "\n"))
	}
	return sc.Err()
}

// firstWord splits a line at its first white space (a space or a tab) into
// the word and the rest, trimmed.
func firstWord(line string) (word, rest string) {
	line = strings.TrimSpace(line)
	i := strings.IndexFunc(line, unicode.IsSpace)
	if i < 0 {
		return line, ""
	}
	return line[:i], strings.TrimSpace(line[i:])
}

// SaveRC writes postherorc with the options that differ from the defaults,
// the mappings and the certificates.
func SaveRC(rc RC) (string, error) {
	path, err := RCPath()
	if err != nil {
		return "", err
	}
	return path, writeFile(path, []byte(RCText(rc)), 0o600)
}

// RCText is postherorc as SaveRC writes it.
func RCText(rc RC) string {
	var b strings.Builder
	b.WriteString(`" posthero startup file, read when posthero starts. Press ? in posthero for more.` + "\n")
	b.WriteString(`" set OPTION            options, as with :set (:set all lists them; a space in a value is \ )` + "\n")
	b.WriteString(`" map KEYS ACTION       map keys, e.g. map gE :env Shop - Local<CR>` + "\n")
	b.WriteString(`" cert HOST CERT KEY    a client certificate for a host (HOST or HOST:PORT)` + "\n\n")
	for _, l := range rc.Settings.Changed() {
		b.WriteString(l + "\n")
	}
	if len(rc.Maps) > 0 {
		b.WriteString("\n")
	}
	for _, m := range rc.Maps {
		b.WriteString("map " + m.LHS + " " + m.RHS + "\n")
	}
	if len(rc.Certs) > 0 {
		b.WriteString("\n")
	}
	for _, c := range rc.Certs {
		if c.PFX != "" {
			b.WriteString("cert " + escapeArg(c.Host) + " " + escapeArg(c.PFX) + "\n")
		} else {
			b.WriteString("cert " + escapeArg(c.Host) + " " + escapeArg(c.Cert) + " " + escapeArg(c.Key) + "\n")
		}
	}
	return b.String()
}

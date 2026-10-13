package vim

import (
	"strconv"
	"strings"
	"testing"
)

// feed splits "ciwfoo<Esc>" into keys and feeds them.
func feed(e *Engine, s string) Action {
	var last Action
	for len(s) > 0 {
		k := s[:1]
		if s[0] == '<' {
			if end := strings.IndexByte(s, '>'); end > 1 {
				k = s[:end+1]
			}
		}
		s = s[len(k):]
		if a := e.HandleKey(k); a.Kind != None {
			last = a
		}
	}
	return last
}

func TestEditing(t *testing.T) {
	cases := []struct {
		name, text, keys, want string
		cur                    Pos
	}{
		{"x", "hello", "x", "ello", Pos{0, 0}},
		{"count x", "hello", "3x", "lo", Pos{0, 0}},
		{"dw", "foo bar baz", "dw", "bar baz", Pos{0, 0}},
		{"dw last word", "foo bar\nbaz", "wdw", "foo \nbaz", Pos{0, 3}},
		{"cw", "foo bar", "cwxyz<Esc>", "xyz bar", Pos{0, 2}},
		{"ciw", "foo bar baz", "wciwX<Esc>", "foo X baz", Pos{0, 4}},
		{"daw", "foo bar baz", "wdaw", "foo baz", Pos{0, 4}},
		{"ci(", "f(a, b)", "fbci(x<Esc>", "f(x)", Pos{0, 2}},
		{"da\"", `say "hi there" now`, `fhda"`, "say now", Pos{0, 4}},
		{"dd", "a\nb\nc", "jdd", "a\nc", Pos{1, 0}},
		{"2dd", "a\nb\nc", "2dd", "c", Pos{0, 0}},
		{"dj", "a\nb\nc", "dj", "c", Pos{0, 0}},
		{"yyp", "a\nb", "yyp", "a\na\nb", Pos{1, 0}},
		{"yiwP", "foo bar", "yiwwP", "foo foobar", Pos{0, 6}},
		{"D", "hello world", "wD", "hello ", Pos{0, 5}},
		{"C", "hello world", "wCthere<Esc>", "hello there", Pos{0, 10}},
		{"A", "abc", "Adef<Esc>", "abcdef", Pos{0, 5}},
		{"I", "  abc", "I-<Esc>", "  -abc", Pos{0, 2}},
		{"o", "  a", "ob<Esc>", "  a\n  b", Pos{1, 2}},
		{"O", "a", "Ob<Esc>", "b\na", Pos{0, 0}},
		{"undo", "abc", "xxu", "bc", Pos{0, 0}},
		{"redo", "abc", "xxuu<C-r>", "bc", Pos{0, 0}},
		{"insert undo is one step", "", "iabc<Esc>u", "", Pos{0, 0}},
		{"dot", "a b c d", "dw..", "d", Pos{0, 0}},
		{"dot insert", "x\ny", "Afoo<Esc>j.", "xfoo\nyfoo", Pos{1, 3}},
		{"3i", "", "3ia<Esc>", "aaa", Pos{0, 2}},
		{"J", "a\n  b", "J", "a b", Pos{0, 1}},
		{"r", "abc", "rx", "xbc", Pos{0, 0}},
		{"~", "abc", "~~", "ABc", Pos{0, 2}},
		{"gUiw", "foo bar", "gUiw", "FOO bar", Pos{0, 0}},
		{"f;", "a,b,c,d", "f,;D", "a,b", Pos{0, 2}},
		{"dt", "foo(bar)", "dt(", "(bar)", Pos{0, 0}},
		{"visual d", "hello world", "vlld", "lo world", Pos{0, 0}},
		{"visual line y p", "a\nb", "Vyjp", "a\nb\na", Pos{2, 0}},
		{"visual c", "hello", "vecX<Esc>", "X", Pos{0, 0}},
		{"visual iw", "foo bar", "wviwd", "foo ", Pos{0, 3}},
		{"visual p", "foo bar", "yiwwviwp", "foo foo", Pos{0, 6}},
		{">>", "a", ">>", "  a", Pos{0, 2}},
		{"<C-a>", "x = 41;", "<C-a>", "x = 42;", Pos{0, 5}},
		{"10<C-x>", "x = 5", "10<C-x>", "x = -5", Pos{0, 5}},
		{"G and gg", "a\nb\nc", "Gx", "a\nb\n", Pos{2, 0}},
		{"%", "(a (b) c)", "%x", "(a (b) c", Pos{0, 7}},
		{"search", "foo\nbar\nbaz", "/ba<CR>nx", "foo\nbar\naz", Pos{2, 0}},
		{"star", "foo bar foo", "*x", "foo bar oo", Pos{0, 8}},
		{"substitute", "a a\na", ":%s/a/b/g<CR>", "b b\nb", Pos{1, 0}},
		{"substitute groups", "foo=1", `:s/\(\w\+\)=\(\d\)/\2=\1/<CR>`, "1=foo", Pos{0, 0}},
		{"macro", "1\n1\n1", "qa<C-a>jq2@a", "2\n2\n2", Pos{2, 0}},
		{"named register", "a\nb", "\"ayyj\"ap", "a\nb\na", Pos{2, 0}},
		{"mark", "a\nb\nc", "majjd'a", "", Pos{0, 0}},
		{"cc keeps indent", "  foo", "ccbar<Esc>", "  bar", Pos{0, 4}},
		{"ip", "a\nb\n\nc", "dip", "\nc", Pos{0, 0}},
		{"de", "foo bar", "de", " bar", Pos{0, 0}},
		{"db", "foo bar", "$db", "foo r", Pos{0, 4}},
		{"ge", "foo bar", "$gex", "fo bar", Pos{0, 2}},
		{"R", "abcd", "Rxy<Esc>", "xycd", Pos{0, 1}},
		{"ci{ multiline", "f {\n  a\n  b\n}", "jci{x<Esc>", "f {\n  x\n}", Pos{1, 2}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := New(c.text, nil)
			e.SetHeight(20)
			feed(e, c.keys)
			if got := e.Text(); got != c.want {
				t.Errorf("text = %q, want %q", got, c.want)
			}
			if e.Cursor() != c.cur {
				t.Errorf("cursor = %+v, want %+v", e.Cursor(), c.cur)
			}
			if e.Mode() != Normal {
				t.Errorf("mode = %v, want NORMAL", e.Mode())
			}
		})
	}
}

func TestActions(t *testing.T) {
	e := New("select 1;\nselect 2;", nil)
	if a := feed(e, "<CR>"); a.Kind != Submit {
		t.Errorf("<CR> in normal = %+v", a)
	}
	if a := feed(e, "Vj<CR>"); a.Kind != SubmitSelection || a.Text != "select 1;\nselect 2;" {
		t.Errorf("<CR> in visual = %+v", a)
	}
	if a := feed(e, "<Esc>"); a.Kind != Cancel {
		t.Errorf("<Esc> in normal = %+v", a)
	}
	if a := feed(e, ":wq<CR>"); a.Kind != Ex || a.Text != "wq" {
		t.Errorf(":wq = %+v", a)
	}
	if a := feed(e, "g<CR>"); a.Kind != Explain {
		t.Errorf("g<CR> = %+v", a)
	}
	for keys, want := range map[string]string{"<C-w>o": "o", "<C-w><C-o>": "o", "<C-w><Left>": "h", "<C-w>|": "|", "3<C-w>+": "+"} {
		if a := feed(e, keys); a.Kind != Window || a.Text != want {
			t.Errorf("%s = %+v", keys, a)
		}
	}
	if a := feed(e, "<C-w>x"); a.Kind != None || e.Pending() != "" {
		t.Errorf("<C-w>x = %+v, pending %q", a, e.Pending())
	}
	single := New("", nil)
	single.SingleLine = true
	if a := feed(single, "iabc<CR>"); a.Kind != Submit || single.Text() != "abc" {
		t.Errorf("single line <CR> = %+v %q", a, single.Text())
	}
}

func TestClipboard(t *testing.T) {
	regs := NewRegisters()
	e := New("hello", regs)
	feed(e, "\"+yiw")
	if s, ok := regs.TakeClipboard(); !ok || s != "hello" {
		t.Errorf("clipboard = %q %v", s, ok)
	}
	if _, ok := regs.TakeClipboard(); ok {
		t.Error("clipboard should be consumed")
	}
}

func TestReplace(t *testing.T) {
	e := New("select us from t", nil)
	feed(e, "fsa")
	e.Replace(Pos{0, 7}, e.Cursor(), "users")
	if e.Text() != "select users from t" || e.Cursor() != (Pos{0, 12}) || e.Mode() != Insert {
		t.Fatalf("text %q cursor %+v", e.Text(), e.Cursor())
	}
	feed(e, "<Esc>u")
	if e.Text() != "select us from t" {
		t.Fatalf("undo: %q", e.Text())
	}
}

// TestMatchesVim holds regressions found by comparing with nvim --clean.
func TestMatchesVim(t *testing.T) {
	cases := []struct {
		name, text, keys, want string
		cur                    Pos
	}{
		// cw stops at the end of the word under the cursor.
		{"cw on one-char word", "a b c", "cwX<Esc>", "X b c", Pos{0, 0}},
		{"cw on last char", "foo bar", "llcwX<Esc>", "foX bar", Pos{0, 2}},
		{"2cw on one-char word", "a b c", "2cwX<Esc>", "X c", Pos{0, 0}},
		{"2cw on last char", "foo bar baz qux", "ll2cwX<Esc>", "foX baz qux", Pos{0, 2}},
		// A count on . replaces the count of the repeated command.
		{"dd3.", "1\n2\n3\n4\n5\n6\n7\n8\n9", "dd3.", "5\n6\n7\n8\n9", Pos{0, 0}},
		{"dd3..", "1\n2\n3\n4\n5\n6\n7\n8\n9", "dd3..", "8\n9", Pos{0, 0}},
		{"2d3w.", "a b c d e f g h i j k l m n", "2d3w.", "m n", Pos{0, 0}},
		{"2d3w1.", "a b c d e f g h i j k l m n", "2d3w1.", "h i j k l m n", Pos{0, 0}},
		{"3ia then .", "x\ny", "3ia<Esc>j.", "aaax\naaay", Pos{1, 2}},
		{"2o then .", "q", "2ox<Esc>.", "q\nx\nx\nx\nx", Pos{4, 0}},
		{"macro with counted insert", "", "qa2ix<Esc>q@a", "xxxx", Pos{0, 2}},
		// :s
		{":s with \\r", "a,b,c", `:s/,/\r/g<CR>`, "a\nb\nc", Pos{2, 0}},
		{":s with empty pattern", "foo bar foo\nfoo", "/foo<CR>:%s//X/g<CR>", "X bar X\nX", Pos{1, 0}},
		{":s sets the search pattern", "foo bar\nbar foo", ":s/bar/X/<CR>n", "foo X\nbar foo", Pos{1, 0}},
		{":s with & and groups", "foo=1 foo=2", `:s/\(o\+\)=/[&\1]/g<CR>`, "f[oo=oo]1 f[oo=oo]2", Pos{0, 0}},
		{":s anchored", "aaa", ":s/^a/X/g<CR>", "Xaa", Pos{0, 0}},
		{":s to nothing at the start", "foo bar", ":s/foo //<CR>", "bar", Pos{0, 0}},
		// The unnamed register keeps its text.
		{"visual p twice", "foo bar baz", "yiwwviwpwviwp", "foo foo bar", Pos{0, 10}},
		{"x on empty line", "abc\n\nz", "yyjxjp", "abc\n\nz\nabc", Pos{3, 0}},
		{"X at column 0", "abc\nqq", "yyjXp", "abc\nqq\nabc", Pos{2, 0}},
		{"s on empty line", "abc\n\nz", "yyjsQ<Esc>jp", "abc\nQ\nz\nabc", Pos{3, 0}},
		// * and #
		{"* on non-ASCII word", "café x café", "*", "café x café", Pos{0, 7}},
		{"* on punctuation uses the next keyword", "a == b == c", "w*", "a == b == c", Pos{0, 5}},
		{"* on punctuation only", "a ==\n== ==", "j*", "a ==\n== ==", Pos{1, 3}},
		{"# on punctuation only", "a ==\n== ==", "jw#", "a ==\n== ==", Pos{1, 0}},
		{"# mid-word", "foo x foobar foo", "$h#", "foo x foobar foo", Pos{0, 0}},
		{"2*", "foo x foo y foo z foo", "2*", "foo x foo y foo z foo", Pos{0, 12}},
		{"2#", "foo foo foo foo", "$2#", "foo foo foo foo", Pos{0, 4}},
		{"* on blank uses next word", "x  foo bar foo", "l*", "x  foo bar foo", Pos{0, 11}},
		{"* before a word", "(foo) foo", "*", "(foo) foo", Pos{0, 6}},
		{"* keeps word boundaries", "foo foobar foo", "*", "foo foobar foo", Pos{0, 11}},
		// Inner blocks on lines of their own are linewise.
		{"di{ multiline", "{\n  foo\n}", "jdi{", "{\n}", Pos{1, 0}},
		{"di( multiline", "f(\n  a,\n  b\n)", "jdi(", "f(\n)", Pos{1, 0}},
		{"di{ indented close", "{\n  foo\n  }", "jdi{", "{\n  }", Pos{1, 2}},
		{"di{ text after open", "{ a\n  foo\n}", "jdi{", "{\n}", Pos{0, 0}},
		{"vi{d", "{\n  foo\n}", "jvi{d", "{\n}", Pos{1, 0}},
		{"di( empty", "f()", "f(di(", "f()", Pos{0, 2}},
		{"ci( empty", "f()", "f(ci(x<Esc>", "f(x)", Pos{0, 2}},
		{"d} is linewise", "a\nb\n\nc", "d}", "\nc", Pos{0, 0}},
		// Replace mode <BS> restores what was replaced.
		{"R <BS>", "abcd", "Rxy<BS><BS><Esc>", "abcd", Pos{0, 0}},
		{"R <BS> appended", "ab", "Rxyz<BS><Esc>", "xy", Pos{0, 1}},
		{"R <BS> before start", "abcd", "llRx<BS><BS>q<Esc>", "aqcd", Pos{0, 1}},
		// A motion that cannot move fails the operator.
		{"dj on last line", "a\nb\nc", "Gdj", "a\nb\nc", Pos{2, 0}},
		{"dk on first line", "a\nb\nc", "dk", "a\nb\nc", Pos{0, 0}},
		{"d5j past the end", "a\nb\nc\nd", "jd5j", "a", Pos{0, 0}},
		// Visual changes repeat on the same amount of text.
		{"visual d .", "abcdefghij", "vlld.", "ghij", Pos{0, 0}},
		{"visual line d .", "1\n2\n3\n4\n5", "Vjd.", "5", Pos{0, 0}},
		{"visual multiline d .", "abcd\nefgh\nijkl\nmnop\nqrst", "lvjd.", "akl\nmnop\nqrst", Pos{0, 1}},
		{"visual c .", "abcdefghij", "vlcX<Esc>l.", "XXefghij", Pos{0, 1}},
		{"visual ~ .", "abcdefghij", "vl~l.", "AbCdefghij", Pos{0, 1}},
		// Searching in visual mode extends the selection.
		{"visual /", "foo bar baz qux", "v/baz<CR>d", "az qux", Pos{0, 0}},
		{"visual ?", "foo bar baz qux", "$v?bar<CR>d", "foo ", Pos{0, 3}},
		{"visual / cancelled", "foo bar baz qux", "v/baz<Esc>d", "oo bar baz qux", Pos{0, 0}},
		// aw and a" white space.
		{"daw on white space", "foo   bar baz", "llldaw", "foo baz", Pos{0, 3}},
		{"daw on trailing white space", "foo bar   \nbaz", "$daw", "foo bar", Pos{0, 6}},
		{"daw on trailing white space at end", "foo bar   ", "$daw", "foo bar   ", Pos{0, 9}},
		{"daw without trailing white space", "foo bar.", "wdaw", "foo.", Pos{0, 3}},
		{"daw keeps indent", "  foo", "wdaw", "  ", Pos{0, 1}},
		{"da\" at end of line", `say "hi"`, `fhda"`, "say", Pos{0, 2}},
		// Redo puts the cursor at the start of the change.
		{"redo cursor", "foo bar baz", "wwdw0u$<C-r>", "foo bar ", Pos{0, 7}},
		{"redo insert cursor", "abc def", "wiXY<Esc>0u$<C-r>", "abc XYdef", Pos{0, 4}},
		{"redo line cursor", "a\nb\nc\nd", "jjddggu<C-r>", "a\nb\nd", Pos{2, 0}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := New(c.text, nil)
			e.SetHeight(20)
			feed(e, c.keys)
			if got := e.Text(); got != c.want {
				t.Errorf("text = %q, want %q", got, c.want)
			}
			if e.Cursor() != c.cur {
				t.Errorf("cursor = %+v, want %+v", e.Cursor(), c.cur)
			}
			if e.Mode() != Normal {
				t.Errorf("mode = %v, want NORMAL", e.Mode())
			}
		})
	}
}

func TestRegistersMatchVim(t *testing.T) {
	cases := []struct {
		name, text, keys string
		want             Register
	}{
		{"visual p", "foo bar baz", "yiwwviwp", Register{Text: "bar"}},
		{"x on empty line", "abc\n\nz", "yyjx", Register{Text: "abc", Linewise: true}},
		{"X at column 0", "abc\nqq", "yyjX", Register{Text: "abc", Linewise: true}},
		{"di{ multiline", "{\n  foo\n}", "jdi{", Register{Text: "  foo", Linewise: true}},
		{"di{ text after open", "{ a\n  foo\n}", "jdi{", Register{Text: " a\n  foo"}},
		{"vi{y", "{\n  foo\n}", "jvi{y", Register{Text: "  foo\n"}},
		{"d}", "a\nb\n\nc", "d}", Register{Text: "a\nb", Linewise: true}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			regs := NewRegisters()
			e := New(c.text, regs)
			feed(e, c.keys)
			if got := regs.Get('"'); got != c.want {
				t.Errorf("register = %+v, want %+v", got, c.want)
			}
		})
	}
}

func TestScrollingMatchesVim(t *testing.T) {
	cases := []struct {
		name     string
		lines    int
		height   int
		keys     string
		cur, top int
	}{
		{"zz", 100, 10, "50Gzz", 49, 45},
		{"zz odd height", 100, 11, "50Gzz", 49, 44},
		{"H", 100, 10, "50GzzH", 47, 45},
		{"3H", 100, 10, "50Gzz3H", 47, 45},
		{"5H", 100, 10, "50Gzz5H", 49, 45},
		{"L", 100, 10, "50GzzL", 52, 45},
		{"3L", 100, 10, "50Gzz3L", 52, 45},
		{"L at the top", 100, 10, "L", 7, 0},
		{"H at the bottom", 100, 10, "GH", 92, 90},
		{"L at the bottom", 100, 10, "GHL", 99, 90},
		{"M short buffer", 3, 10, "M", 1, 0},
		{"M four lines", 4, 10, "M", 1, 0},
		{"M", 100, 10, "M", 4, 0},
		{"M odd height", 100, 11, "M", 5, 0},
		{"<C-e>", 100, 10, "<C-e>", 3, 1},
		{"3<C-e>", 100, 10, "3<C-e>", 5, 3},
		{"<C-y> keeps cursor", 100, 10, "<C-e><C-e><C-e><C-e><C-y>", 6, 3},
		{"<C-d>", 100, 10, "<C-d>", 7, 5},
		{"<C-d> keeps offset", 100, 10, "jjjjj<C-d>", 10, 5},
		{"3<C-d>", 100, 10, "3<C-d>", 5, 3},
		{"3<C-d> sets scroll", 100, 10, "3<C-d><C-d>", 8, 6},
		{"<C-u>", 100, 10, "50Gzz<C-u>", 44, 40},
		{"<C-d> at the end", 100, 10, "95G<C-d>", 99, 90},
		{"<C-f>", 100, 10, "<C-f>", 10, 8},
		{"2<C-f>", 100, 10, "2<C-f>", 18, 16},
		{"<C-b>", 100, 10, "50Gzz<C-b>", 44, 37},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			lines := make([]string, c.lines)
			for i := range lines {
				lines[i] = strconv.Itoa(i + 1)
			}
			e := New(strings.Join(lines, "\n"), nil)
			e.SetHeight(c.height)
			feed(e, c.keys)
			if e.Cursor().Line != c.cur || e.Top() != c.top {
				t.Errorf("cursor line %d top %d, want %d %d", e.Cursor().Line, e.Top(), c.cur, c.top)
			}
		})
	}
}

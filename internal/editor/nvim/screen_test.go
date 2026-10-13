package nvim

import (
	"strings"
	"testing"
)

// Without 'termguicolors': the default cterm colors are numbered from 1 (0
// is unset, the terminal's own), and bold and reverse come from the cterm
// attributes. With it: a colorscheme without a background (-1 with
// ext_termcolors) keeps the terminal's.
func TestScreenColors(t *testing.T) {
	s := newScreen(10, 1)
	s.handle(
		[]any{"option_set", []any{"termguicolors", false}},
		[]any{"default_colors_set", []any{int64(0xe0e0e0), int64(0x101010), int64(-1), int64(251), int64(0)}},
		[]any{"hl_attr_define",
			[]any{int64(1), map[string]any{"foreground": int64(0xffcc00)}, map[string]any{"bold": true, "foreground": int64(3)}, []any{}},
			[]any{int64(2), map[string]any{"reverse": true}, map[string]any{"reverse": true}, []any{}}},
	)
	if got := s.sgr(0); got != "\x1b[0;38;5;250m" {
		t.Errorf("default cterm colors: %q", got)
	}
	if got := s.sgr(1); got != "\x1b[0;1;33m" {
		t.Errorf("cterm bold: %q", got)
	}
	if got := s.sgr(2); !strings.HasPrefix(got, "\x1b[0;7;") {
		t.Errorf("cterm reverse: %q", got)
	}
	s.handle(
		[]any{"option_set", []any{"termguicolors", true}},
		[]any{"default_colors_set", []any{int64(0xe0e0e0), int64(-1), int64(-1), int64(0), int64(0)}},
	)
	if got := s.sgr(0); got != "\x1b[0;38;2;224;224;224m" {
		t.Errorf("rgb without a background: %q", got)
	}
	if got := s.sgr(1); got != "\x1b[0;38;2;255;204;0m" {
		t.Errorf("rgb attributes, not cterm ones: %q", got)
	}
}

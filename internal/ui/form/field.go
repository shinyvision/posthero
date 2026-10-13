// Package form is a dialog of fields (text, dropdown, toggle, button) laid
// out in rows, used for logging in (to Postman, to an identity provider).
// Text fields edit with vim keys; Tab moves between fields.
package form

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/shinyvision/posthero/internal/ui/lineinput"
	"github.com/shinyvision/posthero/internal/vim"
)

// Kind is the type of a field.
type Kind int

// Field kinds.
const (
	Text Kind = iota
	Select
	Toggle
	Button
)

// Field is one input.
type Field struct {
	Kind        Kind
	Label       string
	Width       int
	Options     []string // Select: suggestions; free text is allowed too
	Placeholder string
	Description string              // shown under the label while focused
	Aliases     map[string][]string // Select: extra search terms per option
	Action      func(*Form) tea.Cmd // Button; nil submits the form
	Cancel      bool                // Button: closes the form

	on    bool
	input *lineinput.Model
	sel   int  // highlighted dropdown option
	typed bool // filter the dropdown by the text typed since focusing
}

// NewText returns a text field.
func NewText(label, value string, width int) *Field {
	return &Field{Kind: Text, Label: label, Width: width, input: lineinput.New("", value, nil)}
}

// NewSelect returns a dropdown that also accepts typed values.
func NewSelect(label, value string, options []string, width int) *Field {
	f := NewText(label, value, width)
	f.Kind, f.Options = Select, options
	return f
}

// NewToggle returns a yes/no field.
func NewToggle(label string, on bool, width int) *Field {
	return &Field{Kind: Toggle, Label: label, Width: width, on: on}
}

// NewButton returns a button. A nil action submits the form.
func NewButton(label string, action func(*Form) tea.Cmd) *Field {
	return &Field{Kind: Button, Label: label, Width: len([]rune(label)) + 2, Action: action}
}

// NewCancel returns a button that closes the form.
func NewCancel() *Field {
	f := NewButton("Cancel", nil)
	f.Cancel = true
	return f
}

// Value returns the trimmed text of a text or select field.
func (f *Field) Value() string {
	if f.input == nil {
		return ""
	}
	return strings.TrimSpace(f.input.Value())
}

// On returns a toggle's state.
func (f *Field) On() bool { return f.on }

func (f *Field) editable() bool { return f.Kind == Text || f.Kind == Select }

func (f *Field) focus(regs *vim.Registers) {
	if f.editable() {
		f.input.Placeholder = f.Placeholder
		f.input.Insert()
		f.typed = false
		f.sel = 0
		for i, o := range f.Options {
			if o == f.Value() {
				f.sel = i
			}
		}
	}
}

// matches returns the dropdown options for the current text.
func (f *Field) matches() []string {
	if !f.typed || f.Value() == "" {
		return f.Options
	}
	q := strings.ToLower(f.Value())
	var exact, prefix, contains []string
	for _, o := range f.Options {
		best := 3
		for _, term := range append([]string{o}, f.Aliases[o]...) {
			lo := strings.ToLower(term)
			switch {
			case lo == q:
				best = min(best, 0)
			case strings.HasPrefix(lo, q):
				best = min(best, 1)
			case strings.Contains(lo, q):
				best = min(best, 2)
			}
		}
		switch best {
		case 0:
			exact = append(exact, o)
		case 1:
			prefix = append(prefix, o)
		case 2:
			contains = append(contains, o)
		}
	}
	return append(append(exact, prefix...), contains...)
}

// moveOption moves the dropdown highlight.
func (f *Field) moveOption(d int) {
	n := len(f.matches())
	if n == 0 {
		return
	}
	f.sel = (f.sel + d + n) % n
}

// pick takes the highlighted option, reporting whether there was one.
func (f *Field) pick() bool {
	m := f.matches()
	if len(m) == 0 || f.sel >= len(m) {
		return false
	}
	f.input.SetValue(m[f.sel])
	f.input.Insert()
	f.typed = false
	return true
}

// typeKey feeds a key to the text input.
func (f *Field) typeKey(k string) vim.Action {
	before := f.input.Value()
	act := f.input.Key(k)
	if f.input.Value() != before {
		f.typed = true
		f.sel = 0
	}
	return act
}

// SetMask hides what is typed (tokens, passwords).
func (f *Field) SetMask(on bool) {
	if f.input != nil {
		f.input.Mask = on
	}
}

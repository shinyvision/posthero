package vim

import (
	"strings"
	"sync"
	"unicode"
)

// Register is the content of a vim register.
type Register struct {
	Text     string
	Linewise bool
}

// Registers is shared by every engine in the app so that yanking in one
// place and putting in another works like it does in vim.
type Registers struct {
	mu        sync.Mutex
	regs      map[rune]Register
	clipboard *string
}

// NewRegisters returns an empty register file.
func NewRegisters() *Registers {
	return &Registers{regs: map[rune]Register{}}
}

// Get returns a register's content.
func (r *Registers) Get(name rune) Register {
	r.mu.Lock()
	defer r.mu.Unlock()
	if name == '*' {
		name = '+'
	}
	return r.regs[unicode.ToLower(name)]
}

// SetClipboard stores text in the + register and queues it for the system
// clipboard.
func (r *Registers) SetClipboard(text string) {
	r.CopyRegister(Register{Text: text})
}

// CopyRegister is SetClipboard for a register that may be linewise.
func (r *Registers) CopyRegister(reg Register) {
	r.set('+', reg, false)
}

// Yank copies text the way y does in vim and also puts it on the system
// clipboard: the unnamed register and "0 get it too, so p pastes it in any
// editor or grid.
func (r *Registers) Yank(text string) {
	r.store(0, Register{Text: text}, true)
	r.SetClipboard(text)
}

// TakeClipboard returns text that should be copied to the system clipboard,
// if any was produced since the last call.
func (r *Registers) TakeClipboard() (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.clipboard == nil {
		return "", false
	}
	s := *r.clipboard
	r.clipboard = nil
	return s, true
}

// store records yanked or deleted text following vim's rules: the unnamed
// register always gets it, "0 gets yanks, "1-"9 shift on multi-line deletes
// and "- gets small deletes.
func (r *Registers) store(name rune, reg Register, yank bool) {
	if name == '_' {
		return
	}
	if name != 0 && name != '"' {
		r.set(name, reg, true)
		return
	}
	r.set('"', reg, false)
	r.mu.Lock()
	defer r.mu.Unlock()
	switch {
	case yank:
		r.regs['0'] = reg
	case reg.Linewise || strings.Contains(reg.Text, "\n"):
		for i := '9'; i > '1'; i-- {
			r.regs[i] = r.regs[i-1]
		}
		r.regs['1'] = reg
	default:
		r.regs['-'] = reg
	}
}

func (r *Registers) set(name rune, reg Register, alsoUnnamed bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if name == '*' {
		name = '+'
	}
	if unicode.IsUpper(name) {
		lower := unicode.ToLower(name)
		old := r.regs[lower]
		sep := ""
		if old.Linewise || reg.Linewise {
			sep = "\n"
		}
		reg = Register{Text: old.Text + sep + reg.Text, Linewise: old.Linewise || reg.Linewise}
		name = lower
	}
	r.regs[name] = reg
	if alsoUnnamed {
		r.regs['"'] = reg
	}
	if name == '+' {
		t := reg.Text
		if reg.Linewise {
			t += "\n"
		}
		r.clipboard = &t
	}
}

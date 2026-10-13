// Package keys converts Bubble Tea key presses into Vim key notation, the
// lingua franca between posthero's panes, the built-in vim engine and Neovim.
//
// Printable keys are returned as themselves ("a", "<", " "); everything else
// uses angle-bracket notation ("<Esc>", "<C-r>", "<S-Tab>").
package keys

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
)

var special = map[rune]string{
	tea.KeyEnter:     "CR",
	tea.KeyEscape:    "Esc",
	tea.KeyBackspace: "BS",
	tea.KeyTab:       "Tab",
	tea.KeyDelete:    "Del",
	tea.KeyInsert:    "Insert",
	tea.KeyUp:        "Up",
	tea.KeyDown:      "Down",
	tea.KeyLeft:      "Left",
	tea.KeyRight:     "Right",
	tea.KeyHome:      "Home",
	tea.KeyEnd:       "End",
	tea.KeyPgUp:      "PageUp",
	tea.KeyPgDown:    "PageDown",
	tea.KeySpace:     "Space",
	tea.KeyF1:        "F1",
	tea.KeyF2:        "F2",
	tea.KeyF3:        "F3",
	tea.KeyF4:        "F4",
	tea.KeyF5:        "F5",
	tea.KeyF6:        "F6",
	tea.KeyF7:        "F7",
	tea.KeyF8:        "F8",
	tea.KeyF9:        "F9",
	tea.KeyF10:       "F10",
	tea.KeyF11:       "F11",
	tea.KeyF12:       "F12",
}

// keypad maps the keypad's keys, which terminals speaking the kitty
// protocol report apart, to the keys they stand for.
var keypad = map[rune]rune{
	tea.KeyKpEnter:    tea.KeyEnter,
	tea.KeyKpUp:       tea.KeyUp,
	tea.KeyKpDown:     tea.KeyDown,
	tea.KeyKpLeft:     tea.KeyLeft,
	tea.KeyKpRight:    tea.KeyRight,
	tea.KeyKpPgUp:     tea.KeyPgUp,
	tea.KeyKpPgDown:   tea.KeyPgDown,
	tea.KeyKpHome:     tea.KeyHome,
	tea.KeyKpEnd:      tea.KeyEnd,
	tea.KeyKpInsert:   tea.KeyInsert,
	tea.KeyKpDelete:   tea.KeyDelete,
	tea.KeyKpEqual:    '=',
	tea.KeyKpMultiply: '*',
	tea.KeyKpPlus:     '+',
	tea.KeyKpComma:    ',',
	tea.KeyKpMinus:    '-',
	tea.KeyKpDecimal:  '.',
	tea.KeyKpDivide:   '/',
	tea.KeyKpSep:      ',',
	tea.KeyKp0:        '0',
	tea.KeyKp1:        '1',
	tea.KeyKp2:        '2',
	tea.KeyKp3:        '3',
	tea.KeyKp4:        '4',
	tea.KeyKp5:        '5',
	tea.KeyKp6:        '6',
	tea.KeyKp7:        '7',
	tea.KeyKp8:        '8',
	tea.KeyKp9:        '9',
}

// Vim returns the Vim notation for a key press, or "" for a key that has
// none (media keys, a lone modifier, the keypad's 5 without num lock):
// such keys are best ignored.
func Vim(k tea.KeyPressMsg) string {
	mod := k.Mod &^ (tea.ModCapsLock | tea.ModNumLock | tea.ModScrollLock)
	if k.Text != "" && mod&(tea.ModCtrl|tea.ModAlt|tea.ModMeta|tea.ModSuper) == 0 {
		return k.Text
	}
	code := k.Code
	if c, ok := keypad[code]; ok {
		code = c
	}
	name, isSpecial := special[code]
	if code >= tea.KeyF13 && code <= tea.KeyF63 {
		name, isSpecial = "F"+strconv.Itoa(int(code-tea.KeyF13)+13), true
	}
	if code == tea.KeySpace && mod == 0 {
		return " "
	}
	if !isSpecial && (code > unicode.MaxRune || !utf8.ValidRune(code) || unicode.Is(unicode.Co, code)) {
		return "" // no character and no name, e.g. a media key
	}
	if !isSpecial {
		// Shift is implied by the character itself for printable keys, but
		// is explicit for ctrl/alt combinations: <C-S-j> differs from <C-j>.
		if mod&(tea.ModCtrl|tea.ModAlt|tea.ModMeta) == 0 {
			mod &^= tea.ModShift
			name = string(code)
		} else {
			if unicode.IsUpper(code) {
				mod |= tea.ModShift
			}
			name = string(unicode.ToLower(code))
		}
	}
	var b strings.Builder
	b.WriteByte('<')
	if mod&tea.ModCtrl != 0 {
		b.WriteString("C-")
	}
	if mod&(tea.ModAlt|tea.ModMeta) != 0 {
		b.WriteString("M-")
	}
	if mod&tea.ModShift != 0 {
		b.WriteString("S-")
	}
	if mod&tea.ModSuper != 0 {
		b.WriteString("D-")
	}
	if mod == 0 && !isSpecial {
		return name
	}
	b.WriteString(name)
	b.WriteByte('>')
	return b.String()
}

// Nvim escapes a key from Vim for nvim_input, where a literal "<" must be
// written as "<lt>".
func Nvim(key string) string {
	if key == "<" {
		return "<lt>"
	}
	return key
}

// IsPrintable reports whether a Vim notation key is a literal character.
func IsPrintable(key string) bool {
	return key != "" && (len([]rune(key)) == 1 || !strings.HasPrefix(key, "<") || key == "<")
}

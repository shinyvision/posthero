package keys

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestVim(t *testing.T) {
	cases := []struct {
		k    tea.KeyPressMsg
		want string
	}{
		{tea.KeyPressMsg{Code: 'a', Text: "a"}, "a"},
		{tea.KeyPressMsg{Code: 'a', Text: "A", Mod: tea.ModShift}, "A"},
		{tea.KeyPressMsg{Code: 'h', Mod: tea.ModCtrl}, "<C-h>"},
		{tea.KeyPressMsg{Code: 'h', Mod: tea.ModCtrl | tea.ModShift}, "<C-S-h>"},
		{tea.KeyPressMsg{Code: 'H', Mod: tea.ModCtrl}, "<C-S-h>"},
		{tea.KeyPressMsg{Code: tea.KeyEnter}, "<CR>"},
		{tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}, "<S-Tab>"},
		{tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}, " "},
		{tea.KeyPressMsg{Code: 'x', Mod: tea.ModAlt}, "<M-x>"},
	}
	for _, c := range cases {
		if got := Vim(c.k); got != c.want {
			t.Errorf("Vim(%+v) = %q, want %q", c.k, got, c.want)
		}
	}
}

// Terminals using the kitty protocol send the keypad and F13 and up as
// keys of their own; they must not turn into replacement characters.
func TestKeypadAndExtendedKeys(t *testing.T) {
	cases := []struct {
		k    tea.KeyPressMsg
		want string
	}{
		{tea.KeyPressMsg{Code: tea.KeyKpEnter}, "<CR>"},
		{tea.KeyPressMsg{Code: tea.KeyKpEnter, Mod: tea.ModShift}, "<S-CR>"},
		{tea.KeyPressMsg{Code: tea.KeyKpEnter, Mod: tea.ModNumLock}, "<CR>"},
		{tea.KeyPressMsg{Code: tea.KeyKpUp}, "<Up>"},
		{tea.KeyPressMsg{Code: tea.KeyKpDown}, "<Down>"},
		{tea.KeyPressMsg{Code: tea.KeyKpLeft}, "<Left>"},
		{tea.KeyPressMsg{Code: tea.KeyKpRight, Mod: tea.ModCtrl}, "<C-Right>"},
		{tea.KeyPressMsg{Code: tea.KeyKpPgUp}, "<PageUp>"},
		{tea.KeyPressMsg{Code: tea.KeyKpPgDown}, "<PageDown>"},
		{tea.KeyPressMsg{Code: tea.KeyKpHome}, "<Home>"},
		{tea.KeyPressMsg{Code: tea.KeyKpEnd}, "<End>"},
		{tea.KeyPressMsg{Code: tea.KeyKpInsert}, "<Insert>"},
		{tea.KeyPressMsg{Code: tea.KeyKpDelete}, "<Del>"},
		{tea.KeyPressMsg{Code: tea.KeyKp5, Text: "5"}, "5"},
		{tea.KeyPressMsg{Code: tea.KeyKp5}, "5"},
		{tea.KeyPressMsg{Code: tea.KeyKp5, Mod: tea.ModCtrl}, "<C-5>"},
		{tea.KeyPressMsg{Code: tea.KeyKpMinus}, "-"},
		{tea.KeyPressMsg{Code: tea.KeyKpPlus, Mod: tea.ModCtrl}, "<C-+>"},
		{tea.KeyPressMsg{Code: tea.KeyKpDivide}, "/"},
		{tea.KeyPressMsg{Code: tea.KeyKpMultiply}, "*"},
		{tea.KeyPressMsg{Code: tea.KeyKpDecimal}, "."},
		{tea.KeyPressMsg{Code: tea.KeyKpEqual}, "="},
		{tea.KeyPressMsg{Code: tea.KeyKpComma}, ","},
		{tea.KeyPressMsg{Code: tea.KeyF13}, "<F13>"},
		{tea.KeyPressMsg{Code: tea.KeyF24, Mod: tea.ModShift}, "<S-F24>"},
		{tea.KeyPressMsg{Code: tea.KeyF63}, "<F63>"},
		{tea.KeyPressMsg{Code: tea.KeyKpBegin}, ""},
		{tea.KeyPressMsg{Code: tea.KeyMediaPlay}, ""},
		{tea.KeyPressMsg{Code: tea.KeyCapsLock}, ""},
		{tea.KeyPressMsg{Code: tea.KeyLeftShift, Mod: tea.ModShift}, ""},
		{tea.KeyPressMsg{Code: 0xE000, Mod: tea.ModCtrl}, ""},
	}
	for _, c := range cases {
		if got := Vim(c.k); got != c.want {
			t.Errorf("Vim(%+v) = %q, want %q", c.k, got, c.want)
		}
	}
}

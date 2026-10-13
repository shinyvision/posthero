// Package theme holds posthero's styles. Every color is one of the terminal's
// 16 ANSI colors, so posthero automatically follows the user's terminal theme.
package theme

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// Palette entries, by ANSI index.
var (
	Black   = lipgloss.Color("0")
	Accent  = lipgloss.Color("4")
	Muted   = lipgloss.Color("8")
	Red     = lipgloss.Color("1")
	Green   = lipgloss.Color("2")
	Yellow  = lipgloss.Color("3")
	Magenta = lipgloss.Color("5")
	Cyan    = lipgloss.Color("6")
)

// Text styles.
var (
	Plain      = lipgloss.NewStyle()
	Dim        = lipgloss.NewStyle().Foreground(Muted)
	Bold       = lipgloss.NewStyle().Bold(true)
	Italic     = lipgloss.NewStyle().Italic(true)
	Error      = lipgloss.NewStyle().Foreground(Red)
	Warn       = lipgloss.NewStyle().Foreground(Yellow)
	Success    = lipgloss.NewStyle().Foreground(Green)
	Selected   = lipgloss.NewStyle().Reverse(true)
	Strike     = lipgloss.NewStyle().Foreground(Muted).Strikethrough(true)
	AccentFg   = lipgloss.NewStyle().Foreground(Accent)
	MagentaFg  = lipgloss.NewStyle().Foreground(Magenta)
	CyanFg     = lipgloss.NewStyle().Foreground(Cyan)
	AccentPill = lipgloss.NewStyle().Background(Accent).Foreground(Black).Bold(true).Padding(0, 1)
	MutedPill  = lipgloss.NewStyle().Background(Muted).Foreground(Black).Padding(0, 1)
	WarnPill   = lipgloss.NewStyle().Background(Yellow).Foreground(Black).Bold(true).Padding(0, 1)
	ErrorPill  = lipgloss.NewStyle().Background(Red).Foreground(Black).Bold(true).Padding(0, 1)
)

// Document styles, for descriptions.
var (
	Heading  = lipgloss.NewStyle().Foreground(Accent).Bold(true)
	Heading3 = lipgloss.NewStyle().Bold(true)
	Code     = lipgloss.NewStyle().Foreground(Cyan)
	Link     = lipgloss.NewStyle().Foreground(Accent).Underline(true)
	Quote    = lipgloss.NewStyle().Foreground(Muted)
	Rule     = lipgloss.NewStyle().Foreground(Muted)
)

// Syntax styles, for JSON and the like.
var (
	Key     = lipgloss.NewStyle().Foreground(Accent)
	String  = lipgloss.NewStyle().Foreground(Green)
	Number  = lipgloss.NewStyle().Foreground(Yellow)
	Literal = lipgloss.NewStyle().Foreground(Magenta)
	Punct   = lipgloss.NewStyle().Foreground(Muted)
)

// Status colors an HTTP status code: 2xx green, 3xx blue, 4xx yellow, 5xx
// red.
func Status(code int) lipgloss.Style {
	switch {
	case code >= 500:
		return Error
	case code >= 400:
		return Warn
	case code >= 300:
		return AccentFg
	case code >= 200:
		return Success
	}
	return Dim
}

// StatusPill is a status code drawn as a pill.
func StatusPill(code int) lipgloss.Style {
	c := Muted
	switch {
	case code >= 500:
		c = Red
	case code >= 400:
		c = Yellow
	case code >= 300:
		c = Accent
	case code >= 200:
		c = Green
	}
	return lipgloss.NewStyle().Background(c).Foreground(Black).Bold(true).Padding(0, 1)
}

// EnvPill is the environment drawn as a pill: red for production, so it is
// never missed.
func EnvPill(prod bool) lipgloss.Style {
	if prod {
		return ErrorPill
	}
	return AccentPill
}

// Box draws a rounded box of exactly w×h cells around lines, with a title in
// the top border and an optional footer in the bottom border. The border is
// drawn in the accent color when focused.
func Box(title, footer string, lines []string, w, h int, focused bool) string {
	if w < 4 || h < 2 {
		return ""
	}
	border := Dim
	titleStyle := Dim
	if focused {
		border = AccentFg
		titleStyle = AccentFg.Bold(true)
	}
	inner := w - 2
	var b strings.Builder
	b.WriteString(edge("╭", "╮", title, titleStyle, border, inner))
	for i := 0; i < h-2; i++ {
		line := ""
		if i < len(lines) {
			line = lines[i]
		}
		b.WriteByte('\n')
		b.WriteString(border.Render("│"))
		b.WriteString(Fit(line, inner))
		b.WriteString(border.Render("│"))
	}
	b.WriteByte('\n')
	b.WriteString(edge("╰", "╯", footer, Dim, border, inner))
	return b.String()
}

func edge(left, right, label string, labelStyle, border lipgloss.Style, inner int) string {
	if label == "" || inner < 6 {
		return border.Render(left + strings.Repeat("─", inner) + right)
	}
	label = ansi.Truncate(label, inner-4, "…")
	fill := inner - ansi.StringWidth(label) - 3
	return border.Render(left+"─ ") + labelStyle.Render(label) + border.Render(" "+strings.Repeat("─", fill)+right)
}

// Fit truncates or pads a styled string to exactly w cells.
func Fit(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if ansi.StringWidth(s) > w {
		s = ansi.Truncate(s, w, "")
	}
	if pad := w - ansi.StringWidth(s); pad > 0 {
		s += "\x1b[0m" + strings.Repeat(" ", pad)
	}
	return s
}

// Truncate shortens plain text to w cells with an ellipsis.
func Truncate(s string, w int) string {
	if w <= 0 {
		return ""
	}
	return ansi.Truncate(s, w, "…")
}

// Width is the display width of a styled string in cells.
func Width(s string) int { return ansi.StringWidth(s) }

// Hyperlink wraps styled text in an OSC 8 hyperlink, which terminals that
// support it make clickable.
func Hyperlink(text, url string) string {
	if url == "" {
		return text
	}
	return ansi.SetHyperlink(url) + text + ansi.ResetHyperlink()
}

// Strip removes styling from a string.
func Strip(s string) string { return ansi.Strip(s) }

// The marks of something that opens and closes, as in ql: Nerd Font
// chevrons, each followed by a space.
const (
	Expanded  = "\uf47c "
	Collapsed = "\uf460 "
)

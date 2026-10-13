package app

import (
	"strings"
	"time"
)

// logEntry is a status message, kept for :messages.
type logEntry struct {
	at      time.Time
	text    string
	isError bool
}

// maxLog is how many status messages :messages keeps.
const maxLog = 200

// setStatus shows a message in the status bar, and keeps it for
// :messages.
func (m *Model) setStatus(text string, isError bool) {
	text = strings.TrimSpace(strings.ReplaceAll(text, "\n", " "))
	// What an undo reports when it is done is what it took back. One that
	// failed says so, and that stays.
	if m.undoNote != "" && !strings.HasPrefix(text, "undoing: ") {
		if !isError {
			text = m.undoNote
		}
		m.undoNote = ""
	}
	m.status, m.isError = text, isError
	if text == "" {
		return
	}
	if n := len(m.log); n > 0 && m.log[n-1].text == text {
		m.log[n-1].at = time.Now()
		return
	}
	m.log = append(m.log, logEntry{at: time.Now(), text: text, isError: isError})
	if len(m.log) > maxLog {
		m.log = m.log[len(m.log)-maxLog:]
	}
}

package vimpty

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net"
	"strings"
	"sync"

	"github.com/shinyvision/posthero/internal/editor"
)

// channel is the JSON channel Vim opens to posthero (see vimrcChannel): Vim
// asks for completions and, in the request pane, reports sends, changes,
// writes, K and gd and ctrl-w window commands; posthero calls Vim functions
// to switch files and move the cursor.
type channel struct {
	ln       net.Listener
	port     int
	token    string // only Vim knows it (its script is readable only by the user)
	complete editor.Completer
	events   chan editor.Event

	mu      sync.Mutex
	conn    net.Conn
	pending [][]any // calls made before Vim connected
}

// listen waits for Vim on a local port.
func listen(complete editor.Completer, events chan editor.Event) (*channel, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		ln.Close()
		return nil, err
	}
	c := &channel{ln: ln, port: ln.Addr().(*net.TCPAddr).Port, token: hex.EncodeToString(buf), complete: complete, events: events}
	go c.accept()
	return c, nil
}

func (c *channel) accept() {
	conn, err := c.ln.Accept()
	_ = c.ln.Close() // one Vim, one connection; requests carry the token
	if err != nil {
		return
	}
	c.mu.Lock()
	c.conn = conn
	pending := c.pending
	c.pending = nil
	c.mu.Unlock()
	for _, msg := range pending {
		c.send(msg)
	}
	if len(pending) > 0 {
		c.send([]any{"redraw", ""})
	}
	c.read(conn)
}

// read answers Vim's requests: [id, [name, token, args…]].
func (c *channel) read(conn net.Conn) {
	dec := json.NewDecoder(conn)
	for {
		var msg []json.RawMessage
		if err := dec.Decode(&msg); err != nil {
			return
		}
		if len(msg) != 2 {
			continue
		}
		var id int
		var req []json.RawMessage
		if json.Unmarshal(msg[0], &id) != nil || json.Unmarshal(msg[1], &req) != nil || len(req) < 2 {
			continue
		}
		var name, token string
		_ = json.Unmarshal(req[0], &name)
		if json.Unmarshal(req[1], &token) != nil || token != c.token {
			_ = conn.Close() // not our Vim
			return
		}
		args := req[2:]
		str := func(i int) string {
			var s string
			if i < len(args) {
				_ = json.Unmarshal(args[i], &s)
			}
			return s
		}
		num := func(i int) int {
			var n int
			if i < len(args) {
				_ = json.Unmarshal(args[i], &n)
			}
			return n
		}
		lines := func(i int) []string {
			var l []string
			if i < len(args) {
				_ = json.Unmarshal(args[i], &l)
			}
			return l
		}
		switch name {
		case "complete":
			c.send([]any{id, editor.OmniResult(c.complete, lines(0), num(1), num(2))})
		case "send":
			editor.Notify(c.events, editor.Send{File: str(0), Lines: lines(1), Line: num(2), Col: num(3)})
		case "hover":
			editor.Notify(c.events, editor.Hover{File: str(0), Lines: lines(1), Line: num(2), Col: num(3), Key: str(4)})
		case "write":
			editor.Notify(c.events, editor.Written{File: str(0), Text: strings.Join(lines(1), "\n")})
		case "changed":
			editor.Notify(c.events, editor.Changed{File: str(0), Text: str(1), Modified: num(2) != 0})
		case "win":
			editor.Notify(c.events, editor.WinCmd{Key: str(0)})
		}
	}
}

// call calls a Vim function; before Vim connects, the call waits. Vim does
// not update the screen after a channel command until it is told to (newer
// versions do anyway), so a redraw follows.
func (c *channel) call(fn string, args ...any) {
	if args == nil {
		args = []any{}
	}
	msg := []any{"call", fn, args}
	c.mu.Lock()
	if c.conn == nil {
		c.pending = append(c.pending, msg)
		c.mu.Unlock()
		return
	}
	c.mu.Unlock()
	c.send(msg)
	c.send([]any{"redraw", ""})
}

func (c *channel) send(msg any) bool {
	data, err := json.Marshal(msg)
	if err != nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil {
		return false
	}
	_, err = c.conn.Write(data)
	return err == nil
}

func (c *channel) close() {
	_ = c.ln.Close()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil {
		_ = c.conn.Close()
	}
}

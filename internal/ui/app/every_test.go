package app

import (
	"strings"
	"testing"
)

// writes counts the calls that change something in (the fake) Postman.
func (d *driver) writes() int {
	n := 0
	for k, v := range d.demo.Postman.Calls {
		if !strings.HasPrefix(k, "GET ") {
			n += v
		}
	}
	return n
}

// closeAll closes whatever an action opened: dialogs, editors, forms, the
// command line, half-typed keys.
func (d *driver) closeAll() {
	d.t.Helper()
	for range 10 {
		switch {
		case d.m.edit != nil:
			d.keys("<Esc>")
			d.typeText(":q!")
			d.keys("<CR>")
		case len(d.m.modals) > 0, d.m.form != nil, d.m.cmdline != nil, d.m.keys.pending != "":
			d.keys("<Esc>")
		default:
			return
		}
	}
	d.t.Fatalf("couldn't close what was open:\n%s", d.screen())
}

// Every key binding, in every pane it works in, does its thing without
// panicking or getting stuck, and changes nothing in Postman unless it is
// a change that was asked for and confirmed.
func TestEveryBinding(t *testing.T) {
	d := newDriver(t)
	d.open("list products")
	d.sendOpen()
	d.waitFor("200 OK")
	d.closeAll()
	// What may write: putting (after dd/yy) and undoing that.
	mayWrite := map[string]bool{"put": true, "putabove": true, "undo": true}
	// What leaves posthero: not here.
	skip := map[string]bool{"quit": true}
	panes := []struct {
		name string
		p    pane
		ctx  where
	}{{"tree", paneTree, inTree}, {"response", paneResp, inResp}, {"editor", paneEditor, inEditor}}
	for _, pn := range panes {
		for _, b := range bindings {
			if b.where&pn.ctx == 0 || skip[b.action] {
				continue
			}
			d.selectItem("List products")
			d.m.focusPane(pn.p)
			before := d.writes()
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("%s in the %s (%s) panicked: %v", b.keys, pn.name, b.action, r)
					}
				}()
				d.m.do(b.action, 0)
				d.settle()
				d.screen()
			}()
			d.closeAll()
			if d.writes() != before && !mayWrite[b.action] {
				t.Errorf("%s in the %s (%s) changed something in Postman", b.keys, pn.name, b.action)
			}
		}
	}
}

// Every command, typed without arguments (and with a harmless one where it
// needs one), answers without panicking, and changes nothing in Postman
// without being confirmed.
func TestEveryCommand(t *testing.T) {
	d := newDriver(t)
	d.open("list products")
	d.sendOpen()
	d.waitFor("200 OK")
	d.closeAll()
	skip := map[string]bool{"quit": true, "qall": true, "xit": true, "wq": true}
	args := map[string][]string{
		"edit": {"", "get product"}, "find": {"", "orders"}, "run": {"", "-n 1"}, "env": {"", "Shop · Production", "none"},
		"set": {"", "all", "wrap", "nowrap", "refresh=300"}, "jq": {"", ".[0].name"}, "code": {"", "python"},
		"history": {"", "clear"}, "pull": {""}, "push": {""}, "workspace": {""}, "vars": {""}, "share": {""}, "reset": {"pageSize"},
	}
	// Changes asked for without a question: like yy then p, and u; :example
	// makes an example (a blank one, with no response shown).
	mayWrite := map[string]bool{"write": true, "run": true, "duplicate": true, "undo": true, "example": true}
	for _, c := range commands {
		if skip[c.name] {
			continue
		}
		list := args[c.name]
		if list == nil {
			list = []string{""}
		}
		for _, arg := range list {
			d.selectItem("List products")
			before := d.writes()
			line := ":" + c.name
			if arg != "" {
				line += " " + arg
			}
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("%s panicked: %v", line, r)
					}
				}()
				d.m.focusPane(paneTree)
				d.keys(":")
				d.typeText(strings.TrimPrefix(line, ":"))
				d.keys("<CR>")
				d.settle()
				d.screen()
			}()
			d.closeAll()
			if d.writes() != before && !mayWrite[c.name] {
				t.Errorf("%s changed something in Postman", line)
			}
		}
	}
	// The app still works.
	d.keys(":env Shop · Local<CR>")
	d.open("get product")
	d.sendOpen()
	d.waitFor("200 OK")
}

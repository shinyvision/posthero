package app

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/shinyvision/posthero/internal/config"
	"github.com/shinyvision/posthero/internal/ui/modal"
	"github.com/shinyvision/posthero/internal/ui/theme"
)

// helpIntro is the help's first section: how posthero works.
const helpIntro = `posthero is Postman for the terminal. On the left are your team's
collections, as they are in Postman. On the right is the request, in your
own editor, and below it what came back.

  ⏎ in the tree opens a request; ⏎ in the editor (normal mode) sends it.
  :w saves the request to Postman, for your whole team. Changes you don't
  save stay here as a draft (a + in the tree), until you :w or :e! them.
  :q quits posthero, from the request too; :wq (:x, ZZ) saves it first.
  ctrl+p finds any request. E picks the environment. ? is this help.

Logging in happens by itself: posthero logs in to your identity provider
(Keycloak and friends) the way the request's auth says, refreshes the token
before it runs out, and logs in again when the session is over, filling in
the login page with the password you gave it once (kept in your system
keyring). Only a one-time code or a JavaScript-only login page needs you.`

// docHelp explains the request document.
const docHelp = `A request reads like an .http file. Only what is set is shown.

  POST {{base}}/users/:team          the method and URL
      ?notify=true                   query parameters (or on the URL line)
      # &dry=1                       a # switches a line off
      :team = 7                      path variables
  Content-Type: application/json     headers
  # X-Debug: 1                       a header switched off

  {"name": "Ada"}                    the body, after a blank line

  ### variables                      sections, as needed:
  ### auth bearer                    the request's own auth (none: inherit)
  ### pre-request                    scripts (pm.*, as in Postman)
  ### tests
  ### settings                       followRedirects: false, …
  ### description

A body starting with query or mutation is GraphQL (### variables holds its
variables); { or [ is JSON. Other bodies: ### xml, ### text, ### form,
### multipart (key: @file), ### file, ### graphql. A body switched off
shows as ### json off (take off away to switch it on). A name or value
with unusual characters (spaces at the ends, a colon) is in quotes.

In the editor, K on a {{variable}} says what it is and where it comes from;
gd edits it. Typing {{ offers the variables. The dim notes after a line are
the variables' values (:set noshowvalues hides them).

In a GraphQL query, ctrl+x ctrl+o completes fields, arguments and enum
values from the server's schema, and K says what a field is. posthero asks
the server for its schema by itself once you're logged in (on production
only after you sent a request there); :schema asks again.`

// exampleHelp explains saved examples.
const exampleHelp = `A request's saved examples fold under it in the tree: l opens them (l
again opens the request), o, Tab and za open and close them, h closes
them, and ge lists them. One opens like a request does: the request it
was saved with, then what came back.

  GET {{base}}/products/p-1001       the request it was saved with
  Accept: application/json

  ### response 200 OK                the status: a code, and its text
  Content-Type: application/json     the response's headers

  {"id": "p-1001"}                   the body, after a blank line

JSON on one line shows spread out; it stays as it was unless you change it.
:w saves the example, ⏎ sends its request (with the request's auth and
scripts), and :example! then keeps what came back in it. :example NAME
saves the response shown as a new example (with none shown, it makes a
blank one to fill in). In the tree, r renames an example, D deletes it,
yy or dd then p on a request copies or moves it there, :dup copies it, and
u takes any of it back.`

// localHelp explains local mode.
const localHelp = `:set local works on a copy of the workspace on this machine: saves,
moves, renames and environments change the copy, not Postman, until you
:push. A ↑ in the tree marks what changed. :pull takes in what your team
changed. :push asks about each thing your team changed too: overwrite it,
show the diff, or skip it. The diffs open afterwards, a tab each:
  tab S-tab      the next and previous diff
  c  :e          merge the two in your editor (git's conflict markers)
  :w             push yours (or the merged text)
  :q  q          leave it here, not pushed (q asks first)
  :qq Q          leave them all (Q asks first)
:set nolocal goes back to Postman. Each workspace remembers its mode.`

// showHelp opens the help.
func (m *Model) showHelp() tea.Cmd {
	render := func(width int) ([]string, []int) {
		var lines []string
		var sections []int
		heading := func(title string) {
			if len(lines) > 0 {
				lines = append(lines, "")
			}
			sections = append(sections, len(lines))
			lines = append(lines, theme.Heading.Render(title))
		}
		heading("posthero " + orName(m.opts.Version, ""))
		lines = append(lines, strings.Split(helpIntro, "\n")...)
		heading("The request")
		lines = append(lines, strings.Split(docHelp, "\n")...)
		heading("Examples")
		lines = append(lines, strings.Split(exampleHelp, "\n")...)
		var order []string
		bySection := map[string][]binding{}
		for _, b := range bindings {
			if b.help == "" {
				continue
			}
			if _, ok := bySection[b.section]; !ok {
				order = append(order, b.section)
			}
			bySection[b.section] = append(bySection[b.section], b)
		}
		for _, sec := range order {
			heading(sec)
			for _, b := range bySection[sec] {
				keys := b.keys
				for _, o := range bindings {
					if o.action == b.action && o.keys != b.keys && o.help == "" && o.section == b.section {
						keys += " " + o.keys
					}
				}
				lines = append(lines, fmt.Sprintf("  %-14s %s", displayKeys(keys), b.help))
			}
		}
		heading("In the tree")
		lines = append(lines, "  j k gg G ctrl+d ctrl+u   move;  za zo zc zM zR  fold",
			"  h l            close, open; l on a request opens its examples, l again the request",
			"  o Tab          open or close a folder, or a request's examples",
			"  /              narrow the tree as you type (fuzzy: names, folders, methods, URLs, examples)",
			"  ⏎              open the request or example (in the editor); on a folder: fold it")
		heading("In the response")
		lines = append(lines, "  j k gg G ctrl+d ctrl+u H M L   move",
			"  za zo zc zM zR tab   fold the JSON (and the tests, console, headers above it)",
			"  ]] [[          the next and previous part: tests, console, headers, sent, body",
			"  / ? n N *      search (regexp, smart case)",
			"  |              filter the body with jq (esc shows it again)",
			"  yy yv yp       copy the line, the value, its jq path;  Y the body;  yh the headers",
			"  K              the value in full (a JWT decoded)",
			"  gx             open the link on the line;  gw  wrap long lines")
		heading("Users (gu, :auth)")
		lines = append(lines, strings.Split(usersHelp, "\n")...)
		heading("Local mode")
		lines = append(lines, strings.Split(localHelp, "\n")...)
		heading("Commands")
		for _, c := range commands {
			name := ":" + c.name
			if c.short != "" && c.short != c.name {
				name = ":" + c.short + "[" + strings.TrimPrefix(c.name, c.short) + "]"
			}
			if c.args != "" {
				name += " " + c.args
			}
			lines = append(lines, fmt.Sprintf("  %-34s %s", name, c.help))
		}
		heading("Options (:set)")
		for _, row := range m.settings.Table() {
			lines = append(lines, fmt.Sprintf("  %-16s %-10s %s", row[0], row[1], row[2]))
		}
		heading("Files")
		cfg, _ := config.ConfigDir()
		st, _ := config.StateDir()
		cache, _ := config.CacheDir()
		lines = append(lines,
			"  "+cfg+"/postherorc     options and mappings (:mkpostherorc writes it)",
			"  "+cfg+"/login.json     the Postman login (in your keyring)",
			"  "+st+"/                drafts, your values, tokens, cookies, history",
			"  "+cache+"/             the workspaces as last fetched")
		var wrapped []string
		var secs []int
		si := 0
		for i, l := range lines {
			if si < len(sections) && sections[si] == i {
				secs = append(secs, len(wrapped))
				si++
			}
			wrapped = append(wrapped, theme.Truncate(l, width))
		}
		return wrapped, secs
	}
	return modal.Show(&modal.Pager{Title: "Help · / searches · ]] [[ jump · q closes", Render: render, Wide: true})
}

// displayKeys writes keys for people: <C-p> as ctrl+p, a space as space.
func displayKeys(k string) string {
	r := strings.NewReplacer("<C-S-", "ctrl+shift+", "<C-", "ctrl+", "<CR>", "⏎", "<Esc>", "esc", "<BS>", "backspace", "<F1>", "F1", ">", "", " ", "space")
	if k == " " {
		return "space"
	}
	return r.Replace(k)
}

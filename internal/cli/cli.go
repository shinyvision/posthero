// Package cli is posthero's commands for scripts (and agents): each one
// does one thing in a workspace, reads text from a file or stdin, prints
// plain text and exits, without the screen.
//
// Requests and folders go by path: the collection, the folders and the
// item, joined with "/", like "Shop/Orders/Get order". Each part is a name
// or an id. A name with a "/" in it is written with "\/".
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/shinyvision/posthero/internal/postman"
	"github.com/shinyvision/posthero/internal/reqdoc"
	"github.com/shinyvision/posthero/internal/store"
)

// Usage describes the commands, for posthero's help.
const Usage = `Commands (for scripts; each prints plain text and exits):
  posthero workspaces            list the workspaces
  posthero ls [PATH]             list the collections, or everything in a
                                 collection or folder, with ids
  posthero cat PATH              print a request as text, as the editor
                                 shows it
  posthero add PATH [FILE]       make the request PATH from text (FILE, or
                                 stdin when FILE is - or missing)
  posthero save PATH [FILE]      save text to the request PATH; with
                                 --since TIME, only when nobody changed it
                                 after TIME (from ls -l)
  posthero mkdir PATH            make the folder PATH
  posthero send PATH [FILE]      send the request PATH as posthero does
                                 (scripts, logins, variables); with FILE
                                 (or -), send that text instead. -e ENV
                                 picks the environment, --var KEY=VALUE
                                 sets a variable for this send, -o FILE
                                 writes the body there. Failing tests make
                                 it fail.
  posthero env [cat NAME]        list the environments, or show one's
                                 variables (secrets masked)
  posthero env set NAME KEY VALUE
                                 change a variable's shared value in an
                                 environment (adding it when it isn't
                                 there); --since TIME as for save

  PATH is the collection, the folders and the item, joined with "/", like
  "Shop/Orders/Get order". Each part is a name or an id; write a "/" in a
  name as "\/". Commands take -w NAME for the workspace, as posthero does.
`

// Env is what the commands work with.
type Env struct {
	API postman.API
	// Workspace is the workspace's name or id; Last is the one posthero
	// opened last (its id), used when Workspace is empty.
	Workspace, Last string
	// Local says whether a workspace (by id) is in local mode.
	Local func(id string) bool
	In    io.Reader
	Out   io.Writer
	// Err hears what goes wrong on the side; nil is stderr.
	Err io.Writer
	// Sending is what send needs; without it, send isn't there.
	Sending *Sending
}

func (e *Env) errOut() io.Writer {
	if e.Err == nil {
		return os.Stderr
	}
	return e.Err
}

var commands = map[string]func(ctx context.Context, e *Env, args []string) error{
	"workspaces": workspaces,
	"ls":         ls,
	"cat":        cat,
	"add":        add,
	"save":       save,
	"mkdir":      mkdir,
	"send":       sendRequest,
	"env":        env,
}

// IsCommand says whether name is one of the commands.
func IsCommand(name string) bool {
	_, ok := commands[name]
	return ok
}

// Run runs a command: args[0] is its name.
func Run(ctx context.Context, e *Env, args []string) error {
	run, ok := commands[args[0]]
	if !ok {
		return fmt.Errorf("%s: no such command", args[0])
	}
	return run(ctx, e, args[1:])
}

// flags parses a command's flags, which may come after its arguments too.
func (e *Env) flags(name string, args []string, define func(*flag.FlagSet)) ([]string, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&e.Workspace, "w", e.Workspace, "")
	fs.StringVar(&e.Workspace, "workspace", e.Workspace, "")
	if define != nil {
		define(fs)
	}
	var rest []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		args = fs.Args()
		if len(args) == 0 {
			return rest, nil
		}
		rest = append(rest, args[0])
		args = args[1:]
	}
}

func workspaces(ctx context.Context, e *Env, args []string) error {
	if _, err := e.flags("workspaces", args, nil); err != nil {
		return err
	}
	list, err := e.API.Workspaces(ctx)
	if err != nil {
		return err
	}
	for _, ws := range list {
		fmt.Fprintf(e.Out, "%s\t%s\n", ws.ID, ws.Name)
	}
	return nil
}

func ls(ctx context.Context, e *Env, args []string) error {
	long := false
	args, err := e.flags("ls", args, func(fs *flag.FlagSet) { fs.BoolVar(&long, "l", false, "") })
	if err != nil {
		return err
	}
	if len(args) > 1 {
		return errors.New("ls: give one path at most")
	}
	ws, err := e.workspace(ctx)
	if err != nil {
		return err
	}
	if len(args) == 0 {
		cols, err := e.API.Collections(ctx, ws.ID)
		if err != nil {
			return err
		}
		for _, c := range cols {
			fmt.Fprintf(e.Out, "%s\t%s\n", c.Name, c.UID)
		}
		return nil
	}
	f, err := e.find(ctx, ws, args[0])
	if err != nil {
		return err
	}
	items := f.col.Item
	if f.item != nil {
		if !f.item.Folder {
			printItem(e.Out, f.item, 0, long)
			return nil
		}
		items = f.item.Item
	}
	var walk func(items []*postman.Item, depth int)
	walk = func(items []*postman.Item, depth int) {
		for _, it := range items {
			printItem(e.Out, it, depth, long)
			walk(it.Item, depth+1)
		}
	}
	walk(items, 0)
	return nil
}

// printItem prints a line of ls: a folder's name with a "/" after, or a
// request's method and name, then the id (and with long, when it changed).
func printItem(w io.Writer, it *postman.Item, depth int, long bool) {
	name := it.Name + "/"
	if !it.Folder {
		method := "GET"
		if it.Request != nil && it.Request.Method != "" {
			method = it.Request.Method
		}
		name = method + " " + it.Name
	}
	line := strings.Repeat("  ", depth) + name + "\t" + it.ID
	if long {
		line += "\t" + it.UpdatedAt
	}
	fmt.Fprintln(w, line)
}

func cat(ctx context.Context, e *Env, args []string) error {
	args, err := e.flags("cat", args, nil)
	if err != nil {
		return err
	}
	if len(args) != 1 {
		return errors.New("cat: give the request's path")
	}
	ws, err := e.workspace(ctx)
	if err != nil {
		return err
	}
	f, err := e.find(ctx, ws, args[0])
	if err != nil {
		return err
	}
	if f.item == nil || f.item.Folder {
		return fmt.Errorf("cat: %s is not a request", args[0])
	}
	_, err = io.WriteString(e.Out, reqdoc.Render(f.item))
	return err
}

func save(ctx context.Context, e *Env, args []string) error {
	since := ""
	args, err := e.flags("save", args, func(fs *flag.FlagSet) { fs.StringVar(&since, "since", "", "") })
	if err != nil {
		return err
	}
	if len(args) < 1 || len(args) > 2 {
		return errors.New("save: give the request's path, and a file (or - for stdin)")
	}
	ws, err := e.writable(ctx)
	if err != nil {
		return err
	}
	f, err := e.find(ctx, ws, args[0])
	if err != nil {
		return err
	}
	if f.item == nil || f.item.Folder {
		return fmt.Errorf("save: %s is not a request", args[0])
	}
	text, err := e.read(args[1:])
	if err != nil {
		return err
	}
	it, err := reqdoc.Apply(f.item, text)
	if err != nil {
		return err
	}
	if reqdoc.Render(it) == reqdoc.Render(f.item) {
		fmt.Fprintln(e.Out, "no changes to save")
		return nil
	}
	conflict, err := store.CheckRequest(ctx, e.API, f.col.Info.UID, postman.Ref(it.ID, it.UID), since)
	if err != nil {
		return err
	}
	if conflict != nil {
		by := ""
		if conflict.By != "" {
			by = " by user " + conflict.By
		}
		return fmt.Errorf("not saved: %s changed in Postman at %s%s, after %s", it.Name, conflict.At, by, since)
	}
	at, err := e.API.UpdateRequest(ctx, f.col.Info.UID, it)
	if err != nil {
		return err
	}
	fmt.Fprintf(e.Out, "saved %s\t%s\t%s\n", it.Name, it.ID, at)
	return nil
}

func add(ctx context.Context, e *Env, args []string) error {
	args, err := e.flags("add", args, nil)
	if err != nil {
		return err
	}
	if len(args) < 1 || len(args) > 2 {
		return errors.New("add: give the new request's path, and a file (or - for stdin)")
	}
	ws, err := e.writable(ctx)
	if err != nil {
		return err
	}
	f, name, err := e.findNew(ctx, ws, args[0])
	if err != nil {
		return err
	}
	text, err := e.read(args[1:])
	if err != nil {
		return err
	}
	it, err := reqdoc.Apply(&postman.Item{Name: name}, text)
	if err != nil {
		return err
	}
	made, err := e.API.CreateRequest(ctx, f.col.Info.UID, f.parentRef(), it)
	if err != nil {
		return err
	}
	fmt.Fprintf(e.Out, "made %s\t%s\n", name, made.ID)
	return nil
}

func mkdir(ctx context.Context, e *Env, args []string) error {
	args, err := e.flags("mkdir", args, nil)
	if err != nil {
		return err
	}
	if len(args) != 1 {
		return errors.New("mkdir: give the new folder's path")
	}
	ws, err := e.writable(ctx)
	if err != nil {
		return err
	}
	f, name, err := e.findNew(ctx, ws, args[0])
	if err != nil {
		return err
	}
	made, err := e.API.CreateFolder(ctx, f.col.Info.UID, f.parentRef(), &postman.Item{Name: name, Folder: true})
	if err != nil {
		return err
	}
	fmt.Fprintf(e.Out, "made %s/\t%s\n", name, made.ID)
	return nil
}

// read reads the text a command was given: the file named, or stdin for -
// or no file.
func (e *Env) read(args []string) (string, error) {
	if len(args) == 0 || args[0] == "-" {
		data, err := io.ReadAll(e.In)
		return string(data), err
	}
	data, err := os.ReadFile(args[0])
	return string(data), err
}

// workspace finds the workspace to work in.
func (e *Env) workspace(ctx context.Context) (postman.Workspace, error) {
	want := e.Workspace
	if want == "" {
		want = e.Last
	}
	if want == "" {
		return postman.Workspace{}, errors.New("which workspace? give -w NAME (posthero workspaces lists them)")
	}
	list, err := e.API.Workspaces(ctx)
	if err != nil {
		return postman.Workspace{}, err
	}
	for _, ws := range list {
		if ws.ID == want {
			return ws, nil
		}
	}
	var found []postman.Workspace
	for _, ws := range list {
		if strings.EqualFold(ws.Name, want) {
			found = append(found, ws)
		}
	}
	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		return postman.Workspace{}, fmt.Errorf("no workspace %q (posthero workspaces lists them)", want)
	}
	return postman.Workspace{}, fmt.Errorf("%d workspaces are called %q; give its id", len(found), want)
}

// writable is the workspace for a command that changes it. A workspace in
// local mode isn't: its changes go to the copy, which the commands don't
// know.
func (e *Env) writable(ctx context.Context) (postman.Workspace, error) {
	ws, err := e.workspace(ctx)
	if err == nil && e.Local != nil && e.Local(ws.ID) {
		err = fmt.Errorf("%s is in local mode; :push or :set nolocal in posthero first", ws.Name)
	}
	return ws, err
}

// found is where a path led: a collection, and an item in it (nil: the
// collection itself) with the folders it is in.
type found struct {
	sum     postman.Summary
	col     *postman.Collection
	item    *postman.Item
	parents []*postman.Item
}

// parentRef names the folder found (or none: the collection's top) to the
// API.
func (f found) parentRef() string {
	if f.item == nil {
		return ""
	}
	return postman.Ref(f.item.ID, f.item.UID)
}

// find follows a path to a collection, folder or request, as Postman has
// them now.
func (e *Env) find(ctx context.Context, ws postman.Workspace, path string) (found, error) {
	cols, err := e.API.Collections(ctx, ws.ID)
	if err != nil {
		return found{}, err
	}
	return follow(path, ws.Name, cols, func(sum postman.Summary) (*postman.Collection, error) {
		return e.API.Collection(ctx, sum.UID)
	})
}

// follow follows a path through collections, getting the one it names with
// load.
func follow(path, where string, cols []postman.Summary, load func(postman.Summary) (*postman.Collection, error)) (found, error) {
	parts := splitPath(path)
	if len(parts) == 0 {
		return found{}, errors.New("give a path, like Collection/Folder/Request")
	}
	sum, err := pick(cols, parts[0], where, func(c postman.Summary) (string, []string) { return c.Name, []string{c.ID, c.UID} })
	if err != nil {
		return found{}, err
	}
	col, err := load(sum)
	if err != nil {
		return found{}, err
	}
	f := found{sum: sum, col: col}
	items, where := col.Item, col.Info.Name
	for _, part := range parts[1:] {
		if f.item != nil && !f.item.Folder {
			return found{}, fmt.Errorf("%s is a request, not a folder", where)
		}
		it, err := pick(items, part, where, func(it *postman.Item) (string, []string) { return it.Name, []string{it.ID, it.UID} })
		if err != nil {
			return found{}, err
		}
		if f.item != nil {
			f.parents = append(f.parents, f.item)
		}
		f.item, items, where = it, it.Item, where+"/"+it.Name
	}
	return f, nil
}

// findNew follows a path to where a new item goes: the folder (or
// collection) it goes in, and its name. Something called that already
// there is an error, so names stay paths.
func (e *Env) findNew(ctx context.Context, ws postman.Workspace, path string) (found, string, error) {
	parts := splitPath(path)
	if len(parts) < 2 {
		return found{}, "", fmt.Errorf("%q: give the collection (and folders) it goes in, then its name", path)
	}
	name := parts[len(parts)-1]
	f, err := e.find(ctx, ws, joinPath(parts[:len(parts)-1]))
	if err != nil {
		return found{}, "", err
	}
	if f.item != nil && !f.item.Folder {
		return found{}, "", fmt.Errorf("%s is a request, not a folder", f.item.Name)
	}
	for _, it := range *f.col.Container(f.item) {
		if it.Name == name {
			return found{}, "", fmt.Errorf("%s is there already (%s)", name, it.ID)
		}
	}
	return f, name, nil
}

// pick finds one thing by id, else by name (exactly, else ignoring case).
func pick[T any](list []T, part, where string, key func(T) (string, []string)) (T, error) {
	var zero T
	for _, x := range list {
		if _, ids := key(x); slices.Contains(ids, part) {
			return x, nil
		}
	}
	for _, fold := range []bool{false, true} {
		var hits []T
		for _, x := range list {
			if name, _ := key(x); name == part || (fold && strings.EqualFold(name, part)) {
				hits = append(hits, x)
			}
		}
		switch {
		case len(hits) == 1:
			return hits[0], nil
		case len(hits) > 1:
			return zero, fmt.Errorf("%d things in %s are called %q; give the id", len(hits), where, part)
		}
	}
	return zero, fmt.Errorf("nothing called %q in %s", part, where)
}

// splitPath splits a path at "/" that isn't written "\/".
func splitPath(path string) []string {
	var parts []string
	var b strings.Builder
	for i := 0; i < len(path); i++ {
		switch {
		case path[i] == '\\' && i+1 < len(path) && path[i+1] == '/':
			b.WriteByte('/')
			i++
		case path[i] == '/':
			parts = append(parts, strings.TrimSpace(b.String()))
			b.Reset()
		default:
			b.WriteByte(path[i])
		}
	}
	parts = append(parts, strings.TrimSpace(b.String()))
	return slices.DeleteFunc(parts, func(p string) bool { return p == "" })
}

func joinPath(parts []string) string {
	escaped := make([]string, len(parts))
	for i, p := range parts {
		escaped[i] = strings.ReplaceAll(p, "/", `\/`)
	}
	return strings.Join(escaped, "/")
}

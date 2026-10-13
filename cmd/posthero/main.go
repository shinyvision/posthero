// Command posthero is Postman for the terminal, built for vim users.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/shinyvision/posthero/internal/config"
	"github.com/shinyvision/posthero/internal/demo"
	"github.com/shinyvision/posthero/internal/secret"
	"github.com/shinyvision/posthero/internal/ui/app"
)

// version is set by the release build (-ldflags "-X main.version=v1.2.3").
var version = ""

const usage = `posthero — Postman for the terminal, built for vim users

Usage:
  posthero                 open your team's Postman workspace
  posthero REQUEST         open a request (or an example): its name, id or
                           Postman link
  posthero FILE.json …     open collection and environment exports instead
  posthero --demo          try posthero on a made-up workspace

Options:
  -w, --workspace NAME     open this workspace (a name or id)
  --demo                   use the built-in demo
  -h, --help               show this help
  -v, --version            print the version

Connecting:
  The first time, posthero asks for a Postman API key (it goes in the
  system keyring). Until then it uses $POSTMAN_API_KEY.

Environment:
  POSTHERO_EDITOR          nvim, vim or builtin: the request editor
                           (default: nvim, else vim, else built in)
  POSTMAN_API_KEY          the API key, when posthero has none of its own

Files (on Linux; ? inside posthero shows where they are here):
  ~/.config/posthero/postherorc   options and key mappings
  ~/.config/posthero/login.json   the API key (or where it is)
  ~/.local/state/posthero/        drafts, your variable values, tokens,
                                  cookies and the history
  ~/.cache/posthero/              the workspaces as last fetched

Inside posthero, press ? for every key and command.
`

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "posthero:", err)
		os.Exit(1)
	}
}

func run() error {
	flags := flag.NewFlagSet("posthero", flag.ContinueOnError)
	flags.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	var showVersion, useDemo bool
	var workspace string
	flags.BoolVar(&showVersion, "v", false, "")
	flags.BoolVar(&showVersion, "version", false, "")
	flags.BoolVar(&useDemo, "demo", false, "")
	flags.StringVar(&workspace, "w", "", "")
	flags.StringVar(&workspace, "workspace", "", "")
	if err := flags.Parse(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if showVersion {
		fmt.Println("posthero", appVersion())
		return nil
	}
	dir, _ := os.Getwd()
	opts := app.Options{Dir: dir, Version: appVersion()}
	var files []string
	for _, a := range flags.Args() {
		if isFile(a) {
			p, _ := filepath.Abs(a)
			files = append(files, p)
		} else if opts.Open == "" {
			opts.Open = a
		} else {
			flags.Usage()
			return fmt.Errorf("%q: give one request at most (or files that exist)", a)
		}
	}
	opts.Files = files
	var notices []string
	if useDemo {
		// The demo keeps nothing: no keyring, no files once it quits.
		secret.MockInit()
		d, err := demo.Start()
		if err != nil {
			return err
		}
		defer d.Close()
		opts.Login = config.Login{APIKey: d.APIKey, From: "demo"}
		opts.HasLogin, opts.Demo = true, true
		opts.APIBase = d.APIURL
		opts.RC = config.RC{Settings: config.DefaultSettings()}
		opts.State.Workspace = d.Workspace
		// What the demo keeps (drafts, a local copy) goes when it quits.
		if dir, err := os.MkdirTemp("", "posthero-demo-"); err == nil {
			defer os.RemoveAll(dir)
			opts.StateDir = dir
		}
		notices = append(notices, d.Notice())
	} else {
		login, ok, err := config.LoadLogin()
		switch {
		case err != nil && ok:
			notices = append(notices, "couldn't read posthero's own API key ("+err.Error()+"), so using $POSTMAN_API_KEY")
		case err != nil:
			notices = append(notices, "API key: "+err.Error())
		}
		opts.Login, opts.HasLogin = login, ok
		rc, err := config.LoadRC()
		if err != nil {
			notices = append(notices, err.Error())
		}
		opts.RC = rc
		opts.State = config.LoadState()
		opts.SaveState = config.SaveState
		if opts.StateDir, err = makeDir(config.StateDir); err != nil {
			return err
		}
		if opts.CacheDir, err = makeDir(config.CacheDir); err != nil {
			notices = append(notices, "no cache: "+err.Error())
		}
	}
	if workspace != "" {
		opts.RC.Settings.Workspace = workspace
	}
	if len(notices) > 0 {
		opts.Notice = strings.Join(notices, " · ")
	}
	m := app.New(opts)
	defer m.Close()
	p := tea.NewProgram(m)
	m.SetProgram(p)
	if _, err := p.Run(); err != nil {
		return err
	}
	if note := m.ExitNote(); note != "" {
		fmt.Fprintln(os.Stderr, "posthero:", note)
	}
	return nil
}

// isFile reports whether a command-line argument is a file to open.
func isFile(a string) bool {
	fi, err := os.Stat(a)
	return err == nil && !fi.IsDir()
}

func makeDir(dir func() (string, error)) (string, error) {
	d, err := dir()
	if err != nil {
		return "", err
	}
	return d, os.MkdirAll(d, 0o700)
}

// appVersion is the release version, or the module version for go install.
func appVersion() string {
	if version != "" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "dev"
}

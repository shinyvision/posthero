// Package vimpty embeds classic Vim by running it in a pseudo terminal and
// emulating that terminal. Vim reports writes back to posthero with a private
// OSC escape sequence, and asks for completions over a JSON channel.
package vimpty

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"

	"github.com/shinyvision/posthero/internal/editor"
	"github.com/shinyvision/posthero/internal/mouse"
)

// writeMarker is a private OSC sequence Vim emits when the buffer was
// written.
const writeMarker = "\x1b]7777;posthero-write\x07"

// vimrcModal reports writes.
const vimrcModal = `autocmd BufWritePost <buffer> call echoraw("\x1b]7777;posthero-write\x07")
`

// vimrcChannel talks to posthero over a JSON channel for completion: the
// omnifunc asks for candidates, and typing a @mention or a field value in
// the front matter opens the menu on its own. Vim without +channel gets
// none of it.
const vimrcChannel = `let s:posthero_ch = has('channel') ? ch_open('127.0.0.1:' . s:posthero_port, {'mode': 'json', 'waittime': 3000}) : ''
function! s:PostheroOpen() abort
  return has('channel') && type(s:posthero_ch) == v:t_channel && ch_status(s:posthero_ch) ==# 'open'
endfunction
function! s:PostheroRequest() abort
  if !s:PostheroOpen()
    return [-3, []]
  endif
  let l:res = ch_evalexpr(s:posthero_ch, ['complete', s:posthero_token, getline(1, '$'), line('.') - 1, col('.') - 1], {'timeout': 2000})
  if type(l:res) != v:t_list || len(l:res) != 2
    return [-3, []]
  endif
  return l:res
endfunction
let s:posthero_items = []
function! PostheroOmni(findstart, base) abort
  if a:findstart
    let l:res = s:PostheroRequest()
    let s:posthero_items = l:res[1]
    return l:res[0]
  endif
  return s:posthero_items
endfunction
let s:posthero_typed = 0
function! s:PostheroAuto() abort
  " Only a typed character opens the menu: accepting an item changes the
  " text too, and must not open it again.
  if !s:posthero_typed
    return
  endif
  let s:posthero_typed = 0
  let l:before = strpart(getline('.'), 0, col('.') - 1)
  if l:before !~# '{{[^{}]*$'
    return
  endif
  let l:res = s:PostheroRequest()
  if l:res[0] < 0 || empty(l:res[1])
    return
  endif
  " The menu opens while typing, so it must never insert on its own.
  if &completeopt !~# 'noinsert' && &completeopt !~# 'noselect'
    set completeopt+=noinsert
  endif
  set completeopt+=menuone
  call complete(l:res[0] + 1, l:res[1])
endfunction
function! s:PostheroNotify(name, ...) abort
  if s:PostheroOpen()
    call ch_sendexpr(s:posthero_ch, [a:name, s:posthero_token] + a:000)
  endif
endfunction
`

// vimrcModal sets up a dialog's buffer: completion and the menu opening
// on {{.
const vimrcModalChannel = `setlocal omnifunc=PostheroOmni
" The file is a temporary one: no undo file for it in the user's undodir.
setlocal noundofile
augroup posthero
  autocmd!
  autocmd InsertCharPre <buffer> let s:posthero_typed = 1
  autocmd TextChangedI,TextChangedP <buffer> call s:PostheroAuto()
augroup END
`

// vimrcMain makes Vim posthero's request pane: in posthero's buffers <CR>
// sends, K and gd ask about the {{variable}} under the cursor, and writes
// and changes are reported; ctrl-w window commands go to posthero's panes.
const vimrcMain = `autocmd BufRead,BufNewFile *.posthero set filetype=posthero
function! s:PostheroSetup() abort
  if exists('b:posthero_ready')
    return
  endif
  let b:posthero_ready = 1
  " The files are temporary ones: no undo files for them in the undodir.
  setlocal bufhidden=hide noswapfile autoread noundofile
  if &l:omnifunc ==# ''
    setlocal omnifunc=PostheroOmni
  endif
  " ZZ and ZQ are :x and :q! (see below): they go to posthero too.
  nnoremap <buffer> <silent> ZZ :<C-u>PostheroUpdateQuit<CR>
  nnoremap <buffer> <silent> ZQ :<C-u>PostheroQuit!<CR>
  nnoremap <buffer> <silent> <CR> :<C-u>call <SID>PostheroNotify('send', expand('%:p'), getline(1, '$'), line('.') - 1, col('.') - 1)<CR>
  nnoremap <buffer> <silent> K :<C-u>call <SID>PostheroNotify('hover', expand('%:p'), getline(1, '$'), line('.') - 1, col('.') - 1, 'K')<CR>
  nnoremap <buffer> <silent> gd :<C-u>call <SID>PostheroNotify('hover', expand('%:p'), getline(1, '$'), line('.') - 1, col('.') - 1, 'gd')<CR>
  autocmd posthero InsertCharPre <buffer> let s:posthero_typed = 1
  autocmd posthero TextChangedI,TextChangedP <buffer> call s:PostheroAuto()
endfunction
let s:posthero_last = {}
let s:posthero_told = {}
function! s:PostheroReport(buf) abort
  if a:buf < 1 || !bufloaded(a:buf) || bufname(a:buf) !~# '\.posthero$'
    return
  endif
  let l:text = join(getbufline(a:buf, 1, '$'), "\n")
  let l:mod = getbufvar(a:buf, '&modified')
  let l:key = l:text . (l:mod ? '+' : '-')
  if get(s:posthero_last, a:buf, '') ==# l:key
    return
  endif
  let s:posthero_last[a:buf] = l:key
  let s:posthero_told[a:buf] = l:text
  call s:PostheroNotify('changed', fnamemodify(bufname(a:buf), ':p'), l:text, l:mod ? 1 : 0)
endfunction
augroup posthero
  autocmd!
  autocmd BufEnter,BufWinEnter,FileType *.posthero call s:PostheroSetup()
  autocmd BufWritePost *.posthero let s:posthero_last[str2nr(expand('<abuf>'))] = '' | call s:PostheroNotify('write', expand('<afile>:p'), getbufline(str2nr(expand('<abuf>')), 1, '$')) | call s:PostheroReport(str2nr(expand('<abuf>')))
  autocmd TextChanged,InsertLeave,BufLeave *.posthero call s:PostheroReport(str2nr(expand('<abuf>')))
  " Typing in insert mode is reported too, as it happens.
  autocmd TextChangedI,TextChangedP *.posthero call s:PostheroReport(str2nr(expand('<abuf>')))
  " posthero rewrites a file when Postman's version changes: a buffer
  " without changes reads it again, one with changes keeps them (posthero
  " has them as a draft). Never a W12 question.
  autocmd FileChangedShell *.posthero let v:fcs_choice = getbufvar(str2nr(expand('<abuf>')), '&modified') ? '' : 'reload'
augroup END
function! PostheroOpenFile(file, text) abort
  execute 'silent! keepalt hide edit' fnameescape(a:file)
  call s:PostheroSetup()
  " A draft stands in for the file's text, but a buffer with changes keeps
  " them: they are newer than anything posthero has heard of.
  if type(a:text) == v:t_string && !&modified
    let l:want = split(a:text, "\n", 1)
    if getline(1, '$') !=# l:want
      silent! %delete _
      call setline(1, l:want)
    endif
  endif
  call s:PostheroReport(bufnr('%'))
endfunction
" PostheroReload reads a file again after posthero rewrote it, unless the
" buffer has changes posthero hasn't heard of (it asks only when it has no
" draft: the changes it heard of are none, or undone).
function! PostheroReload(file) abort
  let l:buf = bufnr(a:file)
  if l:buf < 1 || !bufloaded(l:buf)
    return
  endif
  if getbufvar(l:buf, '&modified') && get(s:posthero_told, l:buf, '') !=# join(getbufline(l:buf, 1, '$'), "\n")
    return
  endif
  let l:here = bufnr('%')
  execute 'silent! keepalt hide buffer' l:buf
  silent! edit!
  execute 'silent! keepalt hide buffer' l:here
  let s:posthero_last[l:buf] = ''
  call s:PostheroReport(l:buf)
endfunction
function! PostheroGoto(line, col) abort
  call cursor(a:line, a:col)
  normal! zz
endfunction
function! s:PostheroWin(nr) abort
  call s:PostheroNotify('win', nr2char(a:nr))
endfunction
" :q and its friends in a request go to posthero, as they do in its other
" panes: :q quits posthero, :wq and :x save the request to Postman first.
function! s:PostheroQuit(write, bang) abort
  if a:write ==# 'w'
    silent write
  elseif a:write ==# 'u'
    silent update
  endif
  call s:PostheroNotify('win', (a:write !=# '' ? 'writequit' : 'quit') . (a:bang ? '!' : ''))
endfunction
command! -bang PostheroQuit call <SID>PostheroQuit('', <bang>0)
command! -bang PostheroWriteQuit call <SID>PostheroQuit('w', <bang>0)
command! -bang PostheroUpdateQuit call <SID>PostheroQuit('u', <bang>0)
for [s:from, s:to] in items({'q': 'PostheroQuit', 'quit': 'PostheroQuit', 'qa': 'PostheroQuit', 'qall': 'PostheroQuit', 'quita': 'PostheroQuit', 'quitall': 'PostheroQuit', 'wq': 'PostheroWriteQuit', 'wqa': 'PostheroWriteQuit', 'wqall': 'PostheroWriteQuit', 'x': 'PostheroUpdateQuit', 'xit': 'PostheroUpdateQuit', 'xa': 'PostheroUpdateQuit', 'xall': 'PostheroUpdateQuit', 'exi': 'PostheroUpdateQuit', 'exit': 'PostheroUpdateQuit'})
  execute printf("cnoreabbrev <expr> %s getcmdtype() ==# ':' && getcmdline() ==# '%s' && &filetype ==# 'posthero' ? '%s' : '%s'", s:from, s:from, s:to, s:from)
endfor
call s:PostheroSetup()
`

// vimrcCommon sets up the mouse and the escape wait.
const vimrcCommon = `set shortmess+=I
" posthero keeps its own drafts: no backup files next to its buffers.
set nobackup nowritebackup
" Its buffers are files in a temporary directory: not for the recent files.
if &viminfo !=# ''
  execute 'set viminfo+=r' . fnameescape($TMPDIR !=# '' ? $TMPDIR : '/tmp') . '/posthero-'
endif
" Vim's own default for 'mouse' is empty (defaults.vim, which turns it on,
" is skipped when there is a vimrc), so an empty value is not a choice.
if &mouse ==# ''
  set mouse=a
endif
silent! set ttymouse=sgr
" Without defaults.vim an escape waits a second for a key sequence; posthero's
" terminal sends whole sequences, so use defaults.vim's wait.
if &ttimeoutlen < 0 && !&ttimeout
  set ttimeout ttimeoutlen=100
endif
`

// Available reports whether vim is on PATH.
func Available() bool {
	_, err := exec.LookPath("vim")
	return err == nil
}

// Backend is Vim running in a pseudo terminal.
type Backend struct {
	cmd    *exec.Cmd
	pty    *os.File
	emu    *vt.SafeEmulator
	tmpDir string
	file   string
	events chan editor.Event
	ch     *channel

	mu        sync.Mutex
	shape     tea.CursorShape
	hidden    bool
	exited    atomic.Bool
	closeOnce sync.Once
}

// Start launches vim.
func Start(opts editor.Options, w, h int) (*Backend, error) {
	dir, err := os.MkdirTemp("", "posthero-vim-")
	if err != nil {
		return nil, err
	}
	b := &Backend{
		tmpDir: dir,
		file:   opts.File,
		emu:    vt.NewSafeEmulator(w, h),
		events: make(chan editor.Event, 16),
	}
	b.ch, err = listen(opts.Complete, b.events)
	if err != nil {
		os.RemoveAll(dir)
		return nil, fmt.Errorf("start vim: %w", err)
	}
	rc := filepath.Join(dir, "posthero.vim")
	script := vimrcCommon + fmt.Sprintf("let s:posthero_port = %d\nlet s:posthero_token = %q\n", b.ch.port, b.ch.token) + vimrcChannel
	if opts.Runtime != "" {
		script = "set runtimepath+=" + vimEscape(opts.Runtime) + "\n" + script
	}
	if opts.Main {
		script += vimrcMain + windowMappings()
	} else {
		script += vimrcModal + vimrcModalChannel
	}
	if opts.ReadOnly {
		script += "setlocal nomodifiable readonly\n"
	}
	// The first file was read before posthero's runtime was on the
	// 'runtimepath', and Vim may have taken its # lines for a conf file.
	script += "autocmd BufRead,BufNewFile *.posthero set filetype=posthero\nif expand('%:e') ==# 'posthero' && &filetype !=# 'posthero' | set filetype=posthero | endif\n"
	script += insertScript(opts.Insert)
	if err := os.WriteFile(rc, []byte(script), 0o600); err != nil {
		b.ch.close()
		os.RemoveAll(dir)
		return nil, err
	}
	b.cmd = exec.Command("vim", "-n", "--cmd", "let g:posthero = 1", "-S", rc, opts.File)
	b.cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	// Like the Neovim backend, Vim stays in posthero's session so tmux sees a
	// vim process on the pane (vim-tmux-navigator).
	var tty *os.File
	b.pty, tty, err = pty.Open()
	if err != nil {
		b.ch.close()
		os.RemoveAll(dir)
		return nil, fmt.Errorf("start vim: %w", err)
	}
	_ = pty.Setsize(b.pty, &pty.Winsize{Rows: uint16(h), Cols: uint16(w)})
	b.cmd.Stdin, b.cmd.Stdout, b.cmd.Stderr = tty, tty, tty
	err = b.cmd.Start()
	tty.Close()
	if err != nil {
		b.pty.Close()
		b.ch.close()
		os.RemoveAll(dir)
		return nil, fmt.Errorf("start vim: %w", err)
	}
	b.emu.SetCallbacks(vt.Callbacks{
		CursorStyle: func(style vt.CursorStyle, _ bool) {
			b.mu.Lock()
			defer b.mu.Unlock()
			b.shape = map[vt.CursorStyle]tea.CursorShape{vt.CursorBlock: tea.CursorBlock, vt.CursorUnderline: tea.CursorUnderline, vt.CursorBar: tea.CursorBar}[style]
		},
		CursorVisibility: func(visible bool) {
			b.mu.Lock()
			defer b.mu.Unlock()
			b.hidden = !visible
		},
	})
	go func() { _, _ = io.Copy(b.pty, b.emu) }() // key presses and terminal replies
	go b.readOutput()
	return b, nil
}

// insertScript starts insert mode as editor.Options.Insert asks.
func insertScript(mode string) string {
	switch {
	case mode == "":
		return ""
	case mode == "i":
		return "normal! gg0\nstartinsert\n"
	case mode == "A":
		return "normal! G$\nstartinsert!\n"
	}
	line, cmd := mode[:len(mode)-1], mode[len(mode)-1:]
	if line == "" {
		line = "1"
	}
	if cmd == "A" {
		return "call cursor(" + line + ", 1)\nstartinsert!\n"
	}
	return "call cursor(" + line + ", 1)\nstartinsert\n"
}

func (b *Backend) readOutput() {
	buf := make([]byte, 32*1024)
	var pending []byte
	for {
		n, err := b.pty.Read(buf)
		if n > 0 {
			pending = append(pending, buf[:n]...)
			for {
				i := bytes.Index(pending, []byte(writeMarker))
				if i < 0 {
					break
				}
				_, _ = b.emu.Write(pending[:i])
				pending = pending[i+len(writeMarker):]
				b.emitWrite()
			}
			// Hold back a possible partial marker at the end.
			keep := partialSuffix(pending, writeMarker)
			_, _ = b.emu.Write(pending[:len(pending)-keep])
			pending = append([]byte(nil), pending[len(pending)-keep:]...)
			editor.Notify(b.events, editor.Redraw{})
		}
		if err != nil {
			err = b.cmd.Wait()
			b.exited.Store(true)
			editor.Notify(b.events, editor.Exited{Err: err})
			return
		}
	}
}

func partialSuffix(data []byte, m string) int {
	for k := min(len(m)-1, len(data)); k > 0; k-- {
		if bytes.HasSuffix(data, []byte(m[:k])) {
			return k
		}
	}
	return 0
}

func (b *Backend) emitWrite() {
	data, err := os.ReadFile(b.file)
	if err != nil {
		return
	}
	editor.Notify(b.events, editor.Written{File: b.file, Text: strings.TrimSuffix(string(data), "\n")})
}

// Open implements editor.Backend.
func (b *Backend) Open(file string, text *string) {
	var t any
	if text != nil {
		t = *text
	}
	b.file = file
	b.ch.call("PostheroOpenFile", file, t)
}

// Reload implements editor.Backend.
func (b *Backend) Reload(file string) { b.ch.call("PostheroReload", file) }

// SetHints implements editor.Backend. Vim shows no hints.
func (b *Backend) SetHints(string, []editor.Hint) {}

// Goto implements editor.Backend.
func (b *Backend) Goto(line, col int) { b.ch.call("PostheroGoto", line+1, col+1) }

// windowKeys maps what may follow <C-w> in a mapping to the window command
// posthero gets.
var windowKeys = map[string]rune{
	"h": 'h', "<C-h>": 'h', "<Left>": 'h', "<BS>": 'h',
	"j": 'j', "<C-j>": 'j', "<Down>": 'j',
	"k": 'k', "<C-k>": 'k', "<Up>": 'k',
	"l": 'l', "<C-l>": 'l', "<Right>": 'l',
	"w": 'w', "<C-w>": 'w', "W": 'W',
	"p": 'p', "<C-p>": 'p',
	"t": 't', "<C-t>": 't',
	"b": 'b', "<C-b>": 'b',
	"o": 'o', "<C-o>": 'o',
	"c": 'c',
	"q": 'q', "<C-q>": 'q',
	"=": '=', "+": '+', "-": '-', "<lt>": '<', ">": '>',
	"_": '_', "<C-_>": '_', "<Bar>": '|',
}

// windowMappings maps the ctrl-w window commands to posthero. The key
// travels as a number so | and < need no escaping in the mapping.
func windowMappings() string {
	lhs := make([]string, 0, len(windowKeys))
	for k := range windowKeys {
		lhs = append(lhs, k)
	}
	sort.Strings(lhs)
	var sb strings.Builder
	sb.WriteString("if has('channel')\n")
	for _, k := range lhs {
		fmt.Fprintf(&sb, "  nnoremap <silent> <C-w>%s :<C-u>call <SID>PostheroWin(%d)<CR>\n", k, windowKeys[k])
	}
	sb.WriteString("endif\n")
	return sb.String()
}

// vimEscape escapes a file name for an ex command, like fnameescape().
func vimEscape(file string) string {
	var sb strings.Builder
	for _, r := range file {
		if strings.ContainsRune(" \t\n*?[{`$\\%#'\"|!<,", r) {
			sb.WriteByte('\\')
		}
		sb.WriteRune(r)
	}
	return sb.String()
}

// Name implements editor.Backend.
func (b *Backend) Name() string { return "vim" }

// Key implements editor.Backend.
func (b *Backend) Key(k tea.KeyPressMsg) {
	mod := k.Mod &^ (tea.ModCapsLock | tea.ModNumLock | tea.ModScrollLock)
	if k.Text != "" && mod&^tea.ModShift == 0 {
		// A typed character (shifted or not) is sent as itself: the
		// emulator drops text that comes with a shift modifier.
		r := []rune(k.Text)
		b.emu.SendKey(uv.KeyPressEvent{Text: k.Text, Code: r[0]})
		return
	}
	b.emu.SendKey(uv.KeyPressEvent{Text: k.Text, Mod: uv.KeyMod(mod), Code: k.Code})
}

// Paste implements editor.Backend.
func (b *Backend) Paste(text string) { b.emu.Paste(text) }

// Mouse implements editor.Backend. The terminal emulator encodes the event
// only if Vim enabled mouse reporting; Vim tells double clicks apart itself.
func (b *Backend) Mouse(ev mouse.Event) {
	m := uv.Mouse{X: ev.X, Y: ev.Y, Button: ev.Button, Mod: uv.KeyMod(ev.Mod)}
	switch ev.Action {
	case mouse.Press:
		b.emu.SendMouse(uv.MouseClickEvent(m))
	case mouse.Release:
		b.emu.SendMouse(uv.MouseReleaseEvent(m))
	case mouse.Drag:
		b.emu.SendMouse(uv.MouseMotionEvent(m))
	case mouse.Wheel:
		b.emu.SendMouse(uv.MouseWheelEvent(m))
	}
}

// Command implements editor.Backend.
func (b *Backend) Command(cmd string) {
	// ctrl-\ ctrl-n reaches normal mode from any mode.
	_, _ = b.pty.Write([]byte("\x1c\x0e:" + cmd + "\r"))
}

// Resize implements editor.Backend.
func (b *Backend) Resize(w, h int) {
	if w < 1 || h < 1 {
		return
	}
	b.emu.Resize(w, h)
	_ = pty.Setsize(b.pty, &pty.Winsize{Rows: uint16(h), Cols: uint16(w)})
	if b.cmd.Process != nil {
		// Vim is not the pty's foreground process, so tell it directly.
		notifyResize(b.cmd.Process)
	}
}

// Render implements editor.Backend.
func (b *Backend) Render() []string {
	lines := strings.Split(b.emu.Render(), "\n")
	for i := range lines {
		lines[i] = strings.TrimSuffix(lines[i], "\r") + "\x1b[0m"
	}
	return lines
}

// Cursor implements editor.Backend.
func (b *Backend) Cursor() (int, int, tea.CursorShape, bool) {
	p := b.emu.CursorPosition()
	b.mu.Lock()
	defer b.mu.Unlock()
	return p.X, p.Y, b.shape, !b.hidden
}

// Mode implements editor.Backend. Vim does not report its mode through the
// terminal, so there is none.
func (b *Backend) Mode() string { return "" }

// Events implements editor.Backend.
func (b *Backend) Events() <-chan editor.Event { return b.events }

// Close implements editor.Backend.
func (b *Backend) Close() {
	b.closeOnce.Do(func() {
		if !b.exited.Load() && b.cmd.Process != nil {
			// Quit, and wait for Vim to be gone (as with Neovim), so it
			// isn't still writing files when its directories are removed.
			_, _ = b.pty.Write([]byte("\x1b:qa!\r"))
			for i := 0; i < 100 && !b.exited.Load(); i++ {
				time.Sleep(20 * time.Millisecond)
			}
			if !b.exited.Load() {
				_ = b.cmd.Process.Kill()
			}
		}
		b.ch.close()
		// Ends the goroutine copying key presses to the pty. Emulator.Close
		// would do it too, but it races with the Read that goroutine is in.
		if pw, ok := b.emu.InputPipe().(*io.PipeWriter); ok {
			_ = pw.CloseWithError(io.EOF)
		}
		go func() {
			// The pty closes once Vim is gone, ending readOutput.
			for i := 0; i < 50 && !b.exited.Load(); i++ {
				time.Sleep(50 * time.Millisecond)
			}
			_ = b.pty.Close()
		}()
		_ = os.RemoveAll(b.tmpDir)
	})
}

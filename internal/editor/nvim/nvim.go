// Package nvim embeds Neovim as an editor. Neovim runs headless inside a
// pseudo terminal (so plugins that inspect the terminal, like image.nvim,
// find one) and posthero attaches to it as a UI over a socket (ext_linegrid),
// rendering the grid itself. The user's config, plugins, colorscheme and
// keymaps all work. Configs can check g:posthero to tell they run inside posthero.
package nvim

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/creack/pty"
	"github.com/neovim/go-client/nvim"

	"github.com/shinyvision/posthero/internal/editor"
	"github.com/shinyvision/posthero/internal/keys"
	"github.com/shinyvision/posthero/internal/mouse"
	"github.com/shinyvision/posthero/internal/vim"
)

// Available reports whether nvim is on PATH.
func Available() bool {
	_, err := exec.LookPath("nvim")
	return err == nil
}

// Backend is an embedded Neovim instance.
type Backend struct {
	v        *nvim.Nvim
	cmd      *exec.Cmd
	pty      *os.File
	tmpDir   string
	ui       *screen
	events   chan editor.Event
	calls    chan func()
	once     sync.Once
	done     chan struct{}
	complete editor.Completer

	regs *vim.Registers // posthero's, behind "+ and "* without a clipboard tool
	// ready is set once Neovim is attached and set up.
	ready atomic.Bool
}

// modalLua reports writes of a dialog's buffer to posthero, and gives it
// omni completion.
// filetypeLua makes posthero's files posthero's filetype. The first file
// was read before posthero's runtime was on the 'runtimepath', and Neovim
// may have taken its # lines for a conf file.
const filetypeLua = `vim.filetype.add({ extension = { posthero = 'posthero' } })
if vim.api.nvim_buf_get_name(0):match('%.posthero$') and vim.bo.filetype ~= 'posthero' then
  vim.bo.filetype = 'posthero'
end
`

const modalLua = `
local chan = ...
vim.api.nvim_create_autocmd('BufWritePost', {
  buffer = 0,
  callback = function()
    vim.rpcnotify(chan, 'posthero_write', vim.api.nvim_buf_get_name(0), vim.api.nvim_buf_get_lines(0, 0, -1, false))
  end,
})
local items = {}
function _G.posthero_omnifunc(findstart, _)
  if findstart == 1 then
    local pos = vim.api.nvim_win_get_cursor(0)
    local ok, res = pcall(vim.rpcrequest, chan, 'posthero_complete', vim.api.nvim_buf_get_lines(0, 0, -1, false), pos[1] - 1, pos[2])
    if not ok or type(res) ~= 'table' then
      items = {}
      return -3
    end
    items = res[2]
    return res[1]
  end
  return items
end
vim.bo.omnifunc = 'v:lua.posthero_omnifunc'
-- The file is a temporary one: no undo file for it in the user's undodir.
vim.bo.undofile = false
-- Typing {{ offers the variables. Only a typed character opens the menu:
-- accepting an item changes the text too, and must not open it again.
local typed = false
vim.api.nvim_create_autocmd('InsertCharPre', { buffer = 0, callback = function() typed = true end })
vim.api.nvim_create_autocmd({ 'TextChangedI', 'TextChangedP' }, {
  buffer = 0,
  callback = function()
    if not typed then
      return
    end
    typed = false
    local col = vim.api.nvim_win_get_cursor(0)[2]
    if not vim.api.nvim_get_current_line():sub(1, col):match('{{[^{}]*$') then
      return
    end
    local start = posthero_omnifunc(1, '')
    if start < 0 or #items == 0 then
      return
    end
    local co = vim.o.completeopt
    if not co:find('noinsert') and not co:find('noselect') then
      vim.o.completeopt = co .. ',noinsert'
    end
    if not co:find('menuone') then
      vim.o.completeopt = vim.o.completeopt .. ',menuone'
    end
    vim.fn.complete(start + 1, items)
  end,
})
-- Without posthero there is nobody to edit for.
vim.api.nvim_create_autocmd('UILeave', { callback = function() vim.cmd('qa!') end })
`

// mainLua makes Neovim posthero's request pane. In posthero's buffers
// (*.posthero): <CR> in normal mode sends the request, K and gd ask about
// the {{variable}} under the cursor, writes and changes are reported, {{
// offers variable names, and hints show variables' values. Ctrl-w window
// commands go to posthero's panes everywhere.
const mainLua = `
local chan = ...
local group = vim.api.nvim_create_augroup('posthero', { clear = true })
local ns = vim.api.nvim_create_namespace('posthero_hints')
vim.api.nvim_set_hl(0, 'PostheroValue', { default = true, link = 'Comment' })
vim.api.nvim_set_hl(0, 'PostheroMissing', { default = true, link = 'DiagnosticWarn' })
vim.api.nvim_set_hl(0, 'PostheroNote', { default = true, link = 'Comment' })
vim.filetype.add({ extension = { posthero = 'posthero' } })

local function ours(buf)
  return vim.api.nvim_buf_get_name(buf):match('%.posthero$') ~= nil
end
local function lines(buf)
  return vim.api.nvim_buf_get_lines(buf, 0, -1, false)
end

local items = {}
function _G.posthero_omnifunc(findstart, _)
  if findstart == 1 then
    local pos = vim.api.nvim_win_get_cursor(0)
    local ok, res = pcall(vim.rpcrequest, chan, 'posthero_complete', lines(0), pos[1] - 1, pos[2])
    if not ok or type(res) ~= 'table' then
      items = {}
      return -3
    end
    items = res[2]
    return res[1]
  end
  return items
end

local function setup(buf)
  if vim.b[buf].posthero_ready then
    return
  end
  vim.b[buf].posthero_ready = true
  vim.bo[buf].bufhidden = 'hide'
  vim.bo[buf].swapfile = false
  vim.bo[buf].autoread = true
  -- The files are temporary ones: no undo files for them in the user's undodir.
  vim.bo[buf].undofile = false
  if vim.bo[buf].omnifunc == '' then
    vim.bo[buf].omnifunc = 'v:lua.posthero_omnifunc'
  end
  local function at_cursor(method)
    return function()
      local pos = vim.api.nvim_win_get_cursor(0)
      vim.rpcnotify(chan, method, vim.api.nvim_buf_get_name(0), lines(0), pos[1] - 1, pos[2])
    end
  end
  vim.keymap.set('n', '<CR>', at_cursor('posthero_send'), { buffer = buf, desc = 'posthero: send the request' })
  vim.keymap.set('n', 'K', function()
    local pos = vim.api.nvim_win_get_cursor(0)
    vim.rpcnotify(chan, 'posthero_hover', vim.api.nvim_buf_get_name(0), lines(0), pos[1] - 1, pos[2], 'K')
  end, { buffer = buf, desc = 'posthero: what is the variable under the cursor' })
  vim.keymap.set('n', 'gd', function()
    local pos = vim.api.nvim_win_get_cursor(0)
    vim.rpcnotify(chan, 'posthero_hover', vim.api.nvim_buf_get_name(0), lines(0), pos[1] - 1, pos[2], 'gd')
  end, { buffer = buf, desc = 'posthero: edit the variable under the cursor' })
  -- ZZ and ZQ are :x and :q! (see below): they go to posthero too.
  vim.keymap.set('n', 'ZZ', '<Cmd>PostheroUpdateQuit<CR>', { buffer = buf, desc = 'posthero: save and quit' })
  vim.keymap.set('n', 'ZQ', '<Cmd>PostheroQuit!<CR>', { buffer = buf, desc = 'posthero: quit' })

  -- Typing {{ offers the variables, the way the menu opens while typing.
  local typed = false
  vim.api.nvim_create_autocmd('InsertCharPre', { group = group, buffer = buf, callback = function() typed = true end })
  vim.api.nvim_create_autocmd({ 'TextChangedI', 'TextChangedP' }, {
    group = group,
    buffer = buf,
    callback = function()
      if not typed then
        return
      end
      typed = false
      local col = vim.api.nvim_win_get_cursor(0)[2]
      local before = vim.api.nvim_get_current_line():sub(1, col)
      if not before:match('{{[^{}]*$') then
        return
      end
      local pos = vim.api.nvim_win_get_cursor(0)
      local ok, res = pcall(vim.rpcrequest, chan, 'posthero_complete', lines(0), pos[1] - 1, pos[2])
      if not ok or type(res) ~= 'table' or res[1] < 0 or #res[2] == 0 then
        return
      end
      local co = vim.o.completeopt
      if not co:find('noinsert') and not co:find('noselect') then
        vim.o.completeopt = co .. ',noinsert'
      end
      if not co:find('menuone') then
        vim.o.completeopt = vim.o.completeopt .. ',menuone'
      end
      vim.fn.complete(res[1] + 1, res[2])
    end,
  })
end

local last, told = {}, {}
local function report(buf)
  if not ours(buf) or not vim.api.nvim_buf_is_loaded(buf) then
    return
  end
  local text = table.concat(lines(buf), '\n')
  local modified = vim.bo[buf].modified
  local key = text .. (modified and '+' or '-')
  if last[buf] == key then
    return
  end
  last[buf], told[buf] = key, text
  vim.rpcnotify(chan, 'posthero_changed', vim.api.nvim_buf_get_name(buf), text, modified)
end

vim.api.nvim_create_autocmd({ 'BufEnter', 'BufWinEnter', 'FileType' }, {
  group = group,
  pattern = '*.posthero',
  callback = function(ev) setup(ev.buf) end,
})
vim.api.nvim_create_autocmd('BufWritePost', {
  group = group,
  pattern = '*.posthero',
  callback = function(ev)
    last[ev.buf] = nil
    vim.rpcnotify(chan, 'posthero_write', vim.api.nvim_buf_get_name(ev.buf), lines(ev.buf))
  end,
})
vim.api.nvim_create_autocmd({ 'TextChanged', 'InsertLeave', 'BufModifiedSet', 'BufLeave' }, {
  group = group,
  pattern = '*.posthero',
  callback = function(ev) report(ev.buf) end,
})
-- Typing in insert mode is reported too (a moment after the last key), so
-- a request sent or switched away from while still in insert mode is what
-- the buffer shows.
local soon = {}
vim.api.nvim_create_autocmd({ 'TextChangedI', 'TextChangedP' }, {
  group = group,
  pattern = '*.posthero',
  callback = function(ev)
    if soon[ev.buf] then
      return
    end
    soon[ev.buf] = true
    vim.defer_fn(function()
      soon[ev.buf] = nil
      if vim.api.nvim_buf_is_valid(ev.buf) then
        report(ev.buf)
      end
    end, 100)
  end,
})
-- posthero rewrites a file when Postman's version of the request changes.
-- A buffer without changes reads it again; one with changes keeps them
-- (posthero has them as a draft). Never a W12 question.
vim.api.nvim_create_autocmd('FileChangedShell', {
  group = group,
  pattern = '*.posthero',
  callback = function(ev)
    vim.v.fcs_choice = vim.bo[ev.buf].modified and '' or 'reload'
  end,
})
-- Without posthero there is nobody to edit for.
vim.api.nvim_create_autocmd('UILeave', { group = group, callback = function() vim.cmd('qa!') end })

local wincmds = {
  h = 'h', ['<C-h>'] = 'h', ['<Left>'] = 'h', ['<BS>'] = 'h',
  j = 'j', ['<C-j>'] = 'j', ['<Down>'] = 'j',
  k = 'k', ['<C-k>'] = 'k', ['<Up>'] = 'k',
  l = 'l', ['<C-l>'] = 'l', ['<Right>'] = 'l',
  w = 'w', ['<C-w>'] = 'w', W = 'W',
  p = 'p', ['<C-p>'] = 'p',
  t = 't', ['<C-t>'] = 't',
  b = 'b', ['<C-b>'] = 'b',
  o = 'o', ['<C-o>'] = 'o',
  c = 'c',
  q = 'q', ['<C-q>'] = 'q',
  ['='] = '=', ['+'] = '+', ['-'] = '-', ['<lt>'] = '<', ['>'] = '>',
  ['_'] = '_', ['<C-_>'] = '_', ['<Bar>'] = '|',
}
for lhs, key in pairs(wincmds) do
  vim.keymap.set('n', '<C-w>' .. lhs, function() vim.rpcnotify(chan, 'posthero_wincmd', key) end, { desc = 'posthero: window command' })
end

-- :q and its friends in a request go to posthero, as they do in its other
-- panes: :q quits posthero (asking when requests have changes Postman
-- doesn't have), :wq and :x save the request to Postman first. Neovim's own
-- :q would ask about the buffer (with 'confirm') or refuse.
local function quit(o, write)
  if write then
    vim.cmd(o.name == 'PostheroUpdateQuit' and 'silent update' or 'silent write')
  end
  vim.rpcnotify(chan, 'posthero_wincmd', (write and 'writequit' or 'quit') .. (o.bang and '!' or ''))
end
vim.api.nvim_create_user_command('PostheroQuit', function(o) quit(o, false) end, { bang = true })
vim.api.nvim_create_user_command('PostheroWriteQuit', function(o) quit(o, true) end, { bang = true })
vim.api.nvim_create_user_command('PostheroUpdateQuit', function(o) quit(o, true) end, { bang = true })
local quits = {
  q = 'PostheroQuit', quit = 'PostheroQuit', qa = 'PostheroQuit', qall = 'PostheroQuit', quita = 'PostheroQuit', quitall = 'PostheroQuit',
  wq = 'PostheroWriteQuit', wqa = 'PostheroWriteQuit', wqall = 'PostheroWriteQuit',
  x = 'PostheroUpdateQuit', xit = 'PostheroUpdateQuit', xa = 'PostheroUpdateQuit', xall = 'PostheroUpdateQuit', exi = 'PostheroUpdateQuit', exit = 'PostheroUpdateQuit',
}
for from, to in pairs(quits) do
  vim.cmd(string.format(
    [[cnoreabbrev <expr> %s getcmdtype() ==# ':' && getcmdline() ==# '%s' && &filetype ==# 'posthero' ? '%s' : '%s']],
    from, from, to, from))
end

function _G.posthero_open(file, text)
  vim.cmd('silent! keepalt hide edit ' .. vim.fn.fnameescape(file))
  local buf = vim.api.nvim_get_current_buf()
  setup(buf)
  -- A draft stands in for the file's text, but a buffer with changes keeps
  -- them: they are newer than anything posthero has heard of.
  if text ~= vim.NIL and text ~= nil and not vim.bo[buf].modified then
    local want = vim.split(text, '\n', { plain = true })
    local have = lines(buf)
    if table.concat(have, '\n') ~= text then
      vim.api.nvim_buf_set_lines(buf, 0, -1, false, want)
    end
  end
  report(buf)
end

-- posthero_reload reads a file again after posthero rewrote it, unless the
-- buffer has changes posthero hasn't heard of (it asks only when it has no
-- draft: the changes it heard of are none, or undone).
function _G.posthero_reload(file)
  local buf = vim.fn.bufnr(file)
  if buf < 0 or not vim.api.nvim_buf_is_loaded(buf) then
    return
  end
  if vim.bo[buf].modified and told[buf] ~= table.concat(lines(buf), '\n') then
    return
  end
  vim.api.nvim_buf_call(buf, function() vim.cmd('silent! edit!') end)
  last[buf] = nil
  report(buf)
end

function _G.posthero_hints(file, hints)
  local buf = vim.fn.bufnr(file)
  if buf < 0 or not vim.api.nvim_buf_is_loaded(buf) then
    return
  end
  vim.api.nvim_buf_clear_namespace(buf, ns, 0, -1)
  local count = vim.api.nvim_buf_line_count(buf)
  local hls = { value = 'PostheroValue', missing = 'PostheroMissing', note = 'PostheroNote' }
  for _, h in ipairs(hints) do
    if h[1] < count then
      pcall(vim.api.nvim_buf_set_extmark, buf, ns, h[1], 0, {
        virt_text = { { '  ' .. h[2], hls[h[3]] or 'PostheroValue' } },
        virt_text_pos = 'eol',
        hl_mode = 'combine',
      })
    end
  end
end
`

// clipboardLua routes "+ and "* through posthero when Neovim finds no clipboard
// tool, as over ssh: posthero copies with OSC 52 through the terminal, and
// pastes what was last copied in posthero. A g:clipboard of the user's own is
// kept. Function providers need Neovim 0.10.
const clipboardLua = `
local chan = ...
if vim.fn.has('nvim-0.10') == 1 and vim.g.clipboard == nil and vim.fn['provider#clipboard#Executable']() == '' then
  local function copy(lines, regtype) vim.rpcnotify(chan, 'posthero_copy', lines, regtype) end
  local function paste()
    local ok, res = pcall(vim.rpcrequest, chan, 'posthero_paste')
    if ok and type(res) == 'table' then return res end
    return { {}, 'v' }
  end
  vim.g.clipboard = { name = 'posthero', copy = { ['+'] = copy, ['*'] = copy }, paste = { ['+'] = paste, ['*'] = paste } }
  vim.g.loaded_clipboard_provider = nil
  vim.cmd('runtime autoload/provider/clipboard.vim')
end
`

// Start launches nvim.
func Start(opts editor.Options, regs *vim.Registers, w, h int) (*Backend, error) {
	dir, err := os.MkdirTemp("", "posthero-nvim-")
	if err != nil {
		return nil, err
	}
	sock := filepath.Join(dir, "nvim.sock")
	b := &Backend{
		tmpDir:   dir,
		ui:       newScreen(w, h),
		events:   make(chan editor.Event, 16),
		calls:    make(chan func(), 256),
		done:     make(chan struct{}),
		regs:     regs,
		complete: opts.Complete,
	}
	args := []string{"--headless", "--listen", sock, "-n", "--cmd", "let g:posthero = 1"}
	if truecolor() {
		// Neovim's own terminal UI turns 'termguicolors' on in a terminal
		// with true color; a headless Neovim can't tell. A config can still
		// turn it off.
		args = append(args, "--cmd", "set termguicolors")
	}
	// The user's config runs once posthero is attached, as in a terminal:
	// it sees a UI, and what it says at startup shows in the pane, where a
	// "Press ENTER" can be answered.
	args = append(args, "--cmd", "lua "+waitLua)
	if opts.File != "" {
		args = append(args, "--", opts.File)
	}
	b.cmd = exec.Command("nvim", args...)
	// Neovim's stdio is a pseudo terminal so plugins that measure the
	// terminal (image.nvim) work, but it deliberately stays in posthero's
	// session: its controlling terminal is the user's, so tools that look
	// for an nvim process on the tmux pane (vim-tmux-navigator) keep
	// passing ctrl+hjkl.
	var tty *os.File
	b.pty, tty, err = pty.Open()
	if err != nil {
		os.RemoveAll(dir)
		return nil, fmt.Errorf("start nvim: %w", err)
	}
	_ = pty.Setsize(b.pty, &pty.Winsize{Rows: uint16(h), Cols: uint16(w)})
	b.cmd.Stdin, b.cmd.Stdout, b.cmd.Stderr = tty, tty, tty
	err = b.cmd.Start()
	tty.Close()
	if err != nil {
		b.pty.Close()
		os.RemoveAll(dir)
		return nil, fmt.Errorf("start nvim: %w", err)
	}
	go func() { _, _ = io.Copy(io.Discard, b.pty) }()
	served := make(chan struct{})
	go func() {
		err := b.cmd.Wait()
		close(b.done)
		// Everything Neovim sent before it quit (the write of a :wq) is
		// delivered before the exit.
		select {
		case <-served:
		case <-time.After(2 * time.Second):
		}
		early := !b.ready.Load()
		if early && err == nil {
			err = fmt.Errorf("nvim stopped while starting")
		}
		editor.Notify(b.events, editor.Exited{Err: err, Early: early})
	}()

	if b.v, err = dial(sock, b.done); err != nil {
		close(served)
		b.kill()
		return nil, err
	}
	b.ui.onFlush = func() { editor.Notify(b.events, editor.Redraw{}) }
	_ = b.v.RegisterHandler("redraw", b.ui.handle)
	_ = b.v.RegisterHandler("posthero_write", b.onWrite)
	_ = b.v.RegisterHandler("posthero_complete", b.onComplete)
	_ = b.v.RegisterHandler("posthero_send", b.onSend)
	_ = b.v.RegisterHandler("posthero_hover", b.onHover)
	_ = b.v.RegisterHandler("posthero_wincmd", b.onWinCmd)
	_ = b.v.RegisterHandler("posthero_changed", b.onChanged)
	_ = b.v.RegisterHandler("posthero_copy", b.onCopy)
	_ = b.v.RegisterHandler("posthero_paste", b.onPaste)
	go func() {
		_ = b.v.Serve()
		close(served)
	}()

	setup := modalLua
	if opts.Main {
		setup = mainLua
	}
	if opts.Runtime != "" {
		setup = "vim.opt.runtimepath:append(" + luaString(opts.Runtime) + ")\n" + setup
	}
	// posthero keeps its own drafts: no backup files next to its buffers.
	setup += clipboardLua + "vim.opt.shortmess:append('I')\nvim.o.backup = false\nvim.o.writebackup = false\n" + filetypeLua + insertLua(opts.Insert)
	// The buffers are files in a temporary directory: not for :oldfiles.
	setup += "if vim.o.shada ~= '' then vim.opt.shada:append('r' .. " + luaString(filepath.Join(os.TempDir(), "posthero-")) + ") end\n"
	if opts.ReadOnly {
		setup += "vim.bo.modifiable = false vim.bo.readonly = true\n"
	}
	if opts.Main {
		// The first buffer is set up like the ones Open opens.
		setup += "vim.cmd('doautocmd posthero BufEnter')\n"
	}
	// Attaching and setting up wait while Neovim shows a prompt (the
	// user's config may say something at startup), so they don't hold up
	// posthero: they go out in order with everything after them, and keys
	// (which Neovim takes at once) can answer the prompt.
	go b.loop()
	b.call(func() {
		// ext_termcolors: a colorscheme without a background keeps the
		// terminal's.
		if err := b.v.AttachUI(w, h, map[string]any{"rgb": true, "ext_linegrid": true, "ext_termcolors": true}); err != nil {
			b.kill()
		}
	})
	b.call(func() {
		if err := b.v.ExecLua(setup, nil, b.v.ChannelID()); err != nil {
			b.kill()
			return
		}
		b.ready.Store(true)
	})
	return b, nil
}

// waitLua holds Neovim's startup (from --cmd, before the user's config)
// until posthero is attached. A UI that attached that early gets no UIEnter
// from Neovim, and configs count on it (lazy.nvim's VeryLazy), so it fires
// after VimEnter, as it would.
const waitLua = `vim.wait(10000, function() return #vim.api.nvim_list_uis() > 0 end, 5) ` +
	`local entered = false ` +
	`vim.api.nvim_create_autocmd('UIEnter', { callback = function() if vim.v.vim_did_enter == 1 then entered = true end end }) ` +
	`vim.api.nvim_create_autocmd('VimEnter', { once = true, callback = function() vim.schedule(function() ` +
	`if not entered and #vim.api.nvim_list_uis() > 0 then vim.api.nvim_exec_autocmds('UIEnter', { modeline = false }) end ` +
	`end) end })`

// truecolor reports whether the terminal says it shows 24-bit color.
func truecolor() bool {
	c := strings.ToLower(os.Getenv("COLORTERM"))
	return c == "truecolor" || c == "24bit"
}

// insertLua starts insert mode as editor.Options.Insert asks.
func insertLua(mode string) string {
	if mode == "" {
		return ""
	}
	line, cmd := "1", mode
	if n := len(mode); n > 1 {
		line, cmd = mode[:n-1], mode[n-1:]
	}
	switch {
	case mode == "i":
		return "vim.cmd('normal! gg0') vim.cmd('startinsert')\n"
	case mode == "A":
		return "vim.cmd('normal! G$') vim.cmd('startinsert!')\n"
	case cmd == "A":
		return "pcall(vim.api.nvim_win_set_cursor, 0, {" + line + ", 0}) vim.cmd('startinsert!')\n"
	case cmd == "i":
		return "pcall(vim.api.nvim_win_set_cursor, 0, {" + line + ", 0}) vim.cmd('startinsert')\n"
	}
	return ""
}

// dial waits for nvim to create its socket.
func dial(sock string, done chan struct{}) (*nvim.Nvim, error) {
	deadline := time.Now().Add(10 * time.Second)
	for {
		v, err := nvim.Dial(sock, nvim.DialServe(false), nvim.DialLogf(func(string, ...any) {}))
		if err == nil {
			return v, nil
		}
		select {
		case <-done:
			return nil, fmt.Errorf("nvim exited during startup")
		default:
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("connect to nvim: %w", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// loop serialises RPC calls so their order is kept and the UI goroutine
// never waits on Neovim. A call that doesn't come back quickly (Neovim
// waits in a prompt, which only keys can answer) is left to finish on its
// own: the calls after it, keys among them, still go out in order, and
// Neovim takes them in that order once it can.
func (b *Backend) loop() {
	for {
		select {
		case f := <-b.calls:
			done := make(chan struct{})
			go func() {
				f()
				close(done)
			}()
			select {
			case <-done:
			case <-time.After(150 * time.Millisecond):
			case <-b.done:
				return
			}
		case <-b.done:
			return
		}
	}
}

func (b *Backend) call(f func()) {
	select {
	case b.calls <- f:
	case <-b.done:
	}
}

// onCopy takes a yank to "+ or "* (see clipboardLua). The redraw gets the
// app to hand it to the system clipboard and the terminal.
func (b *Backend) onCopy(lines []string, regtype string) {
	if b.regs == nil {
		return
	}
	linewise := regtype == "V"
	if linewise && len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1] // Neovim ends a linewise yank with an empty line
	}
	b.regs.CopyRegister(vim.Register{Text: strings.Join(lines, "\n"), Linewise: linewise})
	editor.Notify(b.events, editor.Redraw{})
}

// onPaste answers a put from "+ or "* with posthero's + register.
func (b *Backend) onPaste() ([]any, error) {
	if b.regs == nil {
		return []any{[]string{}, "v"}, nil
	}
	r := b.regs.Get('+')
	regtype := "v"
	if r.Linewise {
		regtype = "V"
	}
	return []any{strings.Split(r.Text, "\n"), regtype}, nil
}

// onComplete answers the omnifunc: the byte column where the word starts and
// the candidates as complete-items.
func (b *Backend) onComplete(lines []string, row, col int) ([]any, error) {
	return editor.OmniResult(b.complete, lines, row, col), nil
}

func (b *Backend) onWrite(file string, lines []string) {
	editor.Notify(b.events, editor.Written{File: file, Text: strings.Join(lines, "\n")})
}

func (b *Backend) onSend(file string, lines []string, row, col int) {
	editor.Notify(b.events, editor.Send{File: file, Lines: lines, Line: row, Col: col})
}

func (b *Backend) onHover(file string, lines []string, row, col int, key string) {
	editor.Notify(b.events, editor.Hover{File: file, Lines: lines, Line: row, Col: col, Key: key})
}

func (b *Backend) onWinCmd(key string) {
	editor.Notify(b.events, editor.WinCmd{Key: key})
}

func (b *Backend) onChanged(file, text string, modified bool) {
	editor.Notify(b.events, editor.Changed{File: file, Text: text, Modified: modified})
}

// Open implements editor.Backend.
func (b *Backend) Open(file string, text *string) {
	var t any
	if text != nil {
		t = *text
	}
	b.call(func() { _ = b.v.ExecLua("posthero_open(...)", nil, file, t) })
}

// Reload implements editor.Backend.
func (b *Backend) Reload(file string) {
	b.call(func() { _ = b.v.ExecLua("posthero_reload(...)", nil, file) })
}

// SetHints implements editor.Backend.
func (b *Backend) SetHints(file string, hints []editor.Hint) {
	list := make([]any, 0, len(hints))
	for _, h := range hints {
		list = append(list, []any{h.Line, h.Text, h.Kind})
	}
	b.call(func() { _ = b.v.ExecLua("posthero_hints(...)", nil, file, list) })
}

// Goto implements editor.Backend.
func (b *Backend) Goto(line, col int) {
	b.call(func() {
		_ = b.v.ExecLua(`local line, col = ...
line = math.max(1, math.min(line + 1, vim.api.nvim_buf_line_count(0)))
pcall(vim.api.nvim_win_set_cursor, 0, { line, col })
vim.cmd('normal! zz')`, nil, line, col)
	})
}

// luaString quotes a string for Lua.
func luaString(s string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`, "\n", `\n`).Replace(s) + "'"
}

// Name implements editor.Backend.
func (b *Backend) Name() string { return "nvim" }

// Key implements editor.Backend.
func (b *Backend) Key(k tea.KeyPressMsg) {
	in := keys.Nvim(keys.Vim(k))
	if in == " " {
		in = "<Space>"
	}
	b.call(func() { _, _ = b.v.Input(in) })
}

// Paste implements editor.Backend.
func (b *Backend) Paste(text string) {
	b.call(func() { _, _ = b.v.Paste(text, true, -1) })
}

// Mouse implements editor.Backend. Neovim tells double clicks apart itself
// and follows the user's 'mouse' option.
func (b *Backend) Mouse(ev mouse.Event) {
	button := map[tea.MouseButton]string{tea.MouseLeft: "left", tea.MouseRight: "right", tea.MouseMiddle: "middle"}[ev.Button]
	action := map[mouse.Action]string{mouse.Press: "press", mouse.Release: "release", mouse.Drag: "drag"}[ev.Action]
	if ev.Action == mouse.Wheel {
		button = "wheel"
		action = map[tea.MouseButton]string{tea.MouseWheelUp: "up", tea.MouseWheelDown: "down", tea.MouseWheelLeft: "left", tea.MouseWheelRight: "right"}[ev.Button]
	}
	if button == "" || action == "" {
		return
	}
	var mod string
	if ev.Mod.Contains(tea.ModShift) {
		mod += "S-"
	}
	if ev.Mod.Contains(tea.ModCtrl) {
		mod += "C-"
	}
	if ev.Mod.Contains(tea.ModAlt) {
		mod += "A-"
	}
	b.call(func() { _ = b.v.InputMouse(button, action, mod, 0, ev.Y, ev.X) })
}

// Command implements editor.Backend.
func (b *Backend) Command(cmd string) {
	b.call(func() { _ = b.v.Command(cmd) })
}

// Resize implements editor.Backend.
func (b *Backend) Resize(w, h int) {
	if w < 1 || h < 1 {
		return
	}
	_ = pty.Setsize(b.pty, &pty.Winsize{Rows: uint16(h), Cols: uint16(w)})
	b.call(func() { _ = b.v.TryResizeUI(w, h) })
}

// Render implements editor.Backend.
func (b *Backend) Render() []string { return b.ui.render() }

// Cursor implements editor.Backend.
func (b *Backend) Cursor() (int, int, tea.CursorShape, bool) { return b.ui.cursor() }

// Mode implements editor.Backend.
func (b *Backend) Mode() string { return b.ui.modeName() }

// Events implements editor.Backend.
func (b *Backend) Events() <-chan editor.Event { return b.events }

// Close implements editor.Backend.
func (b *Backend) Close() {
	b.once.Do(func() {
		select {
		case <-b.done:
		default:
			// A Neovim waiting in a prompt doesn't run commands: it gets
			// a moment, then it is stopped.
			go func() { _ = b.v.Command("qa!") }()
			select {
			case <-b.done:
			case <-time.After(time.Second):
				_ = b.cmd.Process.Kill()
				select {
				case <-b.done:
				case <-time.After(time.Second):
				}
			}
		}
		_ = b.v.Close()
		_ = os.RemoveAll(b.tmpDir)
	})
}

func (b *Backend) kill() {
	if b.cmd.Process != nil {
		_ = b.cmd.Process.Kill()
	}
	if b.v != nil {
		_ = b.v.Close()
	}
	_ = os.RemoveAll(b.tmpDir)
}

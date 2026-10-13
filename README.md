# posthero

Postman for the terminal, built for people who use vim.

posthero works on your team's Postman workspace as it is: the same collections, folders, environments, auth and scripts your teammates use in the Postman app. Your collections are on the left. The request under the cursor is on the right, as plain text in your own Neovim, and the response is below it. `⏎` sends the request and `:w` saves it to Postman for the whole team.

Logging in happens by itself. When a request needs an OAuth 2.0 token (Keycloak and friends), posthero gets one the way the collection's auth says. It refreshes the token before it runs out, and when the login session is over it logs in again by filling in the login page itself, with the password you gave it once. Only a one-time code or a login page that needs JavaScript asks for you.

If Neovim isn't installed, posthero runs Vim, and if neither is there it uses a built-in vim engine.

## Install

Download a binary for your platform from the [releases page](https://github.com/shinyvision/posthero/releases), unpack it and put `posthero` somewhere on your `PATH`.

Or build it with Go 1.27 or newer:

```sh
go install github.com/shinyvision/posthero/cmd/posthero@latest
```

To look around first, run `posthero --demo`. It runs against a made-up workspace with a small shop API and its own Keycloak, all inside posthero, so nothing you do there reaches a real one. Log in as `ada` / `lovelace` when it asks.

## Getting started

Run `posthero`. The first time, it asks for a Postman API key, which you make in Postman under your avatar → Settings → [API keys](https://go.postman.co/settings/me/api-keys). The key goes in the system keyring (Secret Service, Keychain or Credential Manager), or in posthero's own file, readable only by you, when there is no keyring. `$POSTMAN_API_KEY` works too.

Then pick your team's workspace. posthero remembers it, along with the open request, the folders you opened and the environment of each collection. `posthero -w Backend` opens another workspace by name, and `posthero "Get locations"` opens a request by name, id or Postman link.

Press `?` at any time for every key and command. In the help, `/` searches, and `]]` and `[[` jump between sections. `<Space>` shows every action that works where you are, each with its key.

## The screen

- **The tree** (left): collections, folders and requests. `j` `k` move, `l` and `h` open and close, `za` `zM` `zR` fold, and `/` narrows the tree as you type, to the requests whose name, folder, method or URL has the words you type (or, when none does, their letters in order). The request under the cursor shows in the editor as you move. Requests show their method, and GraphQL ones show `QRY`, `MUT` or `SUB`. After a send, the tree shows the status code. A `+` means you changed the request and haven't saved it.
- **The request** (top right): your editor. `⏎` in normal mode sends the request. Dim notes after the lines show what the variables are and the URL as it goes out.
- **The response** (bottom right): the tests, the console, the headers, what was sent, the timings, and the body, pretty-printed and foldable.

`ctrl+h` `ctrl+j` `ctrl+k` `ctrl+l` move between the panes from anywhere, the editor included. `ctrl+w` then `o` zooms the pane you're in, and `<` `>` `+` `-` `=` resize. `ctrl+p` finds any request in the workspace by a few letters of its name, folder or URL, with the ones you opened lately first.

## The request is text

A request reads like an `.http` file. Only what is set is shown:

```
POST {{baseUrl}}/api/orders/:shop
    ?notify=true
    # &dry=1
    :shop = {{shopId}}
Content-Type: application/json
# X-Debug: 1

{
  "productId": "{{productId}}",
  "quantity": 2
}

### tests
pm.test('created', () => pm.response.to.have.status(201));
pm.collectionVariables.set('orderId', pm.response.json().id);
```

- The first line is the method and the URL. Query parameters go on indented `?` and `&` lines (or stay on the URL line), and path variables on indented `:name = value` lines.
- Headers follow, then a blank line, then the body.
- `#` at the start of a line switches it off, the way the checkboxes do in Postman. A body that is switched off shows its section with `off`, like `### json off`; remove `off` to switch it on.
- A name or value with unusual characters (spaces at either end, `: `, a leading `#`) is written in quotes, like `"X Custom": 1`.
- A body that starts with `query`, `mutation` or `{` is GraphQL or JSON, and `### variables` holds a GraphQL query's variables. Other bodies get a section: `### xml`, `### text`, `### form` (`key: value` lines), `### multipart` (`key: @file` for files), `### file`.
- `### auth TYPE` gives the request its own auth, for example `### auth bearer` followed by `token: {{token}}`. Without it the request inherits the folder's or the collection's auth, as in Postman.
- `### pre-request`, `### tests`, `### settings` and `### description` hold the rest.

Typing `{{` offers the variables. `K` on a variable says what it is and where its value comes from, and `gd` edits it.

In a GraphQL query, `ctrl+x ctrl+o` completes fields, arguments, input fields and enum values from the server's schema, and `K` on a field shows its type, arguments and description. posthero asks the server for its schema by itself, as Postman does. It uses the request's own auth once you're logged in, and it never logs in just for this. On a production environment it waits until you've sent a request there yourself. `:schema` asks again.

`:w` saves the request to Postman. Changes you don't save stay as a draft, also after you quit, until you `:w` them or throw them away with `:e!`. As everywhere in posthero, `:q` quits (asking first when requests have changes Postman doesn't have), and `:wq` (or `:x`, `ZZ`) saves the request, then quits. If a teammate changed the request in Postman after posthero fetched it, `:w` says so and shows the difference before anything is overwritten.

`e` on a folder or collection edits its auth, variables, scripts and description the same way.

## Logging in

posthero does every kind of auth Postman does: OAuth 2.0 (authorization code, with or without PKCE, implicit, client credentials and password), bearer, basic, API key, JWT, digest, NTLM, OAuth 1.0, AWS Signature, Hawk, Akamai EdgeGrid and ASAP.

For OAuth 2.0 with a login page, the first time asks for your user name and password, in posthero. After that:

1. While the identity provider's session lasts, posthero logs in silently with the session cookie.
2. It refreshes tokens before they run out, in the background, as long as you used the login in the last 12 hours (`:set keepalive`).
3. When the session is over, it fills in the login page itself with the saved password, which is kept in the system keyring.
4. When that doesn't work (the password changed, the page asks for a one-time code), it asks you. If the login page needs JavaScript, it opens your browser and catches the redirect back.

Requests that log in to the same identity provider with the same client, scope and user share one token, across collections. `gl` shows the login: who, how long the token lasts, the scope and the roles, with keys to copy the token, refresh it, log in again (as someone else, with `:login USER`) or log out.

## Environments and variables

`E` picks the environment. posthero remembers it per collection, and the first time it guesses from the names (`Shop - Local` for `Shop`, preferring local ones), so switching collections switches environments.

Variables resolve the way Postman does it: local, data, environment, collection, then globals, along with dynamic variables like `{{$guid}}` and `{{$randomEmail}}` and the vault (`{{vault:name}}`).

As in Postman, each variable has your team's value and your own current value:

- Scripts and `gd` change your value, which stays on your machine.
- `gv` (or `:vars`) shows your values next to the team's.
- `:share NAME` gives your value to the team, and `:reset NAME` takes the team's value again.
- `gE` edits the environment's team values, and `:env new NAME`, `copy`, `rename` and `delete` manage environments. `:env copy` without a name copies the environment to a workspace you pick, with your current values.
- Environments belong to a workspace. Copying a collection to another workspace (`yy` on it) offers to take its environments along; without them, a URL like `{{BackendEndpoint}}/graphql` has nothing to fill it in.

When the environment's name says production (`prod`, `prd`, `live`), posthero asks before sending anything but a read: every method except GET, HEAD and OPTIONS, and every GraphQL mutation. `:set noconfirm` turns that off.

## Scripts

Pre-request and test scripts run as they do in Postman, in a JavaScript sandbox with `pm.*`: `pm.test` and `pm.expect` (chai), `pm.response`, the variable scopes, `pm.sendRequest`, `pm.execution.skipRequest` and `pm.execution.setNextRequest` (in runs), `console`, `Buffer` and `xml2Json`. `require` gives `lodash`, `crypto-js`, `moment`, `chai`, `tv4`, `ajv`, `uuid`, `xml2js`, `querystring`, `path`, `events`, `assert`, `atob`, `btoa` and `postman-collection`. `cheerio` isn't there.

The collection's and folders' scripts run around the request's, outermost first. Test results and console output show at the top of the response.

## The response

The response pane is a pager. `j` `k` move, `za` folds JSON objects and arrays, `]]` and `[[` jump between the parts (tests, console, headers, body), and `/` searches.

- `|` filters the body with jq. `esc` shows it whole again.
- `yy` copies the line, `yv` the value under the cursor, `yp` its jq path, `Y` the whole body, and `yh` the headers.
- `K` shows a value in full, and decodes a JWT.
- `o` opens the body in your editor, read only, for searching and folding your way.
- `gx` opens a link.

`gh` shows the request's earlier responses, and `:history` all of them. They are kept on your machine with secrets masked (auth headers, secret variables, the vault); `:history clear` forgets them. `ge` shows the saved examples, and `:example NAME` saves the response as one.

## Running collections

`S` on a folder or collection runs every request in it, in order, the way the collection runner does. `:run -n 5 -d data.csv -delay 200 -bail` runs it 5 times, or once per row of a CSV or JSON data file. `setNextRequest` works, and so do variables that one request's script sets for the next. `⏎` on a row of the run shows that response, and `<BS>` goes back to the list. `ctrl+c` stops a run.

## Changing the workspace

| Key | Does |
| --- | --- |
| `a` | new request here (in the folder under the cursor) |
| `A` | new folder |
| `r` | rename |
| `dd` then `p` | move: cut, go somewhere else, put it below (`P`: above), in another collection too |
| `yy` then `p` | copy; on a collection, `yy` asks which workspace to copy it to (`:dup` copies it next to itself) |
| `D` | delete, after asking |
| `u` | undo your last change: a save, a move, a rename, a delete |
| `yc` | copy the request as a curl command; `gc` for HTTPie, wget, fetch, Python, Go or raw HTTP |
| `gx` | open it in Postman, in the browser |

Pasting a curl command (in the tree or the response pane, or with `:import curl …`) makes a request from it. `:import FILE` imports a collection or environment export, `:export FILE` writes one, and `:collection new NAME` makes a collection.

Everything goes to Postman right away, so your teammates see it in the Postman app.

## Keeping up with your team

posthero asks Postman what changed every 2 minutes (`:set refresh=120`), while you're using it. It pauses while the terminal is in the background or after 10 minutes without a key, and catches up when you come back. `R` asks right away. A request that changed while you had it open updates in the editor, unless you changed it too, in which case your version stays and posthero says so.

These checks cost Postman API calls, which Postman counts per team per month. One check costs two calls, and only collections that changed are fetched again. The workspace is cached on disk, so posthero starts at once and works offline with what it last saw.

## Working locally

`:set local` keeps the workspace on your machine: posthero changes a copy of it instead of Postman. Saves, new requests, moves, renames, deletes, settings and environments all stay local, `u` still undoes them, and a ↑ in the tree marks what isn't in Postman yet. The status bar shows how many changes are waiting.

- `:pull` takes in what your team changed in Postman. What you changed stays yours.
- `:push` sends your changes. If your team changed the same thing, posthero asks about each one: **overwrite** theirs, **show diff**, or **skip** it (it stays local). Changes to different variables of the same environment are merged without asking.
- The diffs you asked for open afterwards, one tab per conflict, Postman's version against yours. `tab` and `shift-tab` move between tabs. `c` (or `:e`) opens both in your editor with git's conflict markers to merge by hand. When you close the editor, the diff shows the merged text. `:w` pushes it. `:q` leaves a conflict local and goes to the next tab (`q` asks first), and `:qq` leaves them all (`Q` asks first).
- `:set nolocal` goes back to working on Postman directly (it asks first if changes haven't been pushed).

Each workspace remembers whether it's local, so one can be local while another isn't.

## Opening exports instead

`posthero shop.postman_collection.json local.postman_environment.json` opens exported files without a Postman account. `:w` then writes the file.

## Options

Options are set with `:set` and kept in `~/.config/posthero/postherorc`, like a vimrc. `:mkpostherorc` writes the ones you changed.

```vim
set refresh=300
set timeout=30
set nomouse
set wrap
map <leader>r :run<CR>
cert api.example.com ~/certs/me.pem ~/certs/me.key
```

| Option | Default | Does |
| --- | --- | --- |
| `refresh` | `120` | seconds between checks for your team's changes, while posthero is in use; 0 is off |
| `timeout` | `0` | seconds before a request gives up; 0 waits forever |
| `verify` | on | check servers' TLS certificates |
| `followredirects` | on | follow redirects |
| `maxresponse` | `50` | the most of a response to keep, in MB |
| `confirm` | on | ask before changing things on a production environment |
| `keepalive` | `12` | hours after its last use a login is kept fresh |
| `history` | `1000` | how many responses to remember; 0 keeps none |
| `showvalues` | on | show variables' values next to them in the editor |
| `mouse` | on | use the mouse |
| `wrap` | off | wrap long lines in the response |
| `proxy` | | a proxy for every request (empty: `$HTTPS_PROXY`) |
| `cafile` | | a PEM file of certificate authorities to trust too |
| `workdir` | | where relative file paths start (empty: where posthero started) |
| `workspace` | | the workspace to open; empty opens the last one |

`map` maps keys to keys or to a command, as in vim. `cert HOST CERT KEY` gives a client certificate to a host.

## Files

On Linux (`?` inside posthero shows where they are on your system):

- `~/.config/posthero/postherorc` holds options and mappings.
- `~/.config/posthero/login.json` holds the API key, or says it's in the keyring.
- `~/.local/state/posthero/` holds drafts, your values of variables, tokens, cookies, saved logins (passwords are in the keyring) and the history.
- `~/.cache/posthero/` holds the workspaces as last fetched.

`POSTHERO_EDITOR=vim` (or `nvim`, or `builtin`) picks the editor.

## License

MIT

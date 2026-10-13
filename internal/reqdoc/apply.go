package reqdoc

import (
	"encoding/json"
	"strings"

	"github.com/shinyvision/posthero/internal/jsonx"
	"github.com/shinyvision/posthero/internal/postman"
)

// Apply reads an edited request document and returns the request item it
// describes: a copy of old with what the document says. What the document
// doesn't show (descriptions of headers and parameters, examples, ids) is
// kept.
func Apply(old *postman.Item, text string) (*postman.Item, error) {
	d, err := parse(text)
	if err != nil {
		return nil, err
	}
	if err := repeated(d.sections); err != nil {
		return nil, err
	}
	it := old.Clone()
	it.Folder = false
	if it.Request == nil {
		it.Request = &postman.Request{}
	}
	req := it.Request
	var oldReq postman.Request
	if old.Request != nil {
		oldReq = *old.Request
	}
	req.Method = d.method
	// A space in a typed URL is most likely a slip, unless the request's
	// URL had one already (Postman allows them).
	if strings.ContainsAny(d.url, " \t") && !strings.Contains(d.url, "{{") && !strings.ContainsAny(oldReq.URL.String(), " \t") {
		return nil, errorf(d.line, "the URL can't have spaces; write them as %%20")
	}

	// The URL, its query and its path variables.
	base, rawQuery, hash := postman.SplitRaw(d.url)
	var query postman.Params
	if d.query != nil {
		if rawQuery != "" {
			for _, q := range postman.ParseQuery(rawQuery) {
				query = append(query, q)
			}
		}
		for _, f := range d.query {
			query = append(query, postman.Param{Key: f.key, Value: postman.Str(f.value), Disabled: f.off, NoValue: f.noValue})
		}
	} else {
		query = postman.ParseQuery(rawQuery)
	}
	raw := base
	if q := postman.QueryString(query.Enabled()); q != "" {
		raw += "?" + q
	}
	if hash != "" {
		raw += "#" + hash
	}
	// The document shows every path variable, empty ones too: one without
	// a line has no value.
	paths := map[string]string{}
	for _, name := range postman.PathVariables(raw) {
		paths[name] = ""
	}
	for _, f := range d.paths {
		if _, ok := paths[f.key]; !ok {
			return nil, errorf(f.line, "the URL has no :%s to fill in", f.key)
		}
		paths[f.key] = f.value
	}
	req.URL = postman.Rebuild(oldReq.URL, raw, query, paths)
	req.URL.Variable = append(req.URL.Variable, unusedVariables(oldReq.URL, raw)...)
	if len(req.URL.Query) == 0 {
		req.URL.Query = nil
	}

	// Headers keep their descriptions.
	req.Header = mergeParams(oldReq.Header, d.headers)

	// The body.
	body, err := d.bodyFor(oldReq.Body)
	if err != nil {
		return nil, err
	}
	req.Body = body

	// Sections.
	var auth *postman.Auth
	authSeen := false
	var pre, tests *string
	var settings json.RawMessage
	settingsSeen := false
	var desc *string
	for _, s := range d.sections {
		switch s.name {
		case secAuth:
			authSeen = true
			if auth, err = parseAuth(s, oldReq.Auth); err != nil {
				return nil, err
			}
		case secPre:
			t := s.text
			pre = &t
		case secTests:
			t := s.text
			tests = &t
		case secSettings:
			settingsSeen = true
			if settings, err = parseSettings(s); err != nil {
				return nil, err
			}
		case secDesc:
			t := s.text
			desc = &t
		}
	}
	req.Auth = auth
	if !authSeen {
		req.Auth = nil // inherit
	}
	it.Event = setScript(it.Event, "prerequest", pre)
	it.Event = setScript(it.Event, "test", tests)
	if settingsSeen {
		it.ProtocolProfileBehavior = postman.RemovedSettings(old.ProtocolProfileBehavior, settings)
	} else {
		it.ProtocolProfileBehavior = postman.RemovedSettings(old.ProtocolProfileBehavior, keepDefaults(old.ProtocolProfileBehavior))
	}
	req.Description = nil
	if old.Request == nil || old.Request.Description == nil {
		it.Description = nil
	}
	if desc != nil && strings.TrimSpace(*desc) != "" {
		d := &postman.Description{Content: *desc}
		if oldReq.Description != nil {
			d.Type, d.Object = oldReq.Description.Type, oldReq.Description.Object
		}
		req.Description = d
	}
	return it, nil
}

// repeated finds a section that comes twice, where only the last one
// would count.
func repeated(sections []sec) error {
	seen := map[string]bool{}
	for _, s := range sections {
		if seen[s.name] {
			return errorf(s.line, "there is a ### %s section already", s.name)
		}
		seen[s.name] = true
	}
	return nil
}

// unusedVariables are the old URL's path variables that neither it nor
// the new URL uses. The document doesn't show them, and they stay.
func unusedVariables(old *postman.URL, raw string) []postman.Variable {
	if old == nil {
		return nil
	}
	used := map[string]bool{}
	for _, name := range append(postman.PathVariables(old.String()), postman.PathVariables(raw)...) {
		used[name] = true
	}
	var out []postman.Variable
	for _, v := range old.Variable {
		if !used[v.Key] {
			out = append(out, v)
		}
	}
	return out
}

// setScript changes a script unless the text is what it was (but for line
// endings and blank space around it).
func setScript(events []postman.Event, listen string, text *string) []postman.Event {
	if trimEnd(postman.Scripts(events, listen)) == deref(text) {
		return events
	}
	return postman.SetScript(events, listen, deref(text))
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// keepDefaults keeps the settings a document doesn't show because they
// are at their defaults; everything else was removed.
func keepDefaults(raw json.RawMessage) json.RawMessage {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil || len(m) == 0 {
		return raw
	}
	out := map[string]json.RawMessage{}
	for k, v := range m {
		var x any
		if json.Unmarshal(v, &x) == nil && isDefaultSetting(k, x) {
			out[k] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	data, _ := jsonx.Marshal(out)
	return data
}

// mergeParams makes parameters from fields, keeping the descriptions (and
// other details) of the old parameters with the same keys, in order.
func mergeParams(old postman.Params, fields []field) postman.Params {
	used := map[int]bool{}
	var out postman.Params
	for _, f := range fields {
		p, _ := take(old, used, f.key)
		p.Key, p.Value, p.Disabled = f.key, postman.Str(f.value), f.off
		out = append(out, p)
	}
	return out
}

// take returns the first old parameter with a key that isn't used yet,
// and marks it used.
func take(old postman.Params, used map[int]bool, key string) (postman.Param, bool) {
	for i, o := range old {
		if !used[i] && o.Key == key {
			used[i] = true
			return o, true
		}
	}
	return postman.Param{}, false
}

// left reports whether an old parameter with a key isn't used yet.
func left(old postman.Params, used map[int]bool, key string) bool {
	for i, o := range old {
		if !used[i] && o.Key == key {
			return true
		}
	}
	return false
}

// bodyFor works out the body from the text after the headers and the body
// sections.
func (d *doc) bodyFor(old *postman.Body) (*postman.Body, error) {
	var b *postman.Body
	var variables *sec
	off := false
	setOnce := func(s sec) error {
		if b != nil {
			return errorf(s.line, "the request has a body already; a request has one body")
		}
		switch strings.ToLower(s.arg) {
		case "":
		case "off":
			off = true
		default:
			return errorf(s.line, "only off may follow ### %s, for a body that isn't sent", s.name)
		}
		return nil
	}
	if d.hasBody {
		switch detectRaw(d.body) {
		case "graphql":
			b = &postman.Body{Mode: "graphql", GraphQL: &postman.GraphQL{Query: postman.Str(d.body)}}
		default:
			lang := detectRaw(d.body)
			// A body that is what it was stays in its language: a text body
			// doesn't turn into JSON because it starts with a brace.
			if old != nil && old.Mode == "raw" && old.Language() != "" && lang == "text" {
				lang = old.Language()
			}
			b = &postman.Body{Mode: "raw", Raw: d.body}
			b.SetLanguage(lang)
		}
	}
	for i := range d.sections {
		s := d.sections[i]
		switch s.name {
		case secVariables:
			variables = &d.sections[i]
		case secGraphQL:
			if err := setOnce(s); err != nil {
				return nil, err
			}
			b = &postman.Body{Mode: "graphql", GraphQL: &postman.GraphQL{Query: postman.Str(s.text)}}
		case "json", "text", "xml", "html", "javascript":
			if err := setOnce(s); err != nil {
				return nil, err
			}
			b = &postman.Body{Mode: "raw", Raw: s.text}
			b.SetLanguage(s.name)
		case secForm, secMultipart:
			if err := setOnce(s); err != nil {
				return nil, err
			}
			fs, err := fieldLines(s.text, s.first, s.name == secMultipart)
			if err != nil {
				return nil, err
			}
			if s.name == secForm {
				var old0 postman.Params
				if old != nil {
					old0 = old.URLEncoded
				}
				b = &postman.Body{Mode: "urlencoded", URLEncoded: mergeParams(old0, fs)}
				for _, f := range fs {
					if f.file {
						return nil, errorf(f.line, "files go in a ### multipart body")
					}
				}
			} else {
				var old0 postman.Params
				if old != nil {
					old0 = old.FormData
				}
				b = &postman.Body{Mode: "formdata", FormData: formData(old0, fs)}
			}
		case secFile:
			if err := setOnce(s); err != nil {
				return nil, err
			}
			src := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s.text), "@"))
			b = &postman.Body{Mode: "file", File: fileBody(old, src)}
		}
	}
	if variables != nil {
		if b == nil || b.Mode != "graphql" {
			return nil, errorf(variables.line, "### variables are for a GraphQL query; this request's body isn't one")
		}
		b.GraphQL.Variables = postman.Str(variables.text)
	}
	if b == nil {
		if old != nil && old.Mode == "raw" && strings.TrimSpace(old.Raw) == "" {
			return old, nil // an empty body shows as nothing; it stays as it was
		}
		return nil, nil
	}
	// What the document doesn't say stays as it was, and text that only
	// lost the blank space it ended with is the old text.
	if old != nil {
		switch {
		case b.Mode == "raw" && old.Mode == "raw" && trimEnd(old.Raw) == b.Raw:
			b.Raw = old.Raw
		case b.Mode == "graphql" && old.Mode == "graphql" && old.GraphQL != nil:
			if trimEnd(string(old.GraphQL.Query)) == string(b.GraphQL.Query) {
				b.GraphQL.Query = old.GraphQL.Query
			}
			if trimEnd(string(old.GraphQL.Variables)) == string(b.GraphQL.Variables) {
				b.GraphQL.Variables = old.GraphQL.Variables
			}
		}
	}
	b.Disabled = off
	if old != nil {
		b.Extra = old.Extra
		if old.Mode == b.Mode {
			if b.Mode == "raw" {
				lang := b.Language()
				b.Options = old.Options
				// text stands for no language too, and for one posthero
				// doesn't know (yaml); those stay.
				if old.Language() != lang && !(lang == "text" && rawLanguages[old.Language()] == "") {
					b.SetLanguage(lang)
				}
			} else if len(old.Options) > 0 {
				b.Options = old.Options
			}
			if b.Mode == "graphql" && old.GraphQL != nil {
				b.GraphQL.Extra = old.GraphQL.Extra
			}
		}
	}
	return b, nil
}

// fileBody is a binary body's file: the old one's fields with the new
// path, and its content only when the path is the same.
func fileBody(old *postman.Body, src string) *postman.BodyFile {
	if old == nil || old.Mode != "file" || old.File == nil {
		return &postman.BodyFile{Src: postman.Str(src)}
	}
	f := *old.File
	if string(f.Src) != src {
		f.Src, f.Content = postman.Str(src), ""
	}
	return &f
}

// formData makes form-data fields. A line takes the old field with its
// key, in order. File lines with the same key in a row are one field with
// several files, unless they are fields of their own: a field that had one
// file takes one line, and a field with several files takes as many lines
// as it had, or all of them when no other field with the key is left.
func formData(old postman.Params, fs []field) postman.Params {
	used := map[int]bool{}
	var out postman.Params
	for i := 0; i < len(fs); i++ {
		f := fs[i]
		p, had := take(old, used, f.key)
		p.Key, p.Disabled, p.ContentType = f.key, f.off, f.contentType
		if !f.file {
			p.Type, p.Value, p.Src = "text", postman.Str(f.value), nil
			out = append(out, p)
			continue
		}
		many := isList(p.Src)
		files := []string{f.value}
		if !had || many {
			// The next lines are more files: as many as the field had, or
			// all of them when no other field with the key is left.
			for i+1 < len(fs) && sameField(fs[i+1], f) && (len(files) < len(p.Files()) || !left(old, used, f.key)) {
				i++
				files = append(files, fs[i].value)
			}
		}
		p.Type, p.Value, p.Src = "file", "", fileSrc(files, many)
		out = append(out, p)
	}
	return out
}

// sameField reports whether a file line can be another file of a field.
func sameField(f, of field) bool {
	return f.file && f.key == of.key && f.off == of.off && f.contentType == of.contentType
}

// isList reports whether a file field's src is a list of files.
func isList(raw json.RawMessage) bool {
	t := strings.TrimSpace(string(raw))
	return strings.HasPrefix(t, "[")
}

// fileSrc writes a form-data field's files: one as a string, several (or
// any number, for a field that had a list) as a list.
func fileSrc(files []string, list bool) json.RawMessage {
	if len(files) == 1 && files[0] == "" {
		files = []string{}
	}
	if !list && len(files) <= 1 {
		if len(files) == 0 {
			return nil
		}
		data, _ := jsonx.Marshal(files[0])
		return data
	}
	data, _ := jsonx.Marshal(files)
	return data
}

// parseAuth reads a ### auth TYPE section. Settings keep the JSON type
// they had (a boolean stays a boolean).
func parseAuth(s sec, old *postman.Auth) (*postman.Auth, error) {
	typ := strings.ToLower(strings.TrimSpace(s.arg))
	switch typ {
	case "":
		return nil, errorf(s.line, "say which auth: ### auth bearer, basic, apikey, oauth2, digest, oauth1, awsv4, hawk, ntlm, akamai, jwt, asap, none or inherit")
	case "none", "noauth":
		return &postman.Auth{Type: "noauth", Params: otherTypes(old, "noauth")}, nil
	case "inherit":
		if strings.TrimSpace(s.text) != "" {
			return nil, errorf(s.line, "an inherited auth has no settings of its own")
		}
		return nil, nil
	case "akamai", "edgegrid":
		typ = "edgegrid"
	}
	if !knownAuth[typ] {
		return nil, errorf(s.line, "posthero doesn't know auth %q; it knows bearer, basic, apikey, oauth2, digest, oauth1, awsv4, hawk, ntlm, akamai, jwt and asap", typ)
	}
	a := &postman.Auth{Type: typ, Params: otherTypes(old, typ)}
	oldParams := map[string]postman.AuthParam{}
	if old != nil {
		for _, p := range old.Params[typ] {
			if _, ok := oldParams[p.Key]; !ok {
				oldParams[p.Key] = p
			}
		}
	}
	fs, err := fieldLines(s.text, s.first, false)
	if err != nil {
		return nil, err
	}
	var list []postman.AuthParam
	for _, f := range fs {
		if f.off {
			continue
		}
		list = append(list, authParam(f, oldParams))
	}
	a.Params[typ] = list
	return a, nil
}

// otherTypes copies the settings of the auth types other than typ, which
// stay, as Postman keeps them.
func otherTypes(old *postman.Auth, typ string) map[string][]postman.AuthParam {
	out := map[string][]postman.AuthParam{}
	if old != nil {
		for k, list := range old.Params {
			if k != typ {
				out[k] = list
			}
		}
	}
	return out
}

// authParam makes a setting from its line. One that reads as it did stays
// as it was; a changed one keeps its JSON type (text stays text, a
// boolean a boolean).
func authParam(f field, old map[string]postman.AuthParam) postman.AuthParam {
	o, had := old[f.key]
	if !had {
		return postman.AuthParam{Key: f.key, Value: authJSON(f.value, ""), Type: authParamType(f.value, "")}
	}
	if authText(o.Value) == f.value {
		return o
	}
	kind := o.Type
	if kind == "" {
		kind = jsonKind(o.Value)
	}
	return postman.AuthParam{Key: f.key, Value: authJSON(f.value, kind), Type: o.Type}
}

// jsonKind is the type of a JSON value as Postman names it, or "" for
// none.
func jsonKind(raw json.RawMessage) string {
	s := strings.TrimSpace(string(raw))
	switch {
	case s == "" || s == "null":
		return ""
	case s[0] == '"':
		return "string"
	case s == "true" || s == "false":
		return "boolean"
	case s[0] == '[' || s[0] == '{':
		return "any"
	}
	return "number"
}

var knownAuth = map[string]bool{
	"bearer": true, "basic": true, "apikey": true, "oauth2": true, "digest": true, "oauth1": true, "awsv4": true,
	"hawk": true, "ntlm": true, "edgegrid": true, "jwt": true, "asap": true,
}

// authJSON turns a written value back into JSON: booleans, numbers, lists
// and objects as themselves when that is what they look like (or were),
// text as a string.
func authJSON(v, oldType string) json.RawMessage {
	t := strings.TrimSpace(v)
	if oldType != "string" && t != "" {
		var x any
		if json.Unmarshal([]byte(t), &x) == nil {
			switch x.(type) {
			case bool, []any, map[string]any:
				return json.RawMessage(t)
			case float64:
				if oldType == "number" {
					return json.RawMessage(t)
				}
			}
		}
	}
	data, _ := jsonx.Marshal(v)
	return data
}

func authParamType(v, oldType string) string {
	if oldType != "" {
		return oldType
	}
	switch raw := authJSON(v, ""); {
	case raw[0] == 't' || raw[0] == 'f':
		return "boolean"
	case raw[0] == '[' || raw[0] == '{':
		return "any"
	}
	return "string"
}

// parseSettings reads key: JSON lines into request settings.
func parseSettings(s sec) (json.RawMessage, error) {
	fs, err := fieldLines(s.text, s.first, false)
	if err != nil {
		return nil, err
	}
	m := map[string]json.RawMessage{}
	for _, f := range fs {
		if f.off {
			continue
		}
		var x any
		if err := json.Unmarshal([]byte(f.raw), &x); err != nil {
			// A plain word is text.
			data, _ := jsonx.Marshal(f.value)
			m[f.key] = data
			continue
		}
		m[f.key] = json.RawMessage(f.raw)
	}
	if len(m) == 0 {
		return nil, nil
	}
	data, _ := jsonx.Marshal(m)
	return data, nil
}

// ApplyFolder reads an edited folder document into a copy of the folder.
func ApplyFolder(old *postman.Item, text string) (*postman.Item, error) {
	_, sections, err := splitSections(text)
	if err != nil {
		return nil, err
	}
	it := old.Clone()
	it.Folder = true
	auth, events, desc, _, err := ownSettings(sections, old.Auth, it.Event, false)
	if err != nil {
		return nil, err
	}
	it.Auth, it.Event = auth, events
	it.Description = describe(old.Description, desc)
	return it, nil
}

// ApplyCollection reads an edited collection document and returns the
// collection's new auth, scripts, variables and description (the items
// are not copied).
func ApplyCollection(old *postman.Collection, text string) (*postman.Collection, error) {
	_, sections, err := splitSections(text)
	if err != nil {
		return nil, err
	}
	c := &postman.Collection{Info: old.Info, Item: old.Item, ProtocolProfileBehavior: old.ProtocolProfileBehavior, Extra: old.Extra}
	auth, events, desc, variables, err := ownSettings(sections, old.Auth, append([]postman.Event(nil), old.Event...), true)
	if err != nil {
		return nil, err
	}
	c.Auth, c.Event = auth, events
	c.Info.Description = describe(old.Info.Description, desc)
	if variables != nil {
		// Lines take the old variables with their keys, in order.
		used := map[int]bool{}
		for _, f := range variables {
			v := postman.Variable{Key: f.key}
			for i, o := range old.Variable {
				if !used[i] && o.Key == f.key {
					v, used[i] = o, true
					break
				}
			}
			v.Value, v.Disabled = postman.Str(f.value), f.off
			c.Variable = append(c.Variable, v)
		}
	}
	return c, nil
}

func describe(old *postman.Description, text *string) *postman.Description {
	if text == nil || strings.TrimSpace(*text) == "" {
		return nil
	}
	d := &postman.Description{Content: *text}
	if old != nil {
		d.Type, d.Object = old.Type, old.Object
	}
	return d
}

// ownSettings reads the sections a folder or collection has.
func ownSettings(sections []sec, oldAuth *postman.Auth, events []postman.Event, collection bool) (*postman.Auth, []postman.Event, *string, []field, error) {
	var auth *postman.Auth
	var pre, tests, desc *string
	var variables []field
	var err error
	if err = repeated(sections); err != nil {
		return nil, nil, nil, nil, err
	}
	for _, s := range sections {
		switch s.name {
		case secAuth:
			if auth, err = parseAuth(s, oldAuth); err != nil {
				return nil, nil, nil, nil, err
			}
		case secPre:
			t := s.text
			pre = &t
		case secTests:
			t := s.text
			tests = &t
		case secDesc:
			t := s.text
			desc = &t
		case secVariables:
			if !collection {
				return nil, nil, nil, nil, errorf(s.line, "folders have no variables of their own; the collection does")
			}
			if variables, err = fieldLines(s.text, s.first, false); err != nil {
				return nil, nil, nil, nil, err
			}
			if variables == nil {
				variables = []field{}
			}
		default:
			what := "a folder"
			if collection {
				what = "the collection"
			}
			return nil, nil, nil, nil, errorf(s.line, "### %s isn't something %s has; it has auth, pre-request, tests and description", s.name, what)
		}
	}
	events = setScript(events, "prerequest", pre)
	events = setScript(events, "test", tests)
	return auth, events, desc, variables, nil
}

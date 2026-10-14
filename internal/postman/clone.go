package postman

import "slices"

// Clones preserve all modeled and unknown fields without a JSON round trip.
// Every mutable slice, map and pointer is independent, including raw JSON.
func (c *Collection) Clone() *Collection {
	if c == nil {
		return nil
	}
	out := *c
	out.Info.Description = cloneDescription(c.Info.Description)
	out.Info.Extra = cloneExtra(c.Info.Extra)
	out.Item = cloneItems(c.Item)
	out.Event = cloneEvents(c.Event)
	out.Variable = cloneVariables(c.Variable)
	out.Auth = c.Auth.Clone()
	out.ProtocolProfileBehavior = slices.Clone(c.ProtocolProfileBehavior)
	out.Extra = cloneExtra(c.Extra)
	return &out
}

func cloneItems(items []*Item) []*Item {
	out := slices.Clone(items)
	for i, it := range items {
		out[i] = it.Clone()
	}
	return out
}

func (it *Item) Clone() *Item {
	if it == nil {
		return nil
	}
	out := *it
	out.Description = cloneDescription(it.Description)
	out.Item = cloneItems(it.Item)
	out.Request = cloneRequest(it.Request)
	out.Response = slices.Clone(it.Response)
	for i, r := range it.Response {
		out.Response[i] = r.Clone()
	}
	out.Event = cloneEvents(it.Event)
	out.Auth = it.Auth.Clone()
	out.Variable = cloneVariables(it.Variable)
	out.ProtocolProfileBehavior = slices.Clone(it.ProtocolProfileBehavior)
	out.Extra = cloneExtra(it.Extra)
	return &out
}

func (r *Response) Clone() *Response {
	if r == nil {
		return nil
	}
	out := *r
	out.OriginalRequest = cloneRequest(r.OriginalRequest)
	out.Header = cloneParams(r.Header)
	out.Cookie = slices.Clone(r.Cookie)
	out.Extra = cloneExtra(r.Extra)
	return &out
}

func (a *Auth) Clone() *Auth {
	if a == nil {
		return nil
	}
	out := *a
	if a.Params != nil {
		out.Params = make(map[string][]AuthParam, len(a.Params))
		for k, list := range a.Params {
			cp := slices.Clone(list)
			for i, p := range list {
				cp[i].Value = slices.Clone(p.Value)
			}
			out.Params[k] = cp
		}
	}
	return &out
}

func cloneRequest(r *Request) *Request {
	if r == nil {
		return nil
	}
	out := *r
	out.URL = cloneURL(r.URL)
	out.Header = cloneParams(r.Header)
	out.Body = cloneBody(r.Body)
	out.Auth = r.Auth.Clone()
	out.Description = cloneDescription(r.Description)
	out.Extra = cloneExtra(r.Extra)
	return &out
}

func cloneURL(u *URL) *URL {
	if u == nil {
		return nil
	}
	out := *u
	out.Host = slices.Clone(u.Host)
	out.Path = slices.Clone(u.Path)
	out.Query = cloneParams(u.Query)
	out.Variable = cloneVariables(u.Variable)
	out.Extra = cloneExtra(u.Extra)
	return &out
}

func cloneBody(b *Body) *Body {
	if b == nil {
		return nil
	}
	out := *b
	out.URLEncoded = cloneParams(b.URLEncoded)
	out.FormData = cloneParams(b.FormData)
	if b.File != nil {
		cp := *b.File
		cp.Extra = cloneExtra(b.File.Extra)
		out.File = &cp
	}
	if b.GraphQL != nil {
		cp := *b.GraphQL
		cp.Extra = cloneExtra(b.GraphQL.Extra)
		out.GraphQL = &cp
	}
	out.Options = slices.Clone(b.Options)
	out.Extra = cloneExtra(b.Extra)
	return &out
}

func cloneParams(params Params) Params {
	out := slices.Clone(params)
	for i, p := range params {
		out[i].Description = cloneDescription(p.Description)
		out[i].Src = slices.Clone(p.Src)
		out[i].Extra = cloneExtra(p.Extra)
	}
	return out
}

func cloneEvents(events []Event) []Event {
	out := slices.Clone(events)
	for i, e := range events {
		out[i].Extra = cloneExtra(e.Extra)
		out[i].Script.Exec = slices.Clone(e.Script.Exec)
		out[i].Script.Extra = cloneExtra(e.Script.Extra)
	}
	return out
}

func cloneVariables(vars []Variable) []Variable {
	out := slices.Clone(vars)
	for i, v := range vars {
		out[i].Description = cloneDescription(v.Description)
		out[i].Extra = cloneExtra(v.Extra)
	}
	return out
}

func cloneExtra(ex Extra) Extra {
	if ex == nil {
		return nil
	}
	out := make(Extra, len(ex))
	for k, v := range ex {
		out[k] = slices.Clone(v)
	}
	return out
}

func cloneDescription(d *Description) *Description {
	if d == nil {
		return nil
	}
	out := *d
	return &out
}

func (e *Environment) Clone() *Environment {
	if e == nil {
		return nil
	}
	out := *e
	out.Extra = cloneExtra(e.Extra)
	out.Values = slices.Clone(e.Values)
	for i, v := range e.Values {
		out.Values[i].Extra = cloneExtra(v.Extra)
		if v.Enabled != nil {
			on := *v.Enabled
			out.Values[i].Enabled = &on
		}
	}
	return &out
}

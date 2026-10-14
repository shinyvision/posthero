package postman

// ItemPatch changes only the supplied properties of a request or folder.
// A non-nil Auth or Event pointer can explicitly clear that property.
type ItemPatch struct {
	Name, Description *string
	Auth              **Auth
	Event             *[]Event
}

// Fields uses the item endpoints' format; omitted properties stay untouched.
func (p ItemPatch) Fields() map[string]any {
	out := map[string]any{}
	if p.Name != nil {
		out["name"] = *p.Name
	}
	if p.Description != nil {
		out["description"] = *p.Description
	}
	if p.Auth != nil {
		out["auth"] = authItem(*p.Auth)
	}
	if p.Event != nil {
		out["events"] = eventItems(*p.Event)
	}
	return out
}

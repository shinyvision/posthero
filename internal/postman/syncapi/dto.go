package syncapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/shinyvision/posthero/internal/postman"
)

// The sync service keeps collections the way Postman's apps do inside: flat.
// A collection holds its folders and requests in lists of their own, and
// says their order in id lists (order for requests, folders_order for
// folders); a request has its examples beside it the same way. A request is
// in the older format that the Postman API's item endpoints speak too (a
// url string, headerData, dataMode). That doesn't line up with posthero's
// collections (format v2.1, a tree), so these types are what the service
// sends and takes, and convert.go turns them into posthero's and back.
//
// What lines up is used as it is: auth, scripts (events) and variables are
// the same objects in both, and so are environments and globals (see
// client.go).

// collectionDTO is a collection: its own settings and, when fetched with
// populate, its folders and requests.
type collectionDTO struct {
	ID            string               `json:"id"`
	Name          string               `json:"name"`
	Owner         string               `json:"owner,omitempty"`
	LastUpdatedBy string               `json:"lastUpdatedBy,omitempty"`
	Description   *postman.Description `json:"description"`
	Variables     []postman.Variable   `json:"variables"`
	Auth          *postman.Auth        `json:"auth"`
	Events        []postman.Event      `json:"events"`
	Order         []string             `json:"order"`
	FoldersOrder  []string             `json:"folders_order"`
	Folders       []*folderDTO         `json:"folders,omitempty"`
	Requests      []*requestDTO        `json:"requests,omitempty"`
	CreatedAt     string               `json:"createdAt,omitempty"`
	UpdatedAt     string               `json:"updatedAt,omitempty"`
	Attributes    *collectionAttrsDTO  `json:"attributes,omitempty"`
}

// collectionAttrsDTO is what a collection list says about each collection
// besides its tree: where it is.
type collectionAttrsDTO struct {
	Parent struct {
		Type string `json:"type"`
		ID   string `json:"id"`
	} `json:"parent"`
}

// folderDTO is a folder: its settings and the order of what is in it.
type folderDTO struct {
	ID            string               `json:"id"`
	Name          string               `json:"name"`
	Owner         string               `json:"owner,omitempty"`
	LastUpdatedBy string               `json:"lastUpdatedBy,omitempty"`
	Collection    string               `json:"collection,omitempty"`
	Folder        *string              `json:"folder"`
	Description   *postman.Description `json:"description"`
	Variables     []postman.Variable   `json:"variables"`
	Auth          *postman.Auth        `json:"auth"`
	Events        []postman.Event      `json:"events"`
	Order         []string             `json:"order"`
	FoldersOrder  []string             `json:"folders_order"`
	CreatedAt     string               `json:"createdAt,omitempty"`
	UpdatedAt     string               `json:"updatedAt,omitempty"`
}

// requestDTO is a request in the older format.
type requestDTO struct {
	ID               string     `json:"id"`
	Name             string     `json:"name"`
	Owner            string     `json:"owner,omitempty"`
	LastUpdatedBy    string     `json:"lastUpdatedBy,omitempty"`
	Collection       string     `json:"collection,omitempty"`
	Folder           *string    `json:"folder"`
	Method           string     `json:"method"`
	URL              string     `json:"url"`
	QueryParams      []paramDTO `json:"queryParams"`
	PathVariableData []paramDTO `json:"pathVariableData"`
	// PathVariables is the older form of PathVariableData: name to value.
	PathVariables json.RawMessage `json:"pathVariables"`
	HeaderData    []paramDTO      `json:"headerData"`
	DataMode      *string         `json:"dataMode"`
	// Data is the body's fields (a list); in older requests also the raw
	// body (a string) or a GraphQL body (an object).
	Data            json.RawMessage      `json:"data"`
	RawModeData     *string              `json:"rawModeData"`
	GraphqlModeData *graphqlDTO          `json:"graphqlModeData"`
	DataOptions     json.RawMessage      `json:"dataOptions"`
	DataDisabled    *bool                `json:"dataDisabled"`
	Auth            *postman.Auth        `json:"auth"`
	Events          []postman.Event      `json:"events"`
	Description     *postman.Description `json:"description"`
	Variables       []postman.Variable   `json:"variables"`
	Settings        json.RawMessage      `json:"protocolProfileBehavior"`
	ResponsesOrder  []string             `json:"responses_order,omitempty"`
	Responses       []*responseDTO       `json:"responses,omitempty"`
	CreatedAt       string               `json:"createdAt,omitempty"`
	UpdatedAt       string               `json:"updatedAt,omitempty"`
}

// paramDTO is a header, query parameter, path variable or body field.
// Fields it doesn't name are kept in Extra: the Postman API passes them on
// (uuid, warnings about files), and so does posthero.
type paramDTO struct {
	Key         string               `json:"key"`
	Value       json.RawMessage      `json:"value"`
	Enabled     *bool                `json:"enabled,omitempty"`
	Description *postman.Description `json:"description,omitempty"`
	Type        string               `json:"type,omitempty"`
	Src         json.RawMessage      `json:"src,omitempty"`
	ContentType string               `json:"contentType,omitempty"`
	// Equals says whether a query parameter is written with = (key=value)
	// or without (key).
	Equals *bool         `json:"equals,omitempty"`
	Extra  postman.Extra `json:"-"`
}

// UnmarshalJSON implements json.Unmarshaler.
func (p *paramDTO) UnmarshalJSON(data []byte) error {
	type plain paramDTO
	var q plain
	if err := decode(data, &q); err != nil {
		return err
	}
	*p = paramDTO(q)
	var all map[string]json.RawMessage
	if json.Unmarshal(data, &all) != nil {
		return nil
	}
	for k, raw := range all {
		switch k {
		case "key", "value", "enabled", "description", "type", "src", "contentType", "equals":
			continue
		}
		if p.Extra == nil {
			p.Extra = postman.Extra{}
		}
		p.Extra[k] = raw
	}
	return nil
}

// MarshalJSON implements json.Marshaler: the named fields and the kept
// ones.
func (p paramDTO) MarshalJSON() ([]byte, error) {
	type plain paramDTO
	return marshalKeeping(plain(p), p.Extra)
}

// graphqlDTO is a GraphQL body.
type graphqlDTO struct {
	Query     postman.Str `json:"query"`
	Variables postman.Str `json:"variables"`
}

// responseDTO is a saved example. Its request is a requestDTO written as
// JSON text.
type responseDTO struct {
	ID            string          `json:"id"`
	Name          string          `json:"name"`
	Owner         string          `json:"owner,omitempty"`
	Request       string          `json:"request,omitempty"`
	Status        *string         `json:"status"`
	ResponseCode  *responseCode   `json:"responseCode"`
	Headers       []paramDTO      `json:"headers"`
	Cookies       json.RawMessage `json:"cookies"`
	Text          *string         `json:"text"`
	Language      *string         `json:"language"`
	RequestObject json.RawMessage `json:"requestObject"`
	CreatedAt     string          `json:"createdAt,omitempty"`
	UpdatedAt     string          `json:"updatedAt,omitempty"`
}

type responseCode struct {
	Code   int    `json:"code"`
	Name   string `json:"name"`
	Detail string `json:"detail,omitempty"`
}

// UnmarshalJSON implements json.Unmarshaler. Some examples have "" or a
// bare number for their code.
func (c *responseCode) UnmarshalJSON(data []byte) error {
	*c = responseCode{}
	data = bytes.TrimSpace(data)
	switch {
	case len(data) == 0:
	case data[0] == '{':
		var o struct {
			Code   json.Number `json:"code"`
			Name   postman.Str `json:"name"`
			Detail postman.Str `json:"detail"`
		}
		if err := json.Unmarshal(data, &o); err != nil {
			return nil
		}
		n, _ := o.Code.Int64()
		*c = responseCode{Code: int(n), Name: string(o.Name), Detail: string(o.Detail)}
	default:
		n, _ := strconv.Atoi(strings.Trim(string(data), `"`))
		c.Code = n
	}
	return nil
}

// decode reads what the service sent into v. A value of the wrong type
// (Postman's data has a few) leaves its field empty instead of failing the
// whole collection, as posthero's own reading does.
func decode(data []byte, v any) error {
	err := json.Unmarshal(data, v)
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) {
		return nil
	}
	return err
}

// workspaceDTO is a workspace as the service lists it.
type workspaceDTO struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	Type             string `json:"type"`
	VisibilityStatus string `json:"visibilityStatus"`
}

// globalsDTO is a workspace's globals.
type globalsDTO struct {
	ID        string             `json:"id"`
	Workspace string             `json:"workspace"`
	Values    []postman.EnvValue `json:"values"`
}

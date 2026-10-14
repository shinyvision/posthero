package postman

import "context"

// API is a way to Postman: what posthero reads and changes there. Two
// ways implement it: the Postman API with an API key (Client), and the
// sync service Postman's own apps use, with a login (package syncapi).
// Both answer the same, down to the fields of what they return; the tests
// in api_test.go hold every implementation to that, against fakes and
// (when asked) against Postman itself.
//
// Collections and environments go by uid (the owner's id, a dash and the
// id). Items (requests, folders and examples) go by what Ref says. Errors
// from Postman are *Error, so IsUnauthorized and IsNotFound work on them;
// a collection that isn't there is a 404 whose message says "collection".
type API interface {
	// Me is who is signed in.
	Me(ctx context.Context) (User, error)
	// Workspaces lists the workspaces the user can see.
	Workspaces(ctx context.Context) ([]Workspace, error)

	// Collections lists a workspace's collections. UpdatedAt changes when
	// the collection itself changes, not what is in it.
	Collections(ctx context.Context, workspace string) ([]Summary, error)
	// Collection fetches a whole collection, its examples too.
	Collection(ctx context.Context, uid string) (*Collection, error)
	// CreateCollection makes a collection (and what is in it, with ids of
	// Postman's) in a workspace. Only the id, uid and name of the summary
	// are set. It sets the collection's schema when it has none.
	CreateCollection(ctx context.Context, workspace string, col *Collection) (Summary, error)
	DeleteCollection(ctx context.Context, uid string) error
	// PatchCollection changes the collection's own settings: what p sets.
	PatchCollection(ctx context.Context, collection string, p CollectionPatch) error

	// CreateRequest adds a request to a collection, in a folder when folder
	// is not empty, after what is there.
	CreateRequest(ctx context.Context, collection, folder string, it *Item) (Made, error)
	// UpdateRequest saves a request and returns when it was updated.
	UpdateRequest(ctx context.Context, collection string, it *Item) (string, error)
	// PatchRequest changes only the supplied properties, returning updatedAt.
	PatchRequest(ctx context.Context, collection, ref string, p ItemPatch) (string, error)
	DeleteRequest(ctx context.Context, collection, ref string) error
	// RequestInfo fetches when a request last changed and by whom.
	RequestInfo(ctx context.Context, collection, ref string) (RequestRevision, error)
	// CreateFolder adds a folder, inside parent when it is not empty, after
	// the folders there.
	CreateFolder(ctx context.Context, collection, parent string, it *Item) (Made, error)
	// UpdateFolder saves a folder's own settings: name, description, auth
	// and scripts.
	UpdateFolder(ctx context.Context, collection string, it *Item) error
	PatchFolder(ctx context.Context, collection, ref string, p ItemPatch) error
	// DeleteFolder deletes a folder and everything in it.
	DeleteFolder(ctx context.Context, collection, ref string) error
	CreateResponse(ctx context.Context, collection, request string, r *Response) (Made, error)
	UpdateResponse(ctx context.Context, collection string, r *Response) error
	// RenameResponse changes only the example's name.
	RenameResponse(ctx context.Context, collection, ref, name string) error
	DeleteResponse(ctx context.Context, collection, ref string) error
	// Transfer moves (or with copy set, copies) requests or folders to a
	// place and returns the uids of what is there now: the copies, named
	// with " Copy" after, when copying.
	Transfer(ctx context.Context, folders bool, uids []string, to Place, copy bool) ([]string, error)

	Environments(ctx context.Context, workspace string) ([]Summary, error)
	Environment(ctx context.Context, uid string) (*Environment, error)
	// PutEnvironment replaces an environment's name and variables.
	PutEnvironment(ctx context.Context, uid string, e *Environment) error
	// CreateEnvironment makes an environment; only the id, uid and name of
	// the summary are set.
	CreateEnvironment(ctx context.Context, workspace string, e *Environment) (Summary, error)
	DeleteEnvironment(ctx context.Context, uid string) error
	Globals(ctx context.Context, workspace string) ([]EnvValue, error)
	PutGlobals(ctx context.Context, workspace string, values []EnvValue) error
}

// Watcher is an API that tells about changes as they happen, so they
// needn't be asked for over and over.
type Watcher interface {
	// Watch tells f (on a goroutine of its own) about each change to a
	// workspace's collections and environments until ctx ends. It returns
	// once it watches.
	Watch(ctx context.Context, workspace string, f func(Change)) error
}

// Change is something that changed in a workspace.
type Change struct {
	// Kind is "collection" or "environment".
	Kind string
	// UID is the collection or environment's uid.
	UID string
	// Action is "create", "update", "delete", or "reconnect" when a
	// subscription was restored and changes may have been missed.
	Action string
}

var _ API = (*Client)(nil)

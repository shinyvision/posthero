package syncapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/shinyvision/posthero/internal/postman"
)

const nothing = "00000000-0000-4000-8000-000000000000"
const maxPendingChanges = 1024

type subscription struct{ workspace, kind string }

type watch struct {
	ws      string
	f       func(postman.Change)
	mu      sync.Mutex
	pending map[string]postman.Change
	order   []string
	catchup bool
	wake    chan struct{}
	done    <-chan struct{}
}

// Watch shares server subscriptions with other consumers of the workspace.
// The callback must return promptly; a slow consumer cannot block the socket.
// If its bounded queue overflows, a catch-up replaces the queued changes.
func (c *Client) Watch(parent context.Context, workspace string, f func(postman.Change)) error {
	if workspace == "" || f == nil {
		return errors.New("a workspace and callback are required to watch Postman")
	}
	ctx, cancel := context.WithCancel(parent)
	stop := context.AfterFunc(c.ctx, cancel)
	w := &watch{ws: workspace, f: f, pending: map[string]postman.Change{}, wake: make(chan struct{}, 1), done: ctx.Done()}
	c.mu.Lock()
	c.watches[w] = true
	c.sock.Keep(true)
	c.mu.Unlock()
	go w.deliver()
	go func() {
		<-ctx.Done()
		stop()
		cancel()
		c.mu.Lock()
		delete(c.watches, w)
		if len(c.watches) == 0 {
			c.sock.Keep(false)
		}
		c.mu.Unlock()
		c.wakeWatches()
	}()
	subctx, finish := context.WithTimeout(ctx, 30*time.Second)
	defer finish()
	if err := c.lockWatches(subctx); err != nil {
		cancel()
		return err
	}
	err := c.subscribe(subctx, workspace)
	<-c.watchGate
	if err != nil {
		cancel()
		return err
	}
	if err := ctx.Err(); err != nil {
		cancel()
		return err
	}
	return nil
}

func (c *Client) lockWatches(ctx context.Context) error {
	select {
	case c.watchGate <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-c.ctx.Done():
		return c.ctx.Err()
	}
}

// subscribe is called with watchGate held. Successful halves are remembered
// so a failed second subscription can be retried or cleaned up precisely.
func (c *Client) subscribe(ctx context.Context, workspace string) error {
	for _, kind := range []string{"collection", "environment"} {
		key := subscription{workspace, kind}
		if c.subscribed[key] {
			continue
		}
		path := "/list/" + kind + "?workspace=" + url.QueryEscape(workspace) + "&subscribe=true"
		if err := c.call(ctx, "post", path, map[string]any{"ids": []string{nothing}}, nil); err != nil {
			return err
		}
		c.subscribed[key] = true
	}
	return nil
}

func (c *Client) wakeWatches() {
	select {
	case c.watchWake <- struct{}{}:
	default:
	}
}

// A single worker owns resubscription, cleanup and retry. Multiple failed
// subscriptions must not produce an expanding tree of retry goroutines.
func (c *Client) reconnected() {
	c.mu.Lock()
	c.watchReset = true
	c.listings = map[string]*listing{}
	c.mu.Unlock()
	c.wakeWatches()
}

func (c *Client) maintainWatches() {
	var retry <-chan time.Time
	var timer *time.Timer
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	delay := time.Second
	catchup := map[string]bool{}
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-c.watchWake:
			if timer != nil {
				timer.Stop()
			}
			retry = nil
		case <-retry:
			retry = nil
		}
		ctx, cancel := context.WithTimeout(c.ctx, 30*time.Second)
		if err := c.lockWatches(ctx); err != nil {
			cancel()
			continue
		}
		c.mu.Lock()
		reset := c.watchReset
		c.watchReset = false
		desired := map[string][]*watch{}
		for w := range c.watches {
			desired[w.ws] = append(desired[w.ws], w)
		}
		c.mu.Unlock()
		if reset {
			clear(c.subscribed)
			for ws := range desired {
				catchup[ws] = true
			}
		}
		failed := false
		for key := range c.subscribed {
			if len(desired[key.workspace]) != 0 {
				continue
			}
			path := "/list/" + key.kind + "/subscribe?workspace=" + url.QueryEscape(key.workspace)
			if err := c.call(ctx, "delete", path, nil, nil); err != nil {
				failed = true
			} else {
				delete(c.subscribed, key)
			}
		}
		for ws, watches := range desired {
			if err := c.subscribe(ctx, ws); err != nil {
				failed = true
				continue
			}
			if catchup[ws] {
				for _, w := range watches {
					w.push(postman.Change{Action: "reconnect"})
				}
				delete(catchup, ws)
			}
		}
		for ws := range catchup {
			if len(desired[ws]) == 0 {
				delete(catchup, ws)
			}
		}
		<-c.watchGate
		cancel()
		if failed {
			timer = time.NewTimer(delay)
			retry = timer.C
			delay = min(2*delay, time.Minute)
		} else {
			delay = time.Second
		}
	}
}

// event hears an event from the service.
func (c *Client) event(name string, data json.RawMessage) {
	if name != "broadcast" {
		return
	}
	var b struct {
		Meta struct {
			Subscription struct {
				ID string `json:"id"`
			} `json:"subscription"`
		} `json:"meta"`
		Data struct {
			Data []struct {
				ID     string `json:"id"`
				Action string `json:"action"`
			} `json:"data"`
		} `json:"data"`
	}
	if json.Unmarshal(data, &b) != nil {
		return
	}
	// workspace/<id>/list/collections
	parts := strings.Split(b.Meta.Subscription.ID, "/")
	if len(parts) != 4 || parts[0] != "workspace" || parts[2] != "list" {
		return
	}
	kind := strings.TrimSuffix(parts[3], "s")
	if kind != "collection" && kind != "environment" {
		return
	}
	// Subsequent reads must not join a request started before this event.
	c.invalidateReads()
	c.mu.Lock()
	var list []*watch
	for w := range c.watches {
		if w.ws == parts[1] {
			list = append(list, w)
		}
	}
	c.mu.Unlock()
	for _, x := range b.Data.Data {
		ch := postman.Change{Kind: kind, UID: x.ID, Action: x.Action}
		if kind == "collection" {
			c.forget(x.ID)
		}
		for _, w := range list {
			w.push(ch)
		}
	}
}

// push coalesces repeated invalidations, retaining the latest action for an
// entity. Overflow requires a full catch-up rather than silently losing data.
func (w *watch) push(ch postman.Change) {
	select {
	case <-w.done:
		return
	default:
	}
	w.mu.Lock()
	key := ch.Kind + ":" + ch.UID
	_, exists := w.pending[key]
	if ch.Action == "reconnect" || (!w.catchup && !exists && len(w.pending) >= maxPendingChanges) {
		clear(w.pending)
		w.order = nil
		w.catchup = true
	} else if !w.catchup {
		if !exists {
			w.order = append(w.order, key)
		}
		w.pending[key] = ch
	}
	w.mu.Unlock()
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

func (w *watch) deliver() {
	for {
		select {
		case <-w.done:
			return
		case <-w.wake:
		}
		w.mu.Lock()
		batch := make([]postman.Change, 0, len(w.order)+1)
		if w.catchup {
			batch = append(batch, postman.Change{Action: "reconnect"})
		}
		for _, key := range w.order {
			batch = append(batch, w.pending[key])
		}
		clear(w.pending)
		w.order = nil
		w.catchup = false
		w.mu.Unlock()
		for _, ch := range batch {
			select {
			case <-w.done:
				return
			default:
			}
			w.f(ch)
		}
	}
}

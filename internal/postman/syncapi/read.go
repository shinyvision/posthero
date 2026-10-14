package syncapi

import (
	"context"
	"encoding/json"
	"time"
)

type readFlight struct {
	done     chan struct{}
	cancel   context.CancelFunc
	waiters  int
	finished bool
	response Response
	err      error
}

// request shares concurrent identical reads, never completed data or writes.
// The response bytes are immutable; callers decode into their own objects.
// Canceling one caller leaves the others running, while the last cancellation
// releases the network request. Session identity is part of the key.
func (c *Client) request(ctx context.Context, method, path string, data any, token string) (Response, error) {
	headers := map[string]string{"x-access-token": token, "user-agent": c.agent, "x-app-version": AppVersion}
	if !readRequest(method, path) {
		// A read started before a mutation is not a fresh read after it.
		c.invalidateReads()
		defer c.invalidateReads()
		return c.sock.Request(ctx, method, path, data, headers)
	}
	if err := ctx.Err(); err != nil {
		return Response{}, err
	}
	body, err := json.Marshal(data)
	if err != nil {
		return Response{}, err
	}
	key := method + "\x00" + path + "\x00" + token + "\x00" + string(body)
	c.mu.Lock()
	f := c.reads[key]
	if f == nil {
		shared, cancel := context.WithTimeout(c.ctx, 2*time.Minute)
		f = &readFlight{done: make(chan struct{}), cancel: cancel}
		c.reads[key] = f
		go func() {
			resp, err := c.sock.Request(shared, method, path, data, headers)
			cancel()
			c.mu.Lock()
			f.response, f.err, f.finished = resp, err, true
			if c.reads[key] == f {
				delete(c.reads, key)
			}
			close(f.done)
			c.mu.Unlock()
		}()
	}
	f.waiters++
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		f.waiters--
		if f.waiters == 0 && !f.finished {
			f.cancel()
			if c.reads[key] == f {
				delete(c.reads, key)
			}
		}
		c.mu.Unlock()
	}()
	select {
	case <-ctx.Done():
		return Response{}, ctx.Err()
	case <-f.done:
		return f.response, f.err
	}
}

func (c *Client) invalidateReads() {
	c.mu.Lock()
	c.reads = map[string]*readFlight{}
	c.mu.Unlock()
}

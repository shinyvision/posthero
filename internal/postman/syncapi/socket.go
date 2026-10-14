package syncapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/websocket"
)

// The sync service speaks Sails' requests over Socket.IO 2, which runs on
// Engine.IO 3, over a websocket. Engine.IO packets are text frames that
// start with their type: 0 open, 1 close, 2 ping, 3 pong, 4 message. A
// message carries a Socket.IO packet, which also starts with its type: 0
// connect, 1 disconnect, 2 event, 3 ack, 4 error. So "42" starts an event
// and "43" an ack, followed by the ack's number and a JSON list.
//
// A Sails request is an event named after its method ("get", "post", …)
// with {method, url, data, headers}, sent with a number; the answer is the
// ack with that number: [{body, statusCode, headers}]. Events the service
// sends on its own (changes to what is subscribed to) are "broadcast"
// events.

// sailsQuery is what Sails' JavaScript client says about itself when it
// connects; the service wants it.
const sailsQuery = "__sails_io_sdk_version=1.2.1&__sails_io_sdk_platform=browser&__sails_io_sdk_language=javascript"

// ErrDisconnected means the connection went away before an answer came.
// Nothing is known about a write then: it may or may not have been made.
var ErrDisconnected = errors.New("the connection to Postman's sync service was lost")

// Response is the service's answer to a request.
type Response struct {
	Status int
	Body   json.RawMessage
}

// Socket is a connection to the sync service. It connects when it is first
// used, keeps the connection alive, and connects again when it is needed
// after a loss: at once when something is subscribed (OnConnect then sets
// the subscriptions up again), else on the next request.
type Socket struct {
	// URL is the service's address (https or wss).
	URL string
	// Header goes with the websocket handshake: the user agent, the origin.
	Header http.Header
	// OnEvent hears the events the service sends on its own, on the
	// socket's goroutine: it must not block.
	OnEvent func(name string, data json.RawMessage)
	// OnConnect runs (on a goroutine of its own) after each connection but
	// the first: what was subscribed to has to be subscribed to again.
	OnConnect func()

	mu        sync.Mutex
	conn      *conn
	dialing   *dialAttempt
	ctx       context.Context
	cancel    context.CancelFunc
	wake      chan struct{}
	connected int // connections made
	closed    bool
	keep      bool // connect again at once after a loss
}

type dialAttempt struct {
	done chan struct{}
	err  error
}

// conn is one websocket connection.
type conn struct {
	ws      *websocket.Conn
	write   chan struct{}
	slots   chan struct{}
	mu      sync.Mutex
	pending map[int]chan ack
	nextID  int
	done    chan struct{}
	err     error
	pong    chan struct{}
}

type ack struct {
	resp Response
	err  error
}

// Keep makes the socket connect again at once after it loses the
// connection (while something is subscribed), instead of on the next
// request.
func (s *Socket) Keep(on bool) {
	s.mu.Lock()
	s.initLocked()
	s.keep = on
	select {
	case s.wake <- struct{}{}:
	default:
	}
	s.mu.Unlock()
}

// Close closes the connection for good.
func (s *Socket) Close() {
	s.mu.Lock()
	s.initLocked()
	s.closed = true
	s.cancel()
	c := s.conn
	s.conn = nil
	s.mu.Unlock()
	if c != nil {
		c.close(errors.New("closed"))
	}
}

// Request sends a request and waits for its answer. A request lost with
// the connection is sent once more on a new one when it only reads (get);
// a write isn't, as it may have been made.
func (s *Socket) Request(ctx context.Context, method, path string, data any, headers map[string]string) (Response, error) {
	if _, bounded := ctx.Deadline(); !bounded {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
	}
	for attempt := 0; ; attempt++ {
		c, err := s.get(ctx)
		if err != nil {
			return Response{}, err
		}
		resp, err := c.request(ctx, method, path, data, headers)
		if errors.Is(err, ErrDisconnected) && readRequest(method, path) && attempt == 0 && ctx.Err() == nil {
			continue
		}
		return resp, err
	}
}

// readRequest includes Postman's list endpoints: they read, despite POST.
// No mutation is replayed after an ambiguous disconnect.
func readRequest(method, path string) bool {
	if method == "get" {
		return true
	}
	path = strings.SplitN(path, "?", 2)[0]
	return method == "post" && (path == "/list/collection" || path == "/list/environment")
}

func (s *Socket) initLocked() {
	if s.ctx == nil {
		s.ctx, s.cancel = context.WithCancel(context.Background())
		s.wake = make(chan struct{}, 1)
	}
}

// Dialing belongs to the socket, not the first caller. One canceled caller
// cannot abort all the other readers waiting for the same connection.
func (s *Socket) get(ctx context.Context) (*conn, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		s.mu.Lock()
		s.initLocked()
		if s.closed {
			s.mu.Unlock()
			return nil, errors.New("the connection to Postman's sync service is closed")
		}
		if c := s.conn; c != nil && c.alive() {
			s.mu.Unlock()
			return c, nil
		}
		d := s.dialing
		if d == nil {
			d = &dialAttempt{done: make(chan struct{})}
			s.dialing = d
			go s.connect(d)
		}
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-s.ctx.Done():
			return nil, s.ctx.Err()
		case <-d.done:
			if d.err != nil {
				return nil, d.err
			}
		}
	}
}

func (s *Socket) connect(d *dialAttempt) {
	c, err := s.dial(s.ctx)
	s.mu.Lock()
	if s.closed && err == nil {
		c.close(context.Canceled)
		err = context.Canceled
	}
	reconnected := false
	if err == nil {
		s.conn = c
		s.connected++
		reconnected = s.connected > 1
	}
	d.err = err
	s.dialing = nil
	close(d.done)
	s.mu.Unlock()
	if err != nil {
		return
	}
	go s.watch(c)
	if reconnected && s.OnConnect != nil {
		s.OnConnect()
	}
}

func (s *Socket) watch(c *conn) {
	<-c.done
	for delay := time.Second; ; delay = min(2*delay, time.Minute) {
		s.mu.Lock()
		again := s.keep && !s.closed && s.conn == c
		s.mu.Unlock()
		if !again {
			return
		}
		ctx, cancel := context.WithTimeout(s.ctx, 30*time.Second)
		_, err := s.get(ctx)
		cancel()
		if err == nil {
			return
		}
		// Spread reconnects after an outage, and wake immediately on shutdown
		// or when the last subscription is canceled.
		timer := time.NewTimer(delay/2 + time.Duration(rand.Int64N(int64(delay/2)+1)))
		select {
		case <-s.ctx.Done():
			timer.Stop()
			return
		case <-s.wake:
			timer.Stop()
		case <-timer.C:
		}
	}
}

// dial opens a connection and waits until the service says it is
// connected.
func (s *Socket) dial(ctx context.Context) (*conn, error) {
	loc, err := socketURL(s.URL)
	if err != nil {
		return nil, err
	}
	origin := "https://client.postman.co"
	if o := s.Header.Get("Origin"); o != "" {
		origin = o
	}
	cfg, err := websocket.NewConfig(loc, origin)
	if err != nil {
		return nil, err
	}
	for k, v := range s.Header {
		if k != "Origin" {
			cfg.Header[k] = v
		}
	}
	dctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	ws, err := cfg.DialContext(dctx)
	if err != nil {
		return nil, fmt.Errorf("couldn't reach Postman's sync service: %w", err)
	}
	ws.MaxPayloadBytes = 256 << 20
	c := &conn{ws: ws, write: make(chan struct{}, 1), slots: make(chan struct{}, 64), pending: map[int]chan ack{}, done: make(chan struct{}), pong: make(chan struct{}, 1)}
	stop := context.AfterFunc(dctx, func() { ws.Close() })
	defer stop()
	// The service opens (0) with its heartbeat, then connects the default
	// namespace (40).
	_ = ws.SetReadDeadline(time.Now().Add(20 * time.Second))
	interval, timeout := 25*time.Second, 60*time.Second
	for {
		var msg string
		if err := websocket.Message.Receive(ws, &msg); err != nil {
			ws.Close()
			return nil, fmt.Errorf("Postman's sync service didn't answer: %w", err)
		}
		if strings.HasPrefix(msg, "0") {
			var open struct {
				PingInterval int `json:"pingInterval"`
				PingTimeout  int `json:"pingTimeout"`
			}
			if json.Unmarshal([]byte(msg[1:]), &open) == nil && open.PingInterval > 0 {
				interval = time.Duration(open.PingInterval) * time.Millisecond
				if open.PingTimeout > 0 {
					timeout = time.Duration(open.PingTimeout) * time.Millisecond
				}
			}
			continue
		}
		if strings.HasPrefix(msg, "40") {
			break
		}
		if strings.HasPrefix(msg, "44") || strings.HasPrefix(msg, "1") {
			ws.Close()
			return nil, fmt.Errorf("Postman's sync service refused the connection: %s", msg)
		}
	}
	_ = ws.SetReadDeadline(time.Time{})
	go c.read(s)
	go c.heartbeat(interval, timeout)
	return c, nil
}

// socketURL is the websocket address for the service's URL.
func socketURL(base string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	}
	u.Path = strings.TrimSuffix(u.Path, "/") + "/socket.io/"
	u.RawQuery = sailsQuery + "&EIO=3&transport=websocket"
	return u.String(), nil
}

func (c *conn) alive() bool {
	select {
	case <-c.done:
		return false
	default:
		return true
	}
}

// close ends the connection and fails what waits on it.
func (c *conn) close(err error) {
	c.mu.Lock()
	select {
	case <-c.done:
		c.mu.Unlock()
		return
	default:
	}
	c.err = err
	close(c.done)
	pending := c.pending
	c.pending = map[int]chan ack{}
	c.mu.Unlock()
	c.ws.Close()
	for _, ch := range pending {
		ch <- ack{err: ErrDisconnected}
	}
}

func (c *conn) send(ctx context.Context, msg string) error {
	select {
	case c.write <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		return ErrDisconnected
	}
	defer func() { <-c.write }()
	if err := ctx.Err(); err != nil {
		return err
	}
	deadline := time.Now().Add(30 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = c.ws.SetWriteDeadline(deadline)
	// A canceled, partially sent websocket frame cannot be reused safely.
	stop := context.AfterFunc(ctx, func() { c.close(ctx.Err()) })
	defer stop()
	return websocket.Message.Send(c.ws, msg)
}

// request sends one request and waits for its ack.
func (c *conn) request(ctx context.Context, method, path string, data any, headers map[string]string) (Response, error) {
	select {
	case c.slots <- struct{}{}:
	case <-ctx.Done():
		return Response{}, ctx.Err()
	case <-c.done:
		return Response{}, ErrDisconnected
	}
	defer func() { <-c.slots }()
	if err := ctx.Err(); err != nil {
		return Response{}, err
	}
	if data == nil {
		data = map[string]any{}
	}
	body, err := json.Marshal([]any{method, map[string]any{"method": method, "url": path, "data": data, "headers": headers}})
	if err != nil {
		return Response{}, err
	}
	ch := make(chan ack, 1)
	c.mu.Lock()
	select {
	case <-c.done:
		c.mu.Unlock()
		return Response{}, ErrDisconnected
	default:
	}
	c.nextID++
	id := c.nextID
	c.pending[id] = ch
	c.mu.Unlock()
	forget := func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}
	if err := c.send(ctx, "42"+strconv.Itoa(id)+string(body)); err != nil {
		forget()
		if ctx.Err() != nil {
			return Response{}, ctx.Err()
		}
		c.close(err)
		return Response{}, ErrDisconnected
	}
	select {
	case a := <-ch:
		return a.resp, a.err
	case <-ctx.Done():
		forget()
		return Response{}, ctx.Err()
	}
}

// read reads packets until the connection ends.
func (c *conn) read(s *Socket) {
	for {
		var msg string
		if err := websocket.Message.Receive(c.ws, &msg); err != nil {
			c.close(err)
			return
		}
		switch {
		case msg == "3":
			select {
			case c.pong <- struct{}{}:
			default:
			}
		case msg == "2":
			_ = c.send(context.Background(), "3")
		case strings.HasPrefix(msg, "43"):
			c.answer(msg[2:])
		case strings.HasPrefix(msg, "42"):
			if s.OnEvent != nil {
				if name, data, ok := event(msg[2:]); ok {
					s.OnEvent(name, data)
				}
			}
		case strings.HasPrefix(msg, "41"), strings.HasPrefix(msg, "44"), strings.HasPrefix(msg, "1"):
			c.close(errors.New("the service closed the connection"))
			return
		}
	}
}

// answer hands an ack ("<id>[…]") to the request waiting for it.
func (c *conn) answer(msg string) {
	i := strings.IndexByte(msg, '[')
	if i <= 0 {
		return
	}
	id, err := strconv.Atoi(msg[:i])
	if err != nil {
		return
	}
	c.mu.Lock()
	ch := c.pending[id]
	delete(c.pending, id)
	c.mu.Unlock()
	if ch == nil {
		return
	}
	var list []struct {
		Body       json.RawMessage `json:"body"`
		StatusCode *int            `json:"statusCode"`
	}
	if err := json.Unmarshal([]byte(msg[i:]), &list); err != nil || len(list) == 0 {
		ch <- ack{err: fmt.Errorf("Postman's sync service sent an answer posthero can't read: %v", err)}
		return
	}
	status := 200
	if list[0].StatusCode != nil {
		status = *list[0].StatusCode
	}
	ch <- ack{resp: Response{Status: status, Body: list[0].Body}}
}

// event reads an event packet's payload (`["name", data]`).
func event(payload string) (string, json.RawMessage, bool) {
	payload = strings.TrimLeft(payload, "0123456789")
	var list []json.RawMessage
	if json.Unmarshal([]byte(payload), &list) != nil || len(list) == 0 {
		return "", nil, false
	}
	var name string
	if json.Unmarshal(list[0], &name) != nil {
		return "", nil, false
	}
	var data json.RawMessage
	if len(list) > 1 {
		data = list[1]
	}
	return name, data, true
}

// heartbeat pings the service (Engine.IO 3: the client pings) and ends the
// connection when it stops answering.
func (c *conn) heartbeat(interval, timeout time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-c.done:
			return
		case <-t.C:
		}
		select {
		case <-c.pong:
		default:
		}
		if err := c.send(context.Background(), "2"); err != nil {
			c.close(err)
			return
		}
		select {
		case <-c.pong:
		case <-time.After(timeout):
			c.close(errors.New("Postman's sync service stopped answering"))
			return
		case <-c.done:
			return
		}
	}
}

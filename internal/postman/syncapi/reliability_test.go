package syncapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shinyvision/posthero/internal/postman"
	"github.com/shinyvision/posthero/internal/postman/syncapi/synctest"
	"golang.org/x/net/websocket"
)

func waitUntil(t *testing.T, f func() bool) {
	t.Helper()
	for end := time.Now().Add(3 * time.Second); time.Now().Before(end); time.Sleep(time.Millisecond) {
		if f() {
			return
		}
	}
	t.Fatal("condition was not met")
}

func TestSharedWatchSubscriptionsAndCleanup(t *testing.T) {
	srv := synctest.New()
	base := srv.Start()
	defer srv.Close()
	c := New(synctest.Token, Options{URL: base})
	defer c.Close()
	a, stopA := context.WithCancel(context.Background())
	defer stopA()
	b, stopB := context.WithCancel(context.Background())
	defer stopB()
	if err := c.Watch(a, "ws1", func(postman.Change) {}); err != nil {
		t.Fatal(err)
	}
	if err := c.Watch(b, "ws1", func(postman.Change) {}); err != nil {
		t.Fatal(err)
	}
	if n := srv.CallCount("post /list/collection"); n != 1 {
		t.Fatalf("duplicate subscriptions: %d", n)
	}
	stopA()
	waitUntil(t, func() bool { c.mu.Lock(); defer c.mu.Unlock(); return len(c.watches) == 1 })
	if n := srv.Subscriptions("ws1", "collection"); n != 1 {
		t.Fatal("first cancellation removed another consumer")
	}
	stopB()
	waitUntil(t, func() bool {
		return srv.Subscriptions("ws1", "collection") == 0 && srv.Subscriptions("ws1", "environment") == 0
	})
	if srv.CallCount("delete /list/collection/subscribe") != 1 {
		t.Fatal("server subscription not released")
	}
}

func TestWatchQueueIsBoundedAndCoalesces(t *testing.T) {
	done := make(chan struct{})
	w := &watch{done: done, wake: make(chan struct{}, 1), pending: map[string]postman.Change{}}
	for i := 0; i < 10000; i++ {
		w.push(postman.Change{Kind: "collection", UID: "same", Action: "update"})
	}
	if len(w.pending) != 1 || len(w.order) != 1 {
		t.Fatal("duplicate invalidations accumulated")
	}
	for i := 0; i < maxPendingChanges+1; i++ {
		w.push(postman.Change{Kind: "collection", UID: fmt.Sprint(i), Action: "update"})
	}
	if !w.catchup || len(w.pending) != 0 {
		t.Fatal("overflow did not become a catch-up")
	}
	got := make(chan postman.Change, 1)
	w.f = func(ch postman.Change) { got <- ch }
	go w.deliver()
	defer close(done)
	select {
	case ch := <-got:
		if ch.Action != "reconnect" {
			t.Fatal(ch)
		}
	case <-time.After(time.Second):
		t.Fatal("no catch-up")
	}
}

func TestConcurrentReadsShareRequestAndCancelIndependently(t *testing.T) {
	srv := synctest.New()
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	srv.BeforeRequest = func(method, path string) {
		if path == "/workspace" {
			once.Do(func() { close(entered); <-release })
		}
	}
	base := srv.Start()
	defer srv.Close()
	c := New(synctest.Token, Options{URL: base})
	defer c.Close()
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	first, cancel := context.WithCancel(context.Background())
	defer cancel()
	firstDone := make(chan error, 1)
	go func() { _, err := c.Workspaces(first); firstDone <- err }()
	<-entered
	const readers = 24
	result := make(chan error, readers)
	for i := 0; i < readers; i++ {
		go func() { _, err := c.Workspaces(context.Background()); result <- err }()
	}
	waitUntil(t, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		for _, f := range c.reads {
			return f.waiters == readers+1
		}
		return false
	})
	cancel()
	if err := <-firstDone; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	close(release)
	for i := 0; i < readers; i++ {
		if err := <-result; err != nil {
			t.Fatal(err)
		}
	}
	if n := srv.CallCount("get /workspace"); n != 1 {
		t.Fatalf("sent %d reads, want one", n)
	}
	// Completed data is not cached; later reads must reach the service.
	if _, err := c.Workspaces(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := srv.CallCount("get /workspace"); n != 2 {
		t.Fatalf("cached completed read: %d", n)
	}
}

func TestPostListRetriesButWritesDoNot(t *testing.T) {
	srv := synctest.New()
	base := srv.Start()
	defer srv.Close()
	c := New(synctest.Token, Options{URL: base})
	defer c.Close()
	srv.DropNext("post /list/environment")
	if _, err := c.Environments(context.Background(), "ws1"); err != nil {
		t.Fatal(err)
	}
	if n := srv.CallCount("post /list/environment"); n != 2 {
		t.Fatalf("list was sent %d times", n)
	}
	for _, method := range []string{"post", "put", "patch", "delete"} {
		if readRequest(method, "/request/id") {
			t.Fatalf("%s mutation marked replayable", method)
		}
	}
}

func TestCanceledHandshakeDoesNotBlockAnotherCaller(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	srv := httptest.NewServer(websocket.Server{Handshake: func(*websocket.Config, *http.Request) error { return nil }, Handler: func(ws *websocket.Conn) {
		defer ws.Close()
		close(entered)
		<-release
		_ = websocket.Message.Send(ws, `0{"pingInterval":60000,"pingTimeout":60000}`)
		_ = websocket.Message.Send(ws, "40")
		for {
			var msg string
			if websocket.Message.Receive(ws, &msg) != nil {
				return
			}
			if strings.HasPrefix(msg, "42") {
				id := strings.SplitN(msg[2:], "[", 2)[0]
				_ = websocket.Message.Send(ws, "43"+id+`[{"body":{},"statusCode":200}]`)
			}
		}
	}})
	defer srv.Close()
	s := &Socket{URL: srv.URL}
	defer s.Close()
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	first := make(chan error, 1)
	go func() { _, err := s.Request(ctx, "get", "/one", nil, nil); first <- err }()
	<-entered
	cancel()
	select {
	case err := <-first:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled handshake caller blocked")
	}
	second := make(chan error, 1)
	go func() { _, err := s.Request(context.Background(), "get", "/two", nil, nil); second <- err }()
	close(release)
	select {
	case err := <-second:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("second caller lost shared connection")
	}
}

func TestFailedWatchRollsBackPartialSubscription(t *testing.T) {
	srv := synctest.New()
	base := srv.Start()
	defer srv.Close()
	c := New(synctest.Token, Options{URL: base})
	defer c.Close()
	srv.FailNext("post /list/environment", 503)
	if err := c.Watch(context.Background(), "ws1", func(postman.Change) {}); err == nil {
		t.Fatal("failed subscription was accepted")
	}
	waitUntil(t, func() bool { return srv.Subscriptions("ws1", "collection") == 0 })
	if srv.CallCount("delete /list/collection/subscribe") != 1 {
		t.Fatal("partial subscription leaked")
	}
}

func TestReconnectRetriesSubscriptionBeforeReportingCatchup(t *testing.T) {
	srv := synctest.New()
	base := srv.Start()
	defer srv.Close()
	c := New(synctest.Token, Options{URL: base})
	defer c.Close()
	got := make(chan postman.Change, 16)
	if err := c.Watch(context.Background(), "ws1", func(ch postman.Change) { got <- ch }); err != nil {
		t.Fatal(err)
	}
	srv.FailNext("post /list/environment", 503)
	srv.Drop()
	waitUntil(t, func() bool { return srv.CallCount("post /list/environment") >= 2 })
	select {
	case ch := <-got:
		t.Fatalf("catch-up before subscriptions recovered: %+v", ch)
	default:
	}
	select {
	case ch := <-got:
		if ch.Action != "reconnect" {
			t.Fatal(ch)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("retry did not recover")
	}
	if srv.Subscriptions("ws1", "collection") != 1 || srv.Subscriptions("ws1", "environment") != 1 {
		t.Fatal("incomplete resubscription")
	}
}

func TestCanceledQueuedWriteLeavesConnectionUsable(t *testing.T) {
	srv := synctest.New()
	base := srv.Start()
	defer srv.Close()
	s := &Socket{URL: base}
	defer s.Close()
	c, err := s.get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	c.write <- struct{}{} // another frame is currently being written
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err = c.request(ctx, "get", "/workspace", nil, map[string]string{"x-access-token": synctest.Token})
	<-c.write
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if !c.alive() {
		t.Fatal("canceling a queued request disconnected other consumers")
	}
	if _, err := s.Request(context.Background(), "get", "/workspace", nil, map[string]string{"x-access-token": synctest.Token}); err != nil {
		t.Fatal(err)
	}
	if srv.CallCount("get /workspace") != 1 {
		t.Fatal("canceled request reached server")
	}
}

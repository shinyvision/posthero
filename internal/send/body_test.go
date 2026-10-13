package send

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// A body that stops before its end (the connection dropped, the time ran
// out, the request was cancelled) isn't a complete response: Do says so,
// and still hands over the status, the headers and what arrived.
func TestIncompleteBody(t *testing.T) {
	const part = `{"items":[1,2,3`
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/silent" {
			select {
			case <-r.Context().Done():
			case <-release:
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/chunked" {
			w.Header().Set("Content-Length", "1000")
		}
		_, _ = io.WriteString(w, part)
		w.(http.Flusher).Flush()
		switch r.URL.Path {
		case "/drop", "/chunked":
			if conn, _, err := w.(http.Hijacker).Hijack(); err == nil {
				conn.Close()
			}
		case "/stall":
			select {
			case <-r.Context().Done():
			case <-release:
			}
		}
	}))
	defer srv.Close()
	defer close(release)
	c := NewClient(nil)
	get := func(ctx context.Context, path string, timeout time.Duration, want int64, why error, says string) {
		t.Helper()
		s := DefaultSettings()
		s.Timeout = timeout
		resp, err := c.Do(ctx, &Prepared{Method: "GET", URL: srv.URL + path, Settings: s})
		if err == nil {
			t.Errorf("%s: no error; %d with %q, truncated %v", path, resp.Status, resp.Body, resp.Truncated)
			return
		}
		if resp == nil {
			t.Errorf("%s: %v, and no response", path, err)
			return
		}
		if resp.Status != 200 || string(resp.Body) != part || resp.ContentType() != "application/json" || resp.Truncated || resp.WireSize != int64(len(part)) || resp.At.IsZero() {
			t.Errorf("%s: %d %q %q truncated %v, wire %d", path, resp.Status, resp.ContentType(), resp.Body, resp.Truncated, resp.WireSize)
		}
		var inc *IncompleteError
		if !errors.As(err, &inc) || inc.Got != int64(len(part)) || inc.Want != want {
			t.Errorf("%s: %#v", path, err)
		}
		if !errors.Is(err, why) || err.Error() != says {
			t.Errorf("%s: %q, not %v", path, err, why)
		}
	}

	get(context.Background(), "/drop", 0, 1000, io.ErrUnexpectedEOF, "the body stopped after 15 B of 1000 B: the connection closed")
	get(context.Background(), "/chunked", 0, -1, io.ErrUnexpectedEOF, "the body stopped after 15 B: the connection closed")
	get(context.Background(), "/stall", 300*time.Millisecond, 1000, context.DeadlineExceeded,
		"the body stopped after 15 B of 1000 B: no complete response within 300ms (:set timeout changes that)")
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(300*time.Millisecond, cancel)
	get(ctx, "/stall", 0, 1000, context.Canceled, "the body stopped after 15 B of 1000 B: cancelled")

	// Cancelled before anything came: no response, and still "cancelled".
	ctx, cancel = context.WithCancel(context.Background())
	time.AfterFunc(300*time.Millisecond, cancel)
	resp, err := c.Do(ctx, &Prepared{Method: "GET", URL: srv.URL + "/silent", Settings: DefaultSettings()})
	if resp != nil || !errors.Is(err, context.Canceled) || err.Error() != "cancelled" {
		t.Errorf("cancelled before the headers: %v, %v", resp, err)
	}
}

// A compressed body over the maximum size is marked cut, though it was cut
// before decoding.
func TestCompressedOverLimitIsTruncated(t *testing.T) {
	plain := make([]byte, 64<<10)
	_, _ = rand.Read(plain) // doesn't compress: as much over the wire
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write(plain)
	_ = zw.Close()
	small := bytes.Repeat([]byte("a"), 1000)
	var gzSmall bytes.Buffer
	zw = gzip.NewWriter(&gzSmall)
	_, _ = zw.Write(small)
	_ = zw.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		if r.URL.Path == "/small" {
			_, _ = w.Write(gzSmall.Bytes())
			return
		}
		_, _ = w.Write(gz.Bytes())
	}))
	defer srv.Close()
	c := NewClient(nil)
	s := DefaultSettings()
	s.MaxResponseSize = 32 << 10
	resp, err := c.Do(context.Background(), &Prepared{Method: "GET", URL: srv.URL + "/big", Settings: s})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Truncated || int64(len(resp.Body)) > s.MaxResponseSize || !bytes.HasPrefix(plain, resp.Body) {
		t.Errorf("%d of %d bytes, truncated %v", len(resp.Body), len(plain), resp.Truncated)
	}
	// Exactly at the limit, decoded, isn't cut.
	s.MaxResponseSize = int64(len(small))
	resp, err = c.Do(context.Background(), &Prepared{Method: "GET", URL: srv.URL + "/small", Settings: s})
	if err != nil || resp.Truncated || !bytes.Equal(resp.Body, small) {
		t.Errorf("at the limit: %d bytes, truncated %v, %v", len(resp.Body), resp.Truncated, err)
	}
}

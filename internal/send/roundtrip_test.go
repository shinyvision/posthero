package send

import (
	"context"
	"crypto/tls"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

// Transport is what requests go through, for other code that talks HTTP
// itself (the token requests of OAuth 2.0): the same CA file, proxy and
// client certificates, the same connections, and no redirects followed or
// cookies added on its own.
func TestTransportForOthers(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile := clientCert(t, dir, "rachel")
	var cookie string
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie = r.Header.Get("Cookie")
		if r.URL.Path == "/moved" {
			http.Redirect(w, r, "/elsewhere", http.StatusFound)
			return
		}
		name := "nobody"
		if len(r.TLS.PeerCertificates) > 0 {
			name = r.TLS.PeerCertificates[0].Subject.CommonName
		}
		_, _ = io.WriteString(w, name)
	}))
	srv.TLS = &tls.Config{ClientAuth: tls.RequestClientCert}
	srv.StartTLS()
	defer srv.Close()
	proxy, tunnels := connectProxy(t)
	ca := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	s := DefaultSettings() // StrictSSL: only the CA file makes the server trusted
	s.CAFile = ca
	s.Proxy = proxy.URL
	s.Certs = []ClientCert{{Host: "127.0.0.1", Cert: certFile, Key: keyFile}}

	jar := NewJar("")
	u, _ := url.Parse(srv.URL)
	jar.SetCookies(u, []*http.Cookie{{Name: "session", Value: "jar"}})
	c := NewClient(jar)
	rt, err := c.Transport(s)
	if err != nil {
		t.Fatal(err)
	}
	if again, _ := c.Transport(s); again != rt {
		t.Error("the same settings gave another transport")
	}
	get := func(path string) (*http.Response, string) {
		t.Helper()
		req, _ := http.NewRequest("GET", srv.URL+path, nil)
		resp, err := rt.RoundTrip(req)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp, string(body)
	}
	resp, body := get("/token")
	if resp.StatusCode != 200 || body != "rachel" || tunnels.Load() != 1 {
		t.Errorf("%d %q, %d tunnels", resp.StatusCode, body, tunnels.Load())
	}
	if resp, _ := get("/moved"); resp.StatusCode != http.StatusFound {
		t.Errorf("a redirect gave %d: it was followed", resp.StatusCode)
	}
	if cookie != "" {
		t.Errorf("the jar's cookie went along: %q", cookie)
	}
	// A request takes the connection the transport left open.
	sent, err := c.Do(context.Background(), &Prepared{Method: "GET", URL: srv.URL + "/token", Settings: s})
	if err != nil {
		t.Fatal(err)
	}
	if !sent.Timings.Reused || tunnels.Load() != 1 || string(sent.Body) != "rachel" {
		t.Errorf("reused %v, %d tunnels, %q", sent.Timings.Reused, tunnels.Load(), sent.Body)
	}

	s.CAFile = filepath.Join(dir, "missing.pem")
	if _, err := c.Transport(s); err == nil {
		t.Error("a missing CA file: no error")
	}
}

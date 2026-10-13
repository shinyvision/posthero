package send

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// clientCert writes a self-signed client certificate and its key to dir.
func clientCert(t *testing.T, dir, name string) (certFile, keyFile string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: name},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kd, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certFile, keyFile = filepath.Join(dir, name+".crt"), filepath.Join(dir, name+".key")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kd}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certFile, keyFile
}

// whoami is a TLS server that asks for a client certificate and answers
// with its name, or "nobody".
func whoami(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(r.TLS.PeerCertificates) == 0 {
			_, _ = io.WriteString(w, "nobody")
			return
		}
		_, _ = io.WriteString(w, r.TLS.PeerCertificates[0].Subject.CommonName)
	}))
	srv.TLS = &tls.Config{ClientAuth: tls.RequestClientCert}
	srv.EnableHTTP2 = true
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv
}

// connectProxy is a proxy that only tunnels (CONNECT), as for https; it
// counts the tunnels it opened.
func connectProxy(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var tunnels atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			http.Error(w, "CONNECT only", http.StatusMethodNotAllowed)
			return
		}
		up, err := net.Dial("tcp", r.Host)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		tunnels.Add(1)
		w.WriteHeader(http.StatusOK)
		conn, buf, err := w.(http.Hijacker).Hijack()
		if err != nil {
			up.Close()
			return
		}
		go func() { _, _ = io.Copy(up, buf); up.Close() }()
		_, _ = io.Copy(conn, up)
		conn.Close()
	}))
	t.Cleanup(srv.Close)
	return srv, &tunnels
}

// A client certificate goes to its host, directly and through a proxy, and
// to no other host.
func TestClientCertDirectAndThroughProxy(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile := clientCert(t, dir, "rachel")
	mine, other := whoami(t), whoami(t)
	proxy, tunnels := connectProxy(t)
	mineHost := strings.TrimPrefix(mine.URL, "https://")
	for _, host := range []string{mineHost, "127.0.0.1"} {
		for _, viaProxy := range []bool{false, true} {
			s := DefaultSettings()
			s.StrictSSL = false
			s.Certs = []ClientCert{{Host: host, Cert: certFile, Key: keyFile}}
			if viaProxy {
				s.Proxy = proxy.URL
			}
			c := NewClient(nil)
			before := tunnels.Load()
			resp, err := c.Do(context.Background(), &Prepared{Method: "GET", URL: mine.URL + "/x", Settings: s})
			if err != nil {
				t.Fatalf("cert for %s, proxy %v: %v", host, viaProxy, err)
			}
			if string(resp.Body) != "rachel" || resp.Proto != "HTTP/2.0" {
				t.Errorf("cert for %s, proxy %v: the server saw %q over %s", host, viaProxy, resp.Body, resp.Proto)
			}
			if viaProxy && tunnels.Load() == before {
				t.Errorf("cert for %s: the request didn't go through the proxy", host)
			}
			if host != mineHost {
				continue
			}
			// The certificate is for one port; the other server doesn't get it.
			resp, err = c.Do(context.Background(), &Prepared{Method: "GET", URL: other.URL + "/x", Settings: s})
			if err != nil {
				t.Fatalf("other server, proxy %v: %v", viaProxy, err)
			}
			if string(resp.Body) != "nobody" {
				t.Errorf("other server, proxy %v: it saw %q", viaProxy, resp.Body)
			}
			// HTTP/1.1 only, still with the certificate.
			s.HTTP1 = true
			resp, err = c.Do(context.Background(), &Prepared{Method: "GET", URL: mine.URL + "/x", Settings: s})
			if err != nil || string(resp.Body) != "rachel" || resp.Proto != "HTTP/1.1" {
				t.Errorf("HTTP/1.1, proxy %v: %q over %s, %v", viaProxy, resp.Body, resp.Proto, err)
			}
		}
	}
}

// :set proxy proxy.corp:3128 means http://proxy.corp:3128, as curl and
// $HTTPS_PROXY take it.
func TestProxyWithoutScheme(t *testing.T) {
	var lines []string
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lines = append(lines, r.RequestURI)
		_, _ = io.WriteString(w, "via proxy")
	}))
	defer proxy.Close()
	hostport := strings.TrimPrefix(proxy.URL, "http://")
	_, port, _ := net.SplitHostPort(hostport)
	for _, px := range []string{hostport, "localhost:" + port} {
		lines = nil
		s := DefaultSettings()
		s.Proxy = px
		resp, err := NewClient(nil).Do(context.Background(), &Prepared{Method: "GET", URL: "http://api.example.invalid/x", Settings: s})
		if err != nil {
			t.Errorf("proxy %q: %v", px, err)
			continue
		}
		if string(resp.Body) != "via proxy" || len(lines) != 1 || lines[0] != "http://api.example.invalid/x" {
			t.Errorf("proxy %q: %q, the proxy got %q", px, resp.Body, lines)
		}
	}
	s := DefaultSettings()
	s.Proxy = "http://"
	if _, err := NewClient(nil).Do(context.Background(), &Prepared{Method: "GET", URL: "http://api.example.invalid/x", Settings: s}); err == nil || !strings.Contains(err.Error(), "proxy") {
		t.Errorf("a proxy without a host: %v", err)
	}
}

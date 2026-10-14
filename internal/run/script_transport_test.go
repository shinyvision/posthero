package run

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/shinyvision/posthero/internal/auth"
	"github.com/shinyvision/posthero/internal/postman"
	"github.com/shinyvision/posthero/internal/script"
	"github.com/shinyvision/posthero/internal/send"
	"github.com/shinyvision/posthero/internal/vars"
)

func TestScriptRequestsUseRunnerTransport(t *testing.T) {
	for _, mode := range []string{"proxy", "self-signed", "custom CA", "client certificate"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			handler := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				calls.Add(1)
				if mode == "client certificate" && len(req.TLS.PeerCertificates) == 0 {
					t.Error("missing client certificate")
				}
				fmt.Fprint(w, `{"access_token":"token"}`)
			})
			settings := send.DefaultSettings()
			var srv *httptest.Server
			if mode == "proxy" {
				srv = httptest.NewServer(handler)
				settings.Proxy = srv.URL
			} else {
				srv = httptest.NewUnstartedServer(handler)
				if mode == "client certificate" {
					srv.TLS = &tls.Config{ClientAuth: tls.RequireAnyClientCert}
				}
				srv.StartTLS()
				if mode == "self-signed" {
					settings.StrictSSL = false
				} else {
					cert := srv.TLS.Certificates[0]
					certFile := filepath.Join(t.TempDir(), "cert.pem")
					if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]}), 0600); err != nil {
						t.Fatal(err)
					}
					settings.CAFile = certFile
					if mode == "client certificate" {
						key, err := x509.MarshalPKCS8PrivateKey(cert.PrivateKey)
						if err != nil {
							t.Fatal(err)
						}
						keyFile := filepath.Join(t.TempDir(), "key.pem")
						if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}), 0600); err != nil {
							t.Fatal(err)
						}
						settings.Certs = []send.ClientCert{{Host: "127.0.0.1", Cert: certFile, Key: keyFile}}
					}
				}
			}
			defer srv.Close()
			url := srv.URL
			if mode == "proxy" {
				url = "http://only-through-proxy.invalid"
			}
			item := &postman.Item{Name: "Request", Request: &postman.Request{Method: "GET", URL: postman.ParseURL(url)}}
			item.Event = postman.SetScript(nil, "prerequest", fmt.Sprintf(`const r = await pm.sendRequest(%q); pm.test('token fetched', () => pm.expect(r.json().access_token).to.equal('token'));`, url+"/token"))
			r := &Runner{Engine: script.NewEngine(), Client: send.NewClient(nil), Auth: auth.NewManager(""), Settings: settings}
			ex := r.Execute(context.Background(), Target{Item: item}, vars.New(&vars.Scope{Kind: vars.Local}), Options{})
			if ex.Err != nil {
				t.Fatal(ex.Err)
			}
			if calls.Load() != 2 || ex.Scripts.Passed() != 1 {
				t.Fatalf("calls=%d tests=%+v", calls.Load(), ex.Scripts.Tests)
			}
		})
	}
}

package auth_test

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shinyvision/posthero/internal/auth"
	"github.com/shinyvision/posthero/internal/postman"
	"github.com/shinyvision/posthero/internal/send"
)

func md5hex(s string) string {
	h := md5.Sum([]byte(s))
	return hex.EncodeToString(h[:])
}

// A server offering Basic first and Digest second gets a right Digest
// answer (RFC 7616, MD5, qop=auth).
func TestDigest(t *testing.T) {
	const user, pass, realm, nonce = "ada", "lovelace", "shop", "dcd98b7102dd2f0e8b11d0f600bfb0c093"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := r.Header.Get("Authorization")
		if !strings.HasPrefix(h, "Digest ") {
			w.Header().Add("WWW-Authenticate", `Basic realm="shop"`)
			w.Header().Add("WWW-Authenticate", `Digest realm="`+realm+`", qop="auth", nonce="`+nonce+`", opaque="5ccc069c403ebaf9f0171e9517f40e41"`)
			w.WriteHeader(401)
			return
		}
		f := map[string]string{}
		for _, part := range strings.Split(strings.TrimPrefix(h, "Digest "), ",") {
			k, v, _ := strings.Cut(strings.TrimSpace(part), "=")
			f[k] = strings.Trim(v, `"`)
		}
		ha1 := md5hex(user + ":" + realm + ":" + pass)
		ha2 := md5hex(r.Method + ":" + f["uri"])
		want := md5hex(ha1 + ":" + nonce + ":" + f["nc"] + ":" + f["cnonce"] + ":" + f["qop"] + ":" + ha2)
		if f["username"] != user || f["response"] != want || f["uri"] != r.URL.RequestURI() {
			w.WriteHeader(403)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()
	raw := func(s string) json.RawMessage { b, _ := json.Marshal(s); return b }
	a := &postman.Auth{Type: "digest", Params: map[string][]postman.AuthParam{"digest": {
		{Key: "username", Value: raw(user), Type: "string"}, {Key: "password", Value: raw(pass), Type: "string"},
	}}}
	req := &send.Request{Method: "GET", URL: &postman.URL{Raw: srv.URL + "/orders?x=1"}, Settings: send.DefaultSettings()}
	prep, err := req.Prepare(env, send.Options{})
	if err != nil {
		t.Fatal(err)
	}
	resp, _, err := auth.NewManager("").Do(context.Background(), send.NewClient(nil), prep, a, "request", env.Resolve, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != 200 {
		t.Fatalf("status %d", resp.Status)
	}
}

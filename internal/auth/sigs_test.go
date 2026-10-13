package auth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/shinyvision/posthero/internal/postman"
	"github.com/shinyvision/posthero/internal/send"
)

func params(typ string, kv map[string]any) Params {
	a := &postman.Auth{Type: typ, Params: map[string][]postman.AuthParam{}}
	for k, v := range kv {
		raw, _ := json.Marshal(v)
		a.Params[typ] = append(a.Params[typ], postman.AuthParam{Key: k, Value: raw})
	}
	return Resolve(a, nil)
}

func atTime(t *testing.T, ts time.Time) {
	t.Helper()
	old := now
	now = func() time.Time { return ts }
	t.Cleanup(func() { now = old })
}

// RFC 5849, section 1.2.
func TestOAuth1(t *testing.T) {
	p := &send.Prepared{Method: "GET", URL: "http://photos.example.net/photos?file=vacation.jpg&size=original"}
	err := oauth1(p, params("oauth1", map[string]any{
		"consumerKey": "dpf43f3p2l4k3l03", "consumerSecret": "kd94hf93k423kf44",
		"token": "nnch734d00sl2jdk", "tokenSecret": "pfkkdhi9sl3r4s00",
		"nonce": "kllo9940pd9333jh", "timestamp": "1191242096", "signatureMethod": "HMAC-SHA1",
		"realm": "Photos",
	}))
	if err != nil {
		t.Fatal(err)
	}
	h := p.Get("Authorization")
	if !strings.Contains(h, `oauth_signature="tR3%2BTy81lMeYAr%2FFid0kMTYa%2FWM%3D"`) || !strings.HasPrefix(h, `OAuth realm="Photos",`) {
		t.Errorf("header: %s", h)
	}
}

// The get-vanilla case of AWS's Signature Version 4 test suite.
func TestAWSv4(t *testing.T) {
	atTime(t, time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC))
	p := &send.Prepared{Method: "GET", URL: "https://example.amazonaws.com/"}
	err := awsv4(p, params("awsv4", map[string]any{
		"accessKey": "AKIDEXAMPLE", "secretKey": "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY",
		"region": "us-east-1", "service": "service",
	}))
	if err != nil {
		t.Fatal(err)
	}
	want := "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20150830/us-east-1/service/aws4_request, SignedHeaders=host;x-amz-date, Signature=5fa00fa31553b73ebf1942676e86291e8372ff2a2260956d9b8aae1d763fbf31"
	if got := p.Get("Authorization"); got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	if p.Get("X-Amz-Date") != "20150830T123600Z" {
		t.Errorf("date: %s", p.Get("X-Amz-Date"))
	}
}

// The example in Hawk's documentation.
func TestHawk(t *testing.T) {
	p := &send.Prepared{Method: "GET", URL: "http://example.com:8000/resource/1?b=1&a=2"}
	err := hawk(p, params("hawk", map[string]any{
		"authId": "dh37fgj492je", "authKey": "werxhqb98rpaxn39848xrunpaw3489ruxnpa98w4rxn", "algorithm": "sha256",
		"timestamp": "1353832234", "nonce": "j4h3g2", "extraData": "some-app-ext-data",
	}))
	if err != nil {
		t.Fatal(err)
	}
	want := `Hawk id="dh37fgj492je", ts="1353832234", nonce="j4h3g2", ext="some-app-ext-data", mac="6R4rV5iE+NPoym+WwjeHzjAGXUtLNIxmo1vpMofpLAE="`
	if got := p.Get("Authorization"); got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestSimple(t *testing.T) {
	p := &send.Prepared{Method: "GET", URL: "http://x/a?b=1"}
	_ = basic(p, params("basic", map[string]any{"username": "Aladdin", "password": "open sesame"}))
	if p.Get("Authorization") != "Basic QWxhZGRpbjpvcGVuIHNlc2FtZQ==" {
		t.Errorf("basic: %s", p.Get("Authorization"))
	}
	_ = bearer(p, params("bearer", map[string]any{"token": "t0k"}))
	if p.Get("Authorization") != "Bearer t0k" {
		t.Errorf("bearer: %s", p.Get("Authorization"))
	}
	_ = apiKey(p, params("apikey", map[string]any{"key": "X-Api-Key", "value": "k"}))
	_ = apiKey(p, params("apikey", map[string]any{"key": "api key", "value": "a&b", "in": "query"}))
	if p.Get("X-Api-Key") != "k" || p.URL != "http://x/a?b=1&api%20key=a%26b" {
		t.Errorf("apikey: %s %s", p.Get("X-Api-Key"), p.URL)
	}
}

func TestJWT(t *testing.T) {
	p := &send.Prepared{Method: "GET", URL: "http://x/"}
	err := jwtBearer(p, params("jwt", map[string]any{"algorithm": "HS256", "secret": "s3cret", "payload": `{"sub":"ada","admin":true}`, "header": `{"kid":"k1"}`}))
	if err != nil {
		t.Fatal(err)
	}
	tok := strings.TrimPrefix(p.Get("Authorization"), "Bearer ")
	claims, ok := Claims(tok)
	if !ok || claims["sub"] != "ada" || claims["admin"] != true {
		t.Errorf("claims: %v", claims)
	}
	parts := strings.Split(tok, ".")
	hj, _ := base64.RawURLEncoding.DecodeString(parts[0])
	if !strings.Contains(string(hj), `"kid":"k1"`) || !strings.Contains(string(hj), `"alg":"HS256"`) {
		t.Errorf("header: %s", hj)
	}

	// RS256 and ES256 verify with their public keys.
	rk, _ := rsa.GenerateKey(rand.Reader, 2048)
	der, _ := x509.MarshalPKCS8PrivateKey(rk)
	pemKey := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	if err := jwtBearer(p, params("jwt", map[string]any{"algorithm": "RS256", "privateKey": pemKey, "payload": `{}`})); err != nil {
		t.Fatal(err)
	}
	tok = strings.TrimPrefix(p.Get("Authorization"), "Bearer ")
	parts = strings.Split(tok, ".")
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(&rk.PublicKey, 5, sum[:], sig); err != nil {
		t.Errorf("RS256 doesn't verify: %v", err)
	}
	ek, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, _ = x509.MarshalPKCS8PrivateKey(ek)
	pemKey = string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	if err := asap(p, params("asap", map[string]any{"alg": "ES256", "privateKey": pemKey, "iss": "me", "aud": "you", "kid": "me/key"})); err != nil {
		t.Fatal(err)
	}
	tok = strings.TrimPrefix(p.Get("Authorization"), "Bearer ")
	parts = strings.Split(tok, ".")
	sig, _ = base64.RawURLEncoding.DecodeString(parts[2])
	sum = sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	r, s := new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])
	if !ecdsa.Verify(&ek.PublicKey, sum[:], r, s) {
		t.Error("ES256 doesn't verify")
	}
	if c, _ := Claims(tok); c["iss"] != "me" || c["jti"] == nil {
		t.Errorf("asap claims: %v", c)
	}
}

// The GET cases of Akamai's EdgeGrid test data.
func TestEdgeGrid(t *testing.T) {
	cases := map[string]string{
		"/":                        "tL+y4hxyHxgWVD30X3pWnGKHcPzmrIF+LThiAOhMxYU=",
		"/testapi/v1/t1?p1=1&p2=2": "hKDH1UlnQySSHjvIcZpDMbQHihTQ0XyVAKZaApabdeA=",
	}
	for path, sig := range cases {
		p := &send.Prepared{Method: "GET", URL: "https://akaa-baseurl-xxxxxxxxxxx-xxxxxxxxxxxxx.luna.akamaiapis.net" + path}
		err := edgegrid(p, params("edgegrid", map[string]any{
			"accessToken": "akab-access-token-xxx-xxxxxxxxxxxxxxxx", "clientToken": "akab-client-token-xxx-xxxxxxxxxxxxxxxx",
			"clientSecret": "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx=", "timestamp": "20140321T19:34:21+0000", "nonce": "nonce-xx-xxxx-xxxx-xxxx-xxxxxxxxxxxx",
			"headersToSign": "X-Test1,X-Test2,X-Test3",
		}))
		if err != nil {
			t.Fatal(err)
		}
		want := "EG1-HMAC-SHA256 client_token=akab-client-token-xxx-xxxxxxxxxxxxxxxx;access_token=akab-access-token-xxx-xxxxxxxxxxxxxxxx;timestamp=20140321T19:34:21+0000;nonce=nonce-xx-xxxx-xxxx-xxxx-xxxxxxxxxxxx;signature=" + sig
		if got := p.Get("Authorization"); got != want {
			t.Errorf("%s:\ngot  %s\nwant %s", path, got, want)
		}
	}
}

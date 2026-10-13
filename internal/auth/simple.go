package auth

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"hash"
	"strings"
	"time"

	"github.com/shinyvision/posthero/internal/jsonx"
	"github.com/shinyvision/posthero/internal/send"
	"github.com/shinyvision/posthero/internal/vars"
)

// basic adds Authorization: Basic.
func basic(p *send.Prepared, prm Params) error {
	creds := prm.Str("username") + ":" + prm.Str("password")
	p.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(creds)), true)
	return nil
}

// bearer adds Authorization: Bearer.
func bearer(p *send.Prepared, prm Params) error {
	tok := prm.Str("token")
	if tok == "" {
		return nil // Postman sends nothing for an empty token
	}
	p.Set("Authorization", "Bearer "+tok, true)
	return nil
}

// apiKey adds a key to a header or the query.
func apiKey(p *send.Prepared, prm Params) error {
	key, value := prm.Str("key"), prm.Str("value")
	if key == "" {
		return nil
	}
	if prm.Str("in") == "query" {
		p.AddQuery(key, value)
		return nil
	}
	p.Set(key, value, true)
	return nil
}

// jwtBearer signs a JWT from the settings (Postman's JWT Bearer auth) and
// adds it as a bearer token or query parameter.
func jwtBearer(p *send.Prepared, prm Params) error {
	alg := prm.Or("algorithm", "HS256")
	var claims map[string]any
	if s := strings.TrimSpace(prm.Str("payload")); s != "" {
		if err := json.Unmarshal([]byte(s), &claims); err != nil {
			return fmt.Errorf("the JWT payload isn't valid JSON: %v", err)
		}
	}
	header := map[string]any{}
	if s := strings.TrimSpace(prm.Str("header")); s != "" {
		if err := json.Unmarshal([]byte(s), &header); err != nil {
			return fmt.Errorf("the JWT header isn't valid JSON: %v", err)
		}
	}
	key, err := signingKey(alg, prm.Str("secret"), prm.Bool("isSecretBase64Encoded"), prm.Str("privateKey"))
	if err != nil {
		return err
	}
	tok, err := SignJWT(alg, header, claims, key)
	if err != nil {
		return err
	}
	if prm.Str("addTokenTo") == "queryParam" {
		p.AddQuery(prm.Or("queryParamKey", "token"), tok)
		return nil
	}
	prefix := prm.Or("headerPrefix", "Bearer")
	p.Set("Authorization", strings.TrimSpace(prefix+" "+tok), true)
	return nil
}

// asap makes an Atlassian service-to-service token.
func asap(p *send.Prepared, prm Params) error {
	alg := prm.Or("alg", "RS256")
	now := time.Now()
	exp := time.Hour
	if s := prm.Str("exp"); s != "" {
		if d, err := time.ParseDuration(s); err == nil {
			exp = d
		} else if n := prm.Int("exp", 0); n > 0 {
			exp = time.Duration(n) * time.Second
		}
	}
	claims := map[string]any{"iss": prm.Str("iss"), "aud": prm.Str("aud"), "iat": now.Unix(), "nbf": now.Unix(), "exp": now.Add(exp).Unix(), "jti": vars.UUID()}
	if sub := prm.Str("sub"); sub != "" {
		claims["sub"] = sub
	}
	if s := strings.TrimSpace(prm.Str("claims")); s != "" {
		var more map[string]any
		if err := json.Unmarshal([]byte(s), &more); err != nil {
			return fmt.Errorf("the extra ASAP claims aren't valid JSON: %v", err)
		}
		for k, v := range more {
			claims[k] = v
		}
	}
	key, err := signingKey(alg, "", false, prm.Str("privateKey"))
	if err != nil {
		return err
	}
	tok, err := SignJWT(alg, map[string]any{"kid": prm.Str("kid")}, claims, key)
	if err != nil {
		return err
	}
	p.Set("Authorization", "Bearer "+tok, true)
	return nil
}

// signingKey reads the key for a JWT algorithm: a secret for HS*, a PEM
// private key for the others.
func signingKey(alg, secret string, base64Secret bool, privateKey string) (any, error) {
	if strings.HasPrefix(alg, "HS") {
		if base64Secret {
			b, err := base64.StdEncoding.DecodeString(secret)
			if err != nil {
				return nil, fmt.Errorf("the JWT secret isn't valid base64: %v", err)
			}
			return b, nil
		}
		return []byte(secret), nil
	}
	block, _ := pem.Decode([]byte(privateKey))
	if block == nil {
		return nil, fmt.Errorf("%s needs a private key in PEM form (-----BEGIN PRIVATE KEY-----)", alg)
	}
	if k, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	if k, err := x509.ParseECPrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	return nil, errors.New("the private key isn't a PKCS#8, PKCS#1 or EC key")
}

// SignJWT makes a signed JSON Web Token.
func SignJWT(alg string, header, claims map[string]any, key any) (string, error) {
	h := map[string]any{"typ": "JWT"}
	for k, v := range header {
		if v != "" {
			h[k] = v
		}
	}
	h["alg"] = alg
	hj, _ := jsonx.Marshal(h)
	if claims == nil {
		claims = map[string]any{}
	}
	cj, _ := jsonx.Marshal(claims)
	enc := base64.RawURLEncoding
	input := enc.EncodeToString(hj) + "." + enc.EncodeToString(cj)
	sig, err := jwsSign(alg, []byte(input), key)
	if err != nil {
		return "", err
	}
	return input + "." + enc.EncodeToString(sig), nil
}

func jwsHash(alg string) (crypto.Hash, func() hash.Hash, error) {
	switch alg[2:] {
	case "256":
		return crypto.SHA256, sha256.New, nil
	case "384":
		return crypto.SHA384, sha512.New384, nil
	case "512":
		return crypto.SHA512, sha512.New, nil
	}
	return 0, nil, fmt.Errorf("posthero doesn't know JWT algorithm %s", alg)
}

func jwsSign(alg string, input []byte, key any) ([]byte, error) {
	if len(alg) != 5 {
		return nil, fmt.Errorf("posthero doesn't know JWT algorithm %s", alg)
	}
	ch, newHash, err := jwsHash(alg)
	if err != nil {
		return nil, err
	}
	switch alg[:2] {
	case "HS":
		secret, _ := key.([]byte)
		mac := hmac.New(newHash, secret)
		mac.Write(input)
		return mac.Sum(nil), nil
	case "RS", "PS":
		k, ok := key.(*rsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("%s needs an RSA private key", alg)
		}
		hh := newHash()
		hh.Write(input)
		if alg[:2] == "PS" {
			return rsa.SignPSS(rand.Reader, k, ch, hh.Sum(nil), &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash})
		}
		return rsa.SignPKCS1v15(rand.Reader, k, ch, hh.Sum(nil))
	case "ES":
		k, ok := key.(*ecdsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("%s needs an EC private key", alg)
		}
		hh := newHash()
		hh.Write(input)
		r, s, err := ecdsa.Sign(rand.Reader, k, hh.Sum(nil))
		if err != nil {
			return nil, err
		}
		size := (k.Curve.Params().BitSize + 7) / 8
		out := make([]byte, 2*size)
		r.FillBytes(out[:size])
		s.FillBytes(out[size:])
		return out, nil
	}
	return nil, fmt.Errorf("posthero doesn't know JWT algorithm %s", alg)
}

// Claims decodes the claims of a JWT without checking its signature, for
// showing who a token is for and when it ends. ok is false when the token
// isn't a JWT.
func Claims(token string) (map[string]any, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, false
	}
	data, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return nil, false
	}
	var claims map[string]any
	if json.Unmarshal(data, &claims) != nil {
		return nil, false
	}
	return claims, true
}

// claimTime reads a numeric date claim (exp, iat).
func claimTime(claims map[string]any, key string) time.Time {
	switch v := claims[key].(type) {
	case float64:
		return time.Unix(int64(v), 0)
	case json.Number:
		if n, err := v.Int64(); err == nil {
			return time.Unix(n, 0)
		}
	}
	return time.Time{}
}

// Subject is who a token is for: the person's user name or email when the
// token says, else its subject.
func Subject(claims map[string]any) string {
	for _, k := range []string{"preferred_username", "email", "upn", "unique_name", "name", "client_id", "azp", "sub"} {
		if s, ok := claims[k].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

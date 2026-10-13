package auth

import (
	"crypto"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1" //nolint:gosec // OAuth 1.0 and Hawk use SHA-1 by name
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"hash"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/shinyvision/posthero/internal/postman"
	"github.com/shinyvision/posthero/internal/send"
)

// now is the clock signatures use; tests set it.
var now = time.Now

// nonce makes a random alphanumeric string.
func nonce(n int) string {
	const chars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = chars[int(b[i])%len(chars)]
	}
	return string(b)
}

// percent encodes for OAuth 1.0 (RFC 3986 unreserved characters only).
func percent(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '.' || c == '_' || c == '~' {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// body returns what a prepared request sends, reading a body file.
func body(p *send.Prepared) []byte {
	if p.BodyFile != "" {
		data, _ := os.ReadFile(p.BodyFile)
		return data
	}
	return p.Body
}

// oauth1 signs a request with OAuth 1.0 (RFC 5849).
func oauth1(p *send.Prepared, prm Params) error {
	method := strings.ToUpper(prm.Or("signatureMethod", "HMAC-SHA1"))
	ts := prm.Or("timestamp", strconv.FormatInt(now().Unix(), 10))
	oauth := map[string]string{
		"oauth_consumer_key":     prm.Str("consumerKey"),
		"oauth_nonce":            prm.Or("nonce", nonce(11)),
		"oauth_signature_method": method,
		"oauth_timestamp":        ts,
		"oauth_version":          prm.Or("version", "1.0"),
	}
	for k, key := range map[string]string{"oauth_token": "token", "oauth_callback": "callback", "oauth_verifier": "verifier"} {
		if v := prm.Str(key); v != "" || prm.Bool("addEmptyParamsToSign") && key == "token" {
			oauth[k] = v
		}
	}
	u, err := url.Parse(p.URL)
	if err != nil {
		return err
	}
	if prm.Bool("includeBodyHash") && p.Get("Content-Type") != "application/x-www-form-urlencoded" {
		var h hash.Hash = sha1.New() //nolint:gosec
		if strings.HasSuffix(method, "256") {
			h = sha256.New()
		} else if strings.HasSuffix(method, "512") {
			h = sha512.New()
		}
		h.Write(body(p))
		oauth["oauth_body_hash"] = base64.StdEncoding.EncodeToString(h.Sum(nil))
	}

	// The signature base: method, base URL and every parameter, sorted.
	type kv struct{ k, v string }
	var all []kv
	for k, v := range oauth {
		all = append(all, kv{percent(k), percent(v)})
	}
	for _, part := range strings.Split(u.RawQuery, "&") {
		if part == "" {
			continue
		}
		k, v, _ := strings.Cut(part, "=")
		k, _ = url.QueryUnescape(k)
		v, _ = url.QueryUnescape(v)
		all = append(all, kv{percent(k), percent(v)})
	}
	if strings.HasPrefix(strings.ToLower(p.Get("Content-Type")), "application/x-www-form-urlencoded") {
		form, _ := url.ParseQuery(string(p.Body))
		for k, vs := range form {
			for _, v := range vs {
				all = append(all, kv{percent(k), percent(v)})
			}
		}
	}
	sort.Slice(all, func(a, b int) bool {
		if all[a].k != all[b].k {
			return all[a].k < all[b].k
		}
		return all[a].v < all[b].v
	})
	pairs := make([]string, len(all))
	for i, e := range all {
		pairs[i] = e.k + "=" + e.v
	}
	scheme, host := strings.ToLower(u.Scheme), strings.ToLower(u.Host)
	if scheme == "http" && strings.HasSuffix(host, ":80") || scheme == "https" && strings.HasSuffix(host, ":443") {
		host = host[:strings.LastIndex(host, ":")]
	}
	baseURL := scheme + "://" + host + u.EscapedPath()
	base := strings.ToUpper(p.Method) + "&" + percent(baseURL) + "&" + percent(strings.Join(pairs, "&"))
	signKey := percent(prm.Str("consumerSecret")) + "&" + percent(prm.Str("tokenSecret"))

	var sig string
	switch method {
	case "HMAC-SHA1", "HMAC-SHA256", "HMAC-SHA512":
		newHash := sha1.New
		if method == "HMAC-SHA256" {
			newHash = sha256.New
		} else if method == "HMAC-SHA512" {
			newHash = sha512.New
		}
		mac := hmac.New(newHash, []byte(signKey))
		mac.Write([]byte(base))
		sig = base64.StdEncoding.EncodeToString(mac.Sum(nil))
	case "PLAINTEXT":
		sig = signKey
	case "RSA-SHA1", "RSA-SHA256", "RSA-SHA512":
		key, err := signingKey("RS256", "", false, prm.Str("privateKey"))
		if err != nil {
			return err
		}
		ch, h := crypto.SHA1, sha1.New() //nolint:gosec
		if method == "RSA-SHA256" {
			ch, h = crypto.SHA256, sha256.New()
		} else if method == "RSA-SHA512" {
			ch, h = crypto.SHA512, sha512.New()
		}
		h.Write([]byte(base))
		rsaKey, ok := key.(*rsa.PrivateKey)
		if !ok {
			return fmt.Errorf("%s needs an RSA private key", method)
		}
		raw, err := rsa.SignPKCS1v15(rand.Reader, rsaKey, ch, h.Sum(nil))
		if err != nil {
			return err
		}
		sig = base64.StdEncoding.EncodeToString(raw)
	default:
		return fmt.Errorf("posthero doesn't know OAuth 1.0 signature method %s", method)
	}
	oauth["oauth_signature"] = sig

	names := make([]string, 0, len(oauth))
	for k := range oauth {
		names = append(names, k)
	}
	sort.Strings(names)
	addToHeader := true
	if raw, ok := prm.raw["addParamsToHeader"]; ok && strings.TrimSpace(string(raw)) == "false" {
		addToHeader = false
	}
	if !addToHeader {
		for _, k := range names {
			p.AddQuery(k, oauth[k])
		}
		return nil
	}
	var parts []string
	if realm := prm.Str("realm"); realm != "" {
		parts = append(parts, `realm="`+realm+`"`)
	}
	for _, k := range names {
		v := oauth[k]
		if !prm.Bool("disableHeaderEncoding") {
			v = percent(v)
		}
		parts = append(parts, k+`="`+v+`"`)
	}
	p.Set("Authorization", "OAuth "+strings.Join(parts, ","), true)
	return nil
}

// awsv4 signs a request with AWS Signature Version 4.
func awsv4(p *send.Prepared, prm Params) error {
	u, err := url.Parse(p.URL)
	if err != nil {
		return err
	}
	region := prm.Or("region", "us-east-1")
	service := prm.Or("service", "execute-api")
	t := now().UTC()
	amzDate := t.Format("20060102T150405Z")
	day := t.Format("20060102")
	scope := day + "/" + region + "/" + service + "/aws4_request"
	sum := sha256.Sum256(body(p))
	payloadHash := hex.EncodeToString(sum[:])
	inQuery := prm.Bool("addAuthDataToQuery")
	token := prm.Str("sessionToken")
	host := u.Host
	if h := p.Get("Host"); h != "" {
		host = h
	}
	if (u.Scheme == "https" && strings.HasSuffix(host, ":443")) || (u.Scheme == "http" && strings.HasSuffix(host, ":80")) {
		host = host[:strings.LastIndex(host, ":")]
	}

	headers := map[string]string{"host": host}
	if !inQuery {
		headers["x-amz-date"] = amzDate
		if token != "" {
			headers["x-amz-security-token"] = token
		}
		if service == "s3" {
			headers["x-amz-content-sha256"] = payloadHash
		}
	}
	for _, h := range p.Header {
		name := strings.ToLower(h.Name)
		if name == "content-type" || strings.HasPrefix(name, "x-amz-") {
			headers[name] = strings.Join(strings.Fields(h.Value), " ")
		}
	}
	names := make([]string, 0, len(headers))
	for k := range headers {
		names = append(names, k)
	}
	sort.Strings(names)
	signed := strings.Join(names, ";")

	query := u.Query()
	if inQuery {
		query.Set("X-Amz-Algorithm", "AWS4-HMAC-SHA256")
		query.Set("X-Amz-Credential", prm.Str("accessKey")+"/"+scope)
		query.Set("X-Amz-Date", amzDate)
		query.Set("X-Amz-Expires", "86400")
		query.Set("X-Amz-SignedHeaders", signed)
		if token != "" {
			query.Set("X-Amz-Security-Token", token)
		}
	}
	var canonHeaders strings.Builder
	for _, k := range names {
		canonHeaders.WriteString(k + ":" + strings.TrimSpace(headers[k]) + "\n")
	}
	path := u.EscapedPath()
	if path == "" {
		path = "/"
	}
	if service != "s3" {
		// Every service but S3 signs the path encoded twice.
		segs := strings.Split(path, "/")
		for i, s := range segs {
			segs[i] = percent(s)
		}
		path = strings.Join(segs, "/")
		path = strings.ReplaceAll(path, "%2F", "/")
	}
	if service == "s3" && inQuery {
		payloadHash = "UNSIGNED-PAYLOAD"
	}
	canonical := strings.Join([]string{p.Method, path, canonicalQuery(query), canonHeaders.String(), signed, payloadHash}, "\n")
	ch := sha256.Sum256([]byte(canonical))
	toSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + hex.EncodeToString(ch[:])
	k := hmacSHA256([]byte("AWS4"+prm.Str("secretKey")), day)
	k = hmacSHA256(k, region)
	k = hmacSHA256(k, service)
	k = hmacSHA256(k, "aws4_request")
	sig := hex.EncodeToString(hmacSHA256(k, toSign))

	if inQuery {
		query.Set("X-Amz-Signature", sig)
		base, _, hash := postman.SplitRaw(p.URL)
		p.URL = base + "?" + canonicalQuery(query)
		if hash != "" {
			p.URL += "#" + hash
		}
		return nil
	}
	p.Set("X-Amz-Date", amzDate, true)
	if token != "" {
		p.Set("X-Amz-Security-Token", token, true)
	}
	if service == "s3" {
		p.Set("X-Amz-Content-Sha256", payloadHash, true)
	}
	p.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+prm.Str("accessKey")+"/"+scope+", SignedHeaders="+signed+", Signature="+sig, true)
	return nil
}

func canonicalQuery(q url.Values) string {
	type kv struct{ k, v string }
	var all []kv
	for k, vs := range q {
		for _, v := range vs {
			all = append(all, kv{percent(k), percent(v)})
		}
	}
	sort.Slice(all, func(a, b int) bool {
		if all[a].k != all[b].k {
			return all[a].k < all[b].k
		}
		return all[a].v < all[b].v
	})
	parts := make([]string, len(all))
	for i, e := range all {
		parts[i] = e.k + "=" + e.v
	}
	return strings.Join(parts, "&")
}

func hmacSHA256(key []byte, data string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(data))
	return m.Sum(nil)
}

// hawk adds a Hawk authorization header.
func hawk(p *send.Prepared, prm Params) error {
	u, err := url.Parse(p.URL)
	if err != nil {
		return err
	}
	newHash := sha256.New
	if strings.EqualFold(prm.Str("algorithm"), "sha1") {
		newHash = sha1.New
	}
	ts := prm.Or("timestamp", strconv.FormatInt(now().Unix(), 10))
	n := prm.Or("nonce", nonce(6))
	port := u.Port()
	if port == "" {
		port = "80"
		if u.Scheme == "https" {
			port = "443"
		}
	}
	resource := u.EscapedPath()
	if resource == "" {
		resource = "/"
	}
	if u.RawQuery != "" {
		resource += "?" + u.RawQuery
	}
	payloadHash := ""
	if prm.Bool("includePayloadHash") {
		ct, _, _ := strings.Cut(p.Get("Content-Type"), ";")
		h := newHash()
		h.Write([]byte("hawk.1.payload\n" + strings.ToLower(strings.TrimSpace(ct)) + "\n"))
		h.Write(body(p))
		h.Write([]byte("\n"))
		payloadHash = base64.StdEncoding.EncodeToString(h.Sum(nil))
	}
	ext, app, dlg := prm.Str("extraData"), prm.Str("app"), prm.Str("delegation")
	normalized := "hawk.1.header\n" + ts + "\n" + n + "\n" + strings.ToUpper(p.Method) + "\n" + resource + "\n" + strings.ToLower(u.Hostname()) + "\n" + port + "\n" + payloadHash + "\n" + strings.NewReplacer(`\`, `\\`, "\n", `\n`).Replace(ext) + "\n"
	if app != "" {
		normalized += app + "\n" + dlg + "\n"
	}
	mac := hmac.New(newHash, []byte(prm.Str("authKey")))
	mac.Write([]byte(normalized))
	header := fmt.Sprintf(`Hawk id="%s", ts="%s", nonce="%s"`, prm.Str("authId"), ts, n)
	if payloadHash != "" {
		header += `, hash="` + payloadHash + `"`
	}
	if ext != "" {
		header += `, ext="` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(ext) + `"`
	}
	header += `, mac="` + base64.StdEncoding.EncodeToString(mac.Sum(nil)) + `"`
	if app != "" {
		header += `, app="` + app + `"`
		if dlg != "" {
			header += `, dlg="` + dlg + `"`
		}
	}
	p.Set("Authorization", header, true)
	return nil
}

// edgegrid signs a request for Akamai's APIs (EG1-HMAC-SHA256).
func edgegrid(p *send.Prepared, prm Params) error {
	u, err := url.Parse(p.URL)
	if err != nil {
		return err
	}
	if base := prm.Str("baseURL"); base != "" {
		if b, err := url.Parse(base); err == nil && b.Host != "" {
			u.Scheme, u.Host = b.Scheme, b.Host
			p.URL = u.String()
		}
	}
	ts := prm.Or("timestamp", now().UTC().Format("20060102T15:04:05+0000"))
	n := prm.Or("nonce", nonceUUID())
	authHeader := fmt.Sprintf("EG1-HMAC-SHA256 client_token=%s;access_token=%s;timestamp=%s;nonce=%s;", prm.Str("clientToken"), prm.Str("accessToken"), ts, n)
	var canon []string
	for _, name := range strings.Split(prm.Str("headersToSign"), ",") {
		name = strings.TrimSpace(name)
		if v := p.Get(name); name != "" && v != "" {
			canon = append(canon, strings.ToLower(name)+":"+strings.Join(strings.Fields(v), " "))
		}
	}
	contentHash := ""
	if p.Method == "POST" {
		data := body(p)
		if max := prm.Int("maxBodySize", 131072); len(data) > max {
			data = data[:max]
		}
		if len(data) > 0 {
			sum := sha256.Sum256(data)
			contentHash = base64.StdEncoding.EncodeToString(sum[:])
		}
	}
	path := u.EscapedPath()
	if u.RawQuery != "" {
		path += "?" + u.RawQuery
	}
	data := strings.Join([]string{p.Method, u.Scheme, u.Host, path, strings.Join(canon, "\t"), contentHash, authHeader}, "\t")
	key := base64.StdEncoding.EncodeToString(hmacSHA256([]byte(prm.Str("clientSecret")), ts))
	sig := base64.StdEncoding.EncodeToString(hmacSHA256([]byte(key), data))
	p.Set("Authorization", authHeader+"signature="+sig, true)
	return nil
}

func nonceUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	h := hex.EncodeToString(b)
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

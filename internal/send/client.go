package send

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/andybalholm/brotli"

	"github.com/shinyvision/posthero/internal/postman"
)

// Response is what came back, with how long it took.
type Response struct {
	Status     int
	StatusText string
	Proto      string
	Header     []Header
	Body       []byte
	// Truncated says the body was cut at the maximum response size: more
	// came than that, over the wire or once decoded.
	Truncated bool
	// WireSize is the body as it came over the wire (compressed), and
	// HeaderSize the size of the status line and headers.
	WireSize, HeaderSize int64
	Time                 time.Duration
	Timings              Timings
	Redirects            []Redirect
	// URL is where the response came from, after redirects.
	URL string
	// Sent is the request as it was last sent (after redirects), with the
	// cookies the jar added.
	Sent *Prepared
	// Cookies are the cookies the response set.
	Cookies []*http.Cookie
	TLS     *TLSInfo
	Remote  string
	At      time.Time
}

// IncompleteError says a response's body stopped before its end: the
// connection closed, the time ran out or the request was cancelled. Do
// returns it together with the response, whose status and headers came
// whole and whose Body is what arrived. errors.Is sees through it to why:
// context.Canceled when the request was cancelled, and
// context.DeadlineExceeded when the time ran out.
type IncompleteError struct {
	// Got is how much of the body arrived, and Want how much the server
	// said it would send (-1 when it didn't say), both as over the wire.
	Got, Want int64
	// Err is why it stopped.
	Err error
}

func (e *IncompleteError) Error() string {
	of := ""
	if e.Want >= 0 {
		of = " of " + Size(e.Want)
	}
	return fmt.Sprintf("the body stopped after %s%s: %v", Size(e.Got), of, e.Err)
}

func (e *IncompleteError) Unwrap() error { return e.Err }

// because is an error that reads as msg and is err to errors.Is and
// errors.As.
type because struct {
	msg string
	err error
}

func (b *because) Error() string { return b.msg }
func (b *because) Unwrap() error { return b.err }

// Redirect is a hop on the way.
type Redirect struct {
	Status int
	From   string
	To     string
}

// Timings are the phases of the last hop.
type Timings struct {
	DNS, Connect, TLS, Wait, Download time.Duration
	// Reused says the connection was already open.
	Reused bool
}

// TLSInfo describes the secure connection.
type TLSInfo struct {
	Version, Cipher string
	Subject, Issuer string
	NotAfter        time.Time
	Verified        bool
}

// Get returns a response header's value, ignoring case.
func (r *Response) Get(name string) string {
	for _, h := range r.Header {
		if strings.EqualFold(h.Name, name) {
			return h.Value
		}
	}
	return ""
}

// ContentType is the response's media type without parameters.
func (r *Response) ContentType() string {
	ct, _, _ := strings.Cut(r.Get("Content-Type"), ";")
	return strings.ToLower(strings.TrimSpace(ct))
}

// Size is the response's size: headers and body as received.
func (r *Response) Size() int64 { return r.HeaderSize + r.WireSize }

// Client sends prepared requests.
type Client struct {
	Jar *Jar

	mu         sync.Mutex
	transports map[string]*transportSet
}

// NewClient returns a client with a cookie jar (which may be nil).
func NewClient(jar *Jar) *Client {
	return &Client{Jar: jar, transports: map[string]*transportSet{}}
}

// transportSet sends requests with one set of settings. A host with a
// client certificate gets a transport of its own, whose TLS config presents
// it: a TLS config can't choose a certificate by host, and through a proxy
// the transport does the handshake in the tunnel itself, past any dial of
// ours.
type transportSet struct {
	base   *http.Transport
	byHost map[string]*http.Transport // "host:port" or "host", lower case
}

// pick is the transport for a request to u.
func (ts *transportSet) pick(u *url.URL) *http.Transport {
	if len(ts.byHost) > 0 && strings.EqualFold(u.Scheme, "https") {
		host, port := strings.ToLower(u.Hostname()), u.Port()
		if port == "" {
			port = "443"
		}
		if t, ok := ts.byHost[net.JoinHostPort(host, port)]; ok {
			return t
		}
		if t, ok := ts.byHost[host]; ok {
			return t
		}
	}
	return ts.base
}

// RoundTrip sends one request as it is.
func (ts *transportSet) RoundTrip(req *http.Request) (*http.Response, error) {
	return ts.pick(req.URL).RoundTrip(req)
}

// Transport is what requests with settings s go through, for code that
// talks HTTP itself (as OAuth 2.0's token requests do): the same
// certificate checks, CA file, proxy and client certificates, and the same
// open connections. Only those settings matter. It sends each request as
// it is: it follows no redirects and adds no cookies.
func (c *Client) Transport(s Settings) (http.RoundTripper, error) {
	ts, err := c.transport(s)
	if err != nil {
		return nil, err
	}
	return ts, nil
}

// transport returns the transports for the settings, reusing connections
// between requests with the same settings.
func (c *Client) transport(s Settings) (*transportSet, error) {
	key := fmt.Sprintf("%v|%v|%s|%s|%v|%v", s.StrictSSL, s.HTTP1, s.Proxy, s.CAFile, s.TLSDisabled, s.Certs)
	c.mu.Lock()
	defer c.mu.Unlock()
	if ts, ok := c.transports[key]; ok {
		return ts, nil
	}
	tc := &tls.Config{InsecureSkipVerify: !s.StrictSSL} //nolint:gosec // the user's choice, as in Postman
	tc.MinVersion = tls.VersionTLS10
	for _, v := range s.TLSDisabled {
		switch strings.ToLower(strings.TrimPrefix(strings.ToLower(v), "tlsv")) {
		case "1", "1.0":
			tc.MinVersion = max(tc.MinVersion, tls.VersionTLS11)
		case "1.1":
			tc.MinVersion = max(tc.MinVersion, tls.VersionTLS12)
		case "1.2":
			tc.MinVersion = max(tc.MinVersion, tls.VersionTLS13)
		}
	}
	if s.CAFile != "" {
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		data, err := os.ReadFile(s.CAFile)
		if err != nil {
			return nil, fmt.Errorf("the CA file: %w", err)
		}
		if !pool.AppendCertsFromPEM(data) {
			return nil, fmt.Errorf("the CA file %s has no PEM certificates", s.CAFile)
		}
		tc.RootCAs = pool
	}
	var proxy func(*http.Request) (*url.URL, error) // nil: $HTTPS_PROXY and friends
	if s.Proxy != "" {
		raw := s.Proxy
		if !strings.Contains(raw, "://") {
			raw = "http://" + raw // proxy.corp:3128, as $HTTPS_PROXY and curl take it
		}
		pu, err := url.Parse(raw)
		if err != nil {
			return nil, fmt.Errorf("the proxy %q isn't a URL: %w", s.Proxy, err)
		}
		if pu.Host == "" {
			return nil, fmt.Errorf("the proxy %q has no host", s.Proxy)
		}
		proxy = http.ProxyURL(pu)
	}
	newTransport := func(cfg *tls.Config) *http.Transport {
		t := http.DefaultTransport.(*http.Transport).Clone()
		t.DisableCompression = true // posthero decodes, to report sizes
		t.ResponseHeaderTimeout = 0
		t.TLSClientConfig = cfg
		if s.HTTP1 {
			t.ForceAttemptHTTP2 = false
			t.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}
		}
		if proxy != nil {
			t.Proxy = proxy
		}
		return t
	}
	ts := &transportSet{base: newTransport(tc)}
	for _, cc := range s.Certs {
		cert, err := loadClientCert(cc)
		if err != nil {
			return nil, err
		}
		withCert := tc.Clone()
		withCert.Certificates = []tls.Certificate{cert}
		if ts.byHost == nil {
			ts.byHost = map[string]*http.Transport{}
		}
		ts.byHost[strings.ToLower(cc.Host)] = newTransport(withCert)
	}
	c.transports[key] = ts
	return ts, nil
}

func loadClientCert(cc ClientCert) (tls.Certificate, error) {
	if cc.PFX != "" {
		return tls.Certificate{}, fmt.Errorf("client certificate for %s: PFX files aren't supported yet; convert it to PEM (openssl pkcs12 -in file.pfx -out cert.pem -nodes)", cc.Host)
	}
	cert, err := tls.LoadX509KeyPair(cc.Cert, cc.Key)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("client certificate for %s: %w", cc.Host, err)
	}
	return cert, nil
}

// Do sends a request, following redirects as its settings say. When the
// body stops before its end, Do returns the response with what arrived
// together with an *IncompleteError; with any other error there is no
// response.
func (c *Client) Do(ctx context.Context, p *Prepared) (*Response, error) {
	ts, err := c.transport(p.Settings)
	if err != nil {
		return nil, err
	}
	if p.Settings.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, p.Settings.Timeout)
		defer cancel()
	}
	start := time.Now()
	cur := *p
	cur.Header = append([]Header(nil), p.Header...)
	var redirects []Redirect
	for hop := 0; ; hop++ {
		resp, err := c.once(ctx, ts, &cur)
		if resp != nil && err != nil {
			resp.Redirects = redirects
			resp.Time = time.Since(start)
			resp.At = start
			return resp, err
		}
		if err != nil {
			if ctx.Err() == context.DeadlineExceeded && p.Settings.Timeout > 0 {
				return nil, fmt.Errorf("no response within %s (:set timeout changes that)", p.Settings.Timeout)
			}
			return nil, err
		}
		next, ok := redirectTarget(resp, &cur)
		if !ok || !p.Settings.FollowRedirects {
			resp.Redirects = redirects
			resp.Time = time.Since(start)
			resp.At = start
			return resp, nil
		}
		if hop >= p.Settings.MaxRedirects {
			return nil, fmt.Errorf("stopped after %d redirects (maxRedirects); the last went to %s", hop, next)
		}
		redirects = append(redirects, Redirect{Status: resp.Status, From: cur.URL, To: next})
		cur = followed(&cur, resp.Status, next, p.Settings)
	}
}

// redirectTarget is where a redirect response sends the request.
func redirectTarget(resp *Response, p *Prepared) (string, bool) {
	switch resp.Status {
	case 301, 302, 303, 307, 308:
	default:
		return "", false
	}
	loc := resp.Get("Location")
	if loc == "" {
		return "", false
	}
	base, err := url.Parse(p.URL)
	if err != nil {
		return "", false
	}
	next, err := base.Parse(loc)
	if err != nil {
		return "", false
	}
	return next.String(), true
}

// followed is the request to send to a redirect's target.
func followed(p *Prepared, status int, next string, s Settings) Prepared {
	out := *p
	out.URL = next
	out.Header = nil
	from, _ := url.Parse(p.URL)
	to, _ := url.Parse(next)
	// Credentials stay with their origin: the same scheme, host and port.
	sameOrigin := from != nil && to != nil && strings.EqualFold(from.Scheme, to.Scheme) && strings.EqualFold(from.Host, to.Host)
	// 307 and 308 keep the method and the body. 303 becomes a GET; 301 and
	// 302 too, as in browsers, unless followOriginalHttpMethod says to keep
	// the method (and then the body).
	keepBody := status == 307 || status == 308
	switch {
	case keepBody || p.Method == "HEAD":
	case status == 303:
		out.Method = "GET"
	case s.FollowOriginalMethod:
		keepBody = true
	case p.Method != "GET":
		out.Method = "GET"
	}
	if !keepBody {
		out.Body, out.BodyFile, out.BodyText = nil, "", ""
	}
	for _, h := range p.Header {
		name := strings.ToLower(h.Name)
		switch {
		case name == "cookie" && sameOrigin && !h.System:
			// A cookie you set yourself goes along, within the origin.
		case name == "cookie" || name == "host" || name == "referer":
			continue // the jar decides again; the referer is the last hop
		case (h.Auth || name == "authorization" || name == "proxy-authorization") && !sameOrigin && !s.FollowAuthHeader:
			continue
		case strings.HasPrefix(name, "content-") && !keepBody:
			continue
		}
		out.Header = append(out.Header, h)
	}
	if !s.RemoveRefererOnRedirect {
		if ref := referer(from, to); ref != "" {
			out.Header = append(out.Header, Header{Name: "Referer", Value: ref, System: true})
		}
	}
	return out
}

// referer is what a redirect from one address to another says it came
// from, as browsers do: the whole address within the origin, the origin
// alone to another one, and nothing from https to http.
func referer(from, to *url.URL) string {
	if from == nil || to == nil || from.Scheme == "https" && to.Scheme != "https" {
		return ""
	}
	r := *from
	r.User, r.Fragment, r.RawFragment = nil, "", ""
	if !strings.EqualFold(from.Scheme, to.Scheme) || !strings.EqualFold(from.Host, to.Host) {
		return from.Scheme + "://" + from.Host + "/"
	}
	return r.String()
}

// once sends one hop. When the body stops before its end, the response
// comes with an *IncompleteError.
func (c *Client) once(ctx context.Context, ts *transportSet, p *Prepared) (*Response, error) {
	u, err := url.Parse(p.URL)
	if err != nil {
		return nil, err
	}
	t := ts.pick(u)
	var body io.Reader
	var length int64 = -1
	switch {
	case p.BodyFile != "":
		f, err := os.Open(p.BodyFile)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		if fi, err := f.Stat(); err == nil {
			length = fi.Size()
		}
		body = f
	case p.Body != nil:
		body, length = bytes.NewReader(p.Body), int64(len(p.Body))
	}
	req, err := http.NewRequestWithContext(ctx, p.Method, p.URL, body)
	if err != nil {
		return nil, err
	}
	// The URL goes out exactly as typed and encoded, not as Go would
	// escape it again. Through a proxy (for plain http) the request names
	// the whole address, and so does a path starting with //, which would
	// otherwise read as a host.
	path := rawPath(p.URL)
	req.URL.Opaque = path
	if strings.HasPrefix(path, "//") || u.Scheme == "http" && usesProxy(t, req) {
		req.URL.Opaque = "//" + u.Host + path
	}
	req.URL.RawQuery = u.RawQuery
	if length >= 0 {
		req.ContentLength = length
	}
	for _, h := range p.Header {
		switch strings.ToLower(h.Name) {
		case "host":
			req.Host = h.Value
			continue
		case "content-length":
			continue
		case "connection":
			if strings.EqualFold(h.Value, "close") {
				req.Close = true
			}
			continue
		}
		req.Header.Add(h.Name, h.Value)
	}
	if c.Jar != nil && !p.Settings.DisableCookies {
		if jarCookies := c.Jar.Cookies(u); len(jarCookies) > 0 {
			existing := req.Header.Get("Cookie")
			have := map[string]bool{}
			for _, part := range strings.Split(existing, ";") {
				if k, _, ok := strings.Cut(strings.TrimSpace(part), "="); ok {
					have[k] = true
				}
			}
			var parts []string
			if existing != "" {
				parts = append(parts, existing)
			}
			for _, ck := range jarCookies {
				if !have[ck.Name] {
					parts = append(parts, ck.Name+"="+ck.Value)
				}
			}
			req.Header.Set("Cookie", strings.Join(parts, "; "))
		}
	}

	// The trace's hooks run on the transport's goroutines, some even after
	// RoundTrip returns (an HTTP/2 request's write, a dial that another
	// request took over), so they keep to a lock.
	var mu sync.Mutex
	hook := func(f func()) { mu.Lock(); defer mu.Unlock(); f() }
	var traced Timings
	var dnsStart, connStart, tlsStart, wrote, first time.Time
	var remote string
	trace := &httptrace.ClientTrace{
		DNSStart:          func(httptrace.DNSStartInfo) { hook(func() { dnsStart = time.Now() }) },
		DNSDone:           func(httptrace.DNSDoneInfo) { hook(func() { traced.DNS = time.Since(dnsStart) }) },
		ConnectStart:      func(string, string) { hook(func() { connStart = time.Now() }) },
		ConnectDone:       func(string, string, error) { hook(func() { traced.Connect = time.Since(connStart) }) },
		TLSHandshakeStart: func() { hook(func() { tlsStart = time.Now() }) },
		TLSHandshakeDone:  func(tls.ConnectionState, error) { hook(func() { traced.TLS = time.Since(tlsStart) }) },
		GotConn: func(info httptrace.GotConnInfo) {
			hook(func() {
				traced.Reused = info.Reused
				if info.Conn != nil {
					remote = info.Conn.RemoteAddr().String()
				}
			})
		},
		WroteRequest:         func(httptrace.WroteRequestInfo) { hook(func() { wrote = time.Now() }) },
		GotFirstResponseByte: func() { hook(func() { first = time.Now() }) },
	}
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), trace))
	resp, err := t.RoundTrip(req)
	if err != nil {
		return nil, explain(err, u)
	}
	defer resp.Body.Close()
	mu.Lock()
	tm := traced
	if !wrote.IsZero() && !first.IsZero() {
		tm.Wait = first.Sub(wrote)
	}
	out := &Response{Status: resp.StatusCode, Proto: resp.Proto, URL: p.URL, Remote: remote}
	mu.Unlock()
	out.StatusText = strings.TrimSpace(strings.TrimPrefix(resp.Status, fmt.Sprint(resp.StatusCode)))
	if out.StatusText == "" {
		out.StatusText = http.StatusText(resp.StatusCode)
	}
	names := make([]string, 0, len(resp.Header))
	for k := range resp.Header {
		names = append(names, k)
	}
	sort.Strings(names)
	headerSize := int64(len(resp.Proto) + len(resp.Status) + 4)
	for _, k := range names {
		for _, v := range resp.Header[k] {
			out.Header = append(out.Header, Header{Name: k, Value: v})
			headerSize += int64(len(k) + len(v) + 4)
		}
	}
	out.HeaderSize = headerSize

	limit := p.Settings.MaxResponseSize
	counter := &countingReader{r: resp.Body}
	var reader io.Reader = counter
	if limit > 0 {
		reader = io.LimitReader(reader, limit+1)
	}
	readStart := time.Now()
	data, readErr := io.ReadAll(reader)
	tm.Download = time.Since(readStart)
	// More than the limit came over the wire: cut, whatever it decodes to.
	cut := limit > 0 && int64(len(data)) > limit
	if p.Method != "HEAD" && resp.StatusCode != 204 && resp.StatusCode != 304 {
		data = decoded(strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Encoding"))), data, limit)
	}
	if limit > 0 && int64(len(data)) > limit {
		data, cut = data[:limit], true
	}
	out.Body, out.Truncated = data, cut
	out.WireSize = counter.n
	out.Timings = tm
	out.Cookies = resp.Cookies()
	if c.Jar != nil && !p.Settings.DisableCookies {
		c.Jar.SetCookies(u, out.Cookies)
	}
	sent := *p
	sent.Header = append([]Header(nil), p.Header...)
	if ck := req.Header.Get("Cookie"); ck != "" {
		sent.Set("Cookie", ck, false)
		for i := range sent.Header {
			if strings.EqualFold(sent.Header[i].Name, "Cookie") {
				sent.Header[i].System = true
			}
		}
	}
	out.Sent = &sent
	if st := resp.TLS; st != nil {
		info := &TLSInfo{Version: tls.VersionName(st.Version), Cipher: tls.CipherSuiteName(st.CipherSuite), Verified: len(st.VerifiedChains) > 0}
		if len(st.PeerCertificates) > 0 {
			cert := st.PeerCertificates[0]
			info.Subject, info.Issuer, info.NotAfter = cert.Subject.CommonName, cert.Issuer.CommonName, cert.NotAfter
		}
		out.TLS = info
	}
	if readErr != nil {
		// The status and headers came whole, so they stay, with what
		// arrived of the body; but it isn't the whole response.
		return out, &IncompleteError{Got: counter.n, Want: resp.ContentLength, Err: stopped(ctx, readErr, p.Settings)}
	}
	return out, nil
}

// stopped says why a body stopped coming: cancelled, out of time, the
// connection closed, or the error itself. It stays the error it was to
// errors.Is.
func stopped(ctx context.Context, err error, s Settings) error {
	switch cerr := ctx.Err(); {
	case errors.Is(cerr, context.Canceled):
		return &because{"cancelled", cerr}
	case errors.Is(cerr, context.DeadlineExceeded) && s.Timeout > 0:
		return &because{fmt.Sprintf("no complete response within %s (:set timeout changes that)", s.Timeout), cerr}
	case cerr != nil:
		return cerr
	case errors.Is(err, io.ErrUnexpectedEOF):
		return &because{"the connection closed", err}
	}
	return err
}

// rawPath is the path of a URL as written, "/" when there is none.
// usesProxy reports whether the transport sends a request through a proxy.
func usesProxy(t *http.Transport, req *http.Request) bool {
	if t.Proxy == nil {
		return false
	}
	pu, err := t.Proxy(req)
	return err == nil && pu != nil
}

// decoded undoes a response's Content-Encoding. A body that isn't encoded
// the way the header says is kept as it came; a stream cut short keeps
// what could be decoded.
func decoded(encoding string, raw []byte, limit int64) []byte {
	if len(raw) == 0 {
		return raw
	}
	read := func(r io.Reader, err error) ([]byte, bool) {
		if err != nil {
			return nil, false
		}
		if limit > 0 {
			r = io.LimitReader(r, limit+1)
		}
		out, err := io.ReadAll(r)
		return out, err == nil || len(out) > 0 && errors.Is(err, io.ErrUnexpectedEOF)
	}
	switch encoding {
	case "gzip", "x-gzip":
		if out, ok := read(gzip.NewReader(bytes.NewReader(raw))); ok {
			return out
		}
	case "deflate":
		// Servers send zlib's format, as the standard says; some send raw
		// deflate.
		if out, ok := read(zlib.NewReader(bytes.NewReader(raw))); ok {
			return out
		}
		if out, ok := read(flate.NewReader(bytes.NewReader(raw)), nil); ok {
			return out
		}
	case "br":
		if out, ok := read(brotli.NewReader(bytes.NewReader(raw)), nil); ok {
			return out
		}
	}
	return raw
}

func rawPath(raw string) string {
	base, _, _ := postman.SplitRaw(raw)
	if i := strings.Index(base, "://"); i >= 0 {
		base = base[i+3:]
	}
	i := strings.IndexByte(base, '/')
	if i < 0 {
		return "/"
	}
	return base[i:]
}

type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// explain makes network errors say what to check.
func explain(err error, u *url.URL) error {
	var dnsErr *net.DNSError
	var opErr *net.OpError
	var certErr *tls.CertificateVerificationError
	var unknownAuth x509.UnknownAuthorityError
	var hostErr x509.HostnameError
	switch {
	case errors.Is(err, context.Canceled):
		return &because{"cancelled", err}
	case errors.As(err, &dnsErr):
		return fmt.Errorf("couldn't find %s: %v", u.Hostname(), dnsErr.Err)
	case errors.As(err, &certErr), errors.As(err, &unknownAuth), errors.As(err, &hostErr):
		return fmt.Errorf("%s's certificate isn't trusted (%v); :set noverify sends without checking it", u.Host, err)
	case errors.As(err, &opErr) && opErr.Op == "dial":
		return fmt.Errorf("couldn't connect to %s: is it running? (%v)", u.Host, opErr.Err)
	}
	return err
}

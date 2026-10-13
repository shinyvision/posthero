package send

import (
	"encoding/json"
	"strings"
	"time"
)

// Settings decide how a request is sent. Global ones come from :set; a
// request's own (Postman's protocolProfileBehavior) win.
type Settings struct {
	FollowRedirects         bool
	FollowOriginalMethod    bool
	FollowAuthHeader        bool
	RemoveRefererOnRedirect bool
	MaxRedirects            int
	// StrictSSL checks the server's certificate.
	StrictSSL          bool
	DisableBodyPruning bool
	DisableURLEncoding bool
	DisableCookies     bool
	// DisabledSystemHeaders are default headers not to send, lower case.
	DisabledSystemHeaders map[string]bool
	// HTTP1 sends HTTP/1.1 only, never HTTP/2.
	HTTP1 bool
	// TLSDisabled lists TLS versions not to use ("TLSv1", "TLSv1.1", …).
	TLSDisabled []string
	// Timeout ends a request that takes longer; 0 waits forever.
	Timeout time.Duration
	// MaxResponseSize stops reading a response at this many bytes; 0 reads
	// it all.
	MaxResponseSize int64
	// NoCacheHeaderOff leaves out Cache-Control: no-cache.
	NoCacheHeaderOff bool
	// Proxy is a proxy URL to use instead of $HTTPS_PROXY and friends.
	Proxy string
	// CAFile adds certificate authorities to trust.
	CAFile string
	// Certs are client certificates by host.
	Certs []ClientCert
}

// ClientCert is a client certificate for a host (and port, when set).
type ClientCert struct {
	Host       string
	Cert, Key  string // PEM files
	PFX        string // or a PKCS#12 file
	Passphrase string
}

// DefaultSettings are Postman's defaults.
func DefaultSettings() Settings {
	return Settings{FollowRedirects: true, MaxRedirects: 10, StrictSSL: true, MaxResponseSize: 50 << 20}
}

// With returns s changed by a request's protocolProfileBehavior.
func (s Settings) With(raw json.RawMessage) Settings {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return s
	}
	b := func(key string, dst *bool) {
		if v, ok := m[key]; ok {
			var x bool
			if json.Unmarshal(v, &x) == nil {
				*dst = x
			}
		}
	}
	b("followRedirects", &s.FollowRedirects)
	b("followOriginalHttpMethod", &s.FollowOriginalMethod)
	b("followAuthorizationHeader", &s.FollowAuthHeader)
	b("removeRefererHeaderOnRedirect", &s.RemoveRefererOnRedirect)
	b("strictSSL", &s.StrictSSL)
	b("disableBodyPruning", &s.DisableBodyPruning)
	b("disableUrlEncoding", &s.DisableURLEncoding)
	b("disableCookies", &s.DisableCookies)
	if v, ok := m["maxRedirects"]; ok {
		var n int
		if json.Unmarshal(v, &n) == nil {
			s.MaxRedirects = n
		}
	}
	if v, ok := m["disabledSystemHeaders"]; ok {
		var hs map[string]bool
		if json.Unmarshal(v, &hs) == nil {
			s.DisabledSystemHeaders = map[string]bool{}
			for k, on := range hs {
				if on {
					s.DisabledSystemHeaders[strings.ToLower(k)] = true
				}
			}
		}
	}
	if v, ok := m["protocolVersion"]; ok {
		var pv string
		if json.Unmarshal(v, &pv) == nil {
			s.HTTP1 = pv == "http1"
		}
	}
	if v, ok := m["tlsDisabledProtocols"]; ok {
		_ = json.Unmarshal(v, &s.TLSDisabled)
	}
	return s
}

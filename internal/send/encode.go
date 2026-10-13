package send

import (
	"strings"

	"github.com/shinyvision/posthero/internal/postman"
)

// EncodeURL percent-encodes what may not appear in a URL as typed (spaces,
// quotes, non-ASCII, …) and leaves everything else, %XX sequences
// included, as it is, like Postman.
func EncodeURL(raw string) string {
	base, query, hash := postman.SplitRaw(raw)
	scheme, rest := "", base
	if i := strings.Index(base, "://"); i >= 0 {
		scheme, rest = base[:i+3], base[i+3:]
	}
	out := scheme + encode(rest, pathOK)
	if query != "" || strings.Contains(raw, "?") && !strings.Contains(base, "?") {
		out += "?" + encode(query, queryOK)
	}
	if hash != "" {
		out += "#" + encode(hash, queryOK)
	}
	return out
}

// pathOK and queryOK are the bytes that stay as they are: the WHATWG URL
// standard's path and query percent-encode sets, which Postman follows.
func pathOK(c byte) bool {
	return c > 0x20 && c < 0x7f && strings.IndexByte("\"#<>?`{}", c) < 0
}

func queryOK(c byte) bool {
	return c > 0x20 && c < 0x7f && strings.IndexByte("\"#<>'", c) < 0
}

func unreserved(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '.' || c == '_' || c == '~'
}

func isHex(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

func encode(s string, ok func(byte) bool) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '%':
			// An escape stays; a lone % is encoded itself.
			if i+2 < len(s) && isHex(s[i+1]) && isHex(s[i+2]) {
				b.WriteByte(c)
			} else {
				b.WriteString("%25")
			}
		case ok(c):
			b.WriteByte(c)
		default:
			b.WriteString("%" + strings.ToUpper(hexByte(c)))
		}
	}
	return b.String()
}

func hexByte(c byte) string {
	const digits = "0123456789abcdef"
	return string([]byte{digits[c>>4], digits[c&15]})
}

// encodeQueryPart encodes a query key or value that is added to a URL
// (auth tokens in the query, API keys). They are values, not addresses
// typed by hand: a % in one is a %, so it is encoded too.
func encodeQueryPart(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if unreserved(c) || strings.IndexByte("!$'()*,;:@/?", c) >= 0 {
			b.WriteByte(c)
		} else {
			b.WriteString("%" + strings.ToUpper(hexByte(c)))
		}
	}
	return b.String()
}

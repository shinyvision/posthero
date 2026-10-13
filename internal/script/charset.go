package script

import (
	"strings"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/unicode"

	"github.com/shinyvision/posthero/internal/send"
)

// bodyText is a response body as scripts read it: decoded from the charset
// its Content-Type names, as Postman decodes it, and UTF-8 otherwise.
func bodyText(body []byte, headers []send.Header) string {
	for _, h := range headers {
		if !strings.EqualFold(h.Name, "Content-Type") {
			continue
		}
		if enc := charsets[charsetName(charsetOf(h.Value))]; enc != nil {
			if out, err := enc.NewDecoder().Bytes(body); err == nil {
				return string(out)
			}
		}
		break
	}
	return validUTF8(body)
}

// charsetOf is the charset parameter of a Content-Type, "" without one.
func charsetOf(contentType string) string {
	params := strings.Split(contentType, ";")
	for _, p := range params[1:] {
		k, v, ok := strings.Cut(p, "=")
		if ok && strings.EqualFold(strings.TrimSpace(k), "charset") {
			return strings.Trim(strings.TrimSpace(v), `"'`)
		}
	}
	return ""
}

// charsetName writes a charset's name the way iconv-lite (what Postman
// decodes with) compares them: lower case, letters and digits only, so
// ISO-8859-1, iso_8859-1 and iso88591 are the same.
func charsetName(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// charsets are the encodings besides UTF-8 that bodies are decoded from,
// by charsetName. ISO-8859-1 is Latin-1 itself, as in iconv-lite, not
// windows-1252 as browsers take it.
var charsets = map[string]encoding.Encoding{
	"iso88591": charmap.ISO8859_1, "latin1": charmap.ISO8859_1, "l1": charmap.ISO8859_1, "cp819": charmap.ISO8859_1, "ibm819": charmap.ISO8859_1,
	"iso88592": charmap.ISO8859_2, "latin2": charmap.ISO8859_2, "l2": charmap.ISO8859_2,
	"iso88593": charmap.ISO8859_3, "latin3": charmap.ISO8859_3, "l3": charmap.ISO8859_3,
	"iso88594": charmap.ISO8859_4, "latin4": charmap.ISO8859_4, "l4": charmap.ISO8859_4,
	"iso88595": charmap.ISO8859_5, "cyrillic": charmap.ISO8859_5,
	"iso88596": charmap.ISO8859_6, "arabic": charmap.ISO8859_6,
	"iso88597": charmap.ISO8859_7, "greek": charmap.ISO8859_7,
	"iso88598": charmap.ISO8859_8, "hebrew": charmap.ISO8859_8,
	"iso88599": charmap.ISO8859_9, "latin5": charmap.ISO8859_9, "l5": charmap.ISO8859_9,
	"iso885910": charmap.ISO8859_10, "latin6": charmap.ISO8859_10, "l6": charmap.ISO8859_10,
	"iso885913": charmap.ISO8859_13, "latin7": charmap.ISO8859_13, "l7": charmap.ISO8859_13,
	"iso885914": charmap.ISO8859_14, "latin8": charmap.ISO8859_14, "l8": charmap.ISO8859_14,
	"iso885915": charmap.ISO8859_15, "latin9": charmap.ISO8859_15, "l9": charmap.ISO8859_15,
	"iso885916": charmap.ISO8859_16, "latin10": charmap.ISO8859_16, "l10": charmap.ISO8859_16,
	"windows1250": charmap.Windows1250, "cp1250": charmap.Windows1250,
	"windows1251": charmap.Windows1251, "cp1251": charmap.Windows1251,
	"windows1252": charmap.Windows1252, "cp1252": charmap.Windows1252,
	"windows1253": charmap.Windows1253, "cp1253": charmap.Windows1253,
	"windows1254": charmap.Windows1254, "cp1254": charmap.Windows1254,
	"windows1255": charmap.Windows1255, "cp1255": charmap.Windows1255,
	"windows1256": charmap.Windows1256, "cp1256": charmap.Windows1256,
	"windows1257": charmap.Windows1257, "cp1257": charmap.Windows1257,
	"windows1258": charmap.Windows1258, "cp1258": charmap.Windows1258,
	"windows874": charmap.Windows874, "cp874": charmap.Windows874, "tis620": charmap.Windows874,
	"koi8r": charmap.KOI8R, "koi8u": charmap.KOI8U,
	"cp437": charmap.CodePage437, "ibm437": charmap.CodePage437,
	"cp850": charmap.CodePage850, "ibm850": charmap.CodePage850,
	"cp852": charmap.CodePage852, "ibm852": charmap.CodePage852,
	"cp866": charmap.CodePage866, "ibm866": charmap.CodePage866,
	"macintosh": charmap.Macintosh, "mac": charmap.Macintosh,
	// UTF-16 follows a byte order mark when there is one, and drops it.
	"utf16":   unicode.UTF16(unicode.LittleEndian, unicode.UseBOM),
	"utf16le": unicode.UTF16(unicode.LittleEndian, unicode.UseBOM),
	"ucs2":    unicode.UTF16(unicode.LittleEndian, unicode.UseBOM),
	"utf16be": unicode.UTF16(unicode.BigEndian, unicode.UseBOM),
}

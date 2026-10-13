package vars

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math/big"
	mrand "math/rand/v2"
	"strconv"
	"strings"
	"time"
)

// Dynamic returns a fresh value for one of Postman's dynamic variables,
// named with its $ ($guid, $timestamp, $randomEmail, …).
func Dynamic(name string) (string, bool) {
	f, ok := dynamic[name]
	if !ok {
		return "", false
	}
	return f(), true
}

// DynamicNames lists the dynamic variables, for completion.
func DynamicNames() []string {
	out := make([]string, 0, len(dynamic))
	for k := range dynamic {
		out = append(out, k)
	}
	sortStrings(out)
	return out
}

// UUID returns a random (version 4) UUID.
func UUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b)
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

func intn(n int) int { return mrand.IntN(n) }

func pick(list []string) string { return list[intn(len(list))] }

func between(lo, hi int) int { return lo + intn(hi-lo+1) }

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func digits(n int) string {
	var b strings.Builder
	for range n {
		b.WriteByte(byte('0' + intn(10)))
	}
	return b.String()
}

func alnum(n int) string {
	const chars = "abcdefghijklmnopqrstuvwxyz0123456789"
	var b strings.Builder
	for range n {
		b.WriteByte(chars[intn(len(chars))])
	}
	return b.String()
}

func words(n int, list []string) string {
	out := make([]string, n)
	for i := range out {
		out[i] = pick(list)
	}
	return strings.Join(out, " ")
}

func sentence() string { return capitalize(words(between(4, 10), lorem)) + "." }

func sentences(n int) string {
	out := make([]string, n)
	for i := range out {
		out[i] = sentence()
	}
	return strings.Join(out, " ")
}

func paragraph() string { return sentences(between(3, 6)) }

func firstName() string { return pick(firstNames) }
func lastName() string  { return pick(lastNames) }

func userName() string {
	clean := strings.NewReplacer(" ", "", "'", "").Replace
	f, l := clean(firstName()), clean(lastName())
	switch intn(3) {
	case 0:
		return f + strconv.Itoa(intn(100))
	case 1:
		return f + pick([]string{".", "_"}) + l
	}
	return f + pick([]string{".", "_"}) + l + strconv.Itoa(intn(100))
}

func domainWord() string {
	return strings.ToLower(strings.NewReplacer("'", "", " ", "-").Replace(pick(append(append([]string{}, lastNames...), adjectives...))))
}

func domainName() string { return domainWord() + "." + pick(domainSuffixes) }

func email(domain string) string {
	return strings.ToLower(userName()) + "@" + domain
}

func date(from, to time.Duration) string {
	d := from + time.Duration(mrand.Int64N(int64(to-from)))
	return time.Now().Add(d).UTC().Format("Mon Jan 02 2006 15:04:05 GMT+0000 (Coordinated Universal Time)")
}

func image(category string) string {
	return fmt.Sprintf("http://placeimg.com/640/480/%s", category)
}

func ipv4() string {
	return fmt.Sprintf("%d.%d.%d.%d", intn(256), intn(256), intn(256), intn(256))
}

func ipv6() string {
	parts := make([]string, 8)
	for i := range parts {
		parts[i] = fmt.Sprintf("%x", intn(65536))
	}
	return strings.Join(parts, ":")
}

func mac() string {
	parts := make([]string, 6)
	for i := range parts {
		parts[i] = fmt.Sprintf("%02x", intn(256))
	}
	return strings.Join(parts, ":")
}

func iban() string {
	return "NL" + digits(2) + strings.ToUpper(pick([]string{"ABNA", "INGB", "RABO", "SNSB", "TRIO"})) + digits(10)
}

func bitcoin() string {
	const chars = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"
	var b strings.Builder
	b.WriteByte("13"[intn(2)])
	for range between(25, 33) {
		b.WriteByte(chars[intn(len(chars))])
	}
	return b.String()
}

func hexColor() string { return fmt.Sprintf("#%06x", intn(1<<24)) }

func filePath() string {
	return "/" + strings.Join([]string{pick(directories), pick(lorem), pick(lorem) + "." + pick(fileExts)}, "/")
}

func price() string {
	return fmt.Sprintf("%d.%02d", between(1, 999), intn(100))
}

// dataURI is a tiny transparent PNG, like Postman's.
const dataURI = "data:image/svg+xml;charset=UTF-8,%3Csvg%20xmlns%3D%22http%3A%2F%2Fwww.w3.org%2F2000%2Fsvg%22%20version%3D%221.1%22%20baseProfile%3D%22full%22%20width%3D%22640%22%20height%3D%22480%22%3E%3Crect%20width%3D%22100%25%22%20height%3D%22100%25%22%20fill%3D%22grey%22%2F%3E%3C%2Fsvg%3E"

// password is 15 letters, digits and underscores: what Postman's
// $randomPassword (faker's internet.password) makes.
func password() string {
	const chars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789_"
	b := make([]byte, 15)
	for i := range b {
		n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(chars))))
		b[i] = chars[n.Int64()]
	}
	return string(b)
}

var dynamic = map[string]func() string{
	"$guid":         UUID,
	"$randomUUID":   UUID,
	"$timestamp":    func() string { return strconv.FormatInt(time.Now().Unix(), 10) },
	"$isoTimestamp": func() string { return time.Now().UTC().Format("2006-01-02T15:04:05.000Z") },
	"$randomInt":    func() string { return strconv.Itoa(intn(1001)) },

	"$randomAlphaNumeric": func() string { return alnum(1) },
	"$randomBoolean":      func() string { return strconv.FormatBool(intn(2) == 0) },
	"$randomColor":        func() string { return pick(colors) },
	"$randomHexColor":     hexColor,
	"$randomAbbreviation": func() string { return pick(abbreviations) },

	"$randomIP":         ipv4,
	"$randomIPV6":       ipv6,
	"$randomMACAddress": mac,
	"$randomPassword":   password,
	"$randomLocale":     func() string { return pick(locales) },
	"$randomUserAgent":  func() string { return pick(userAgents) },
	"$randomProtocol":   func() string { return pick([]string{"http", "https"}) },
	"$randomSemver":     func() string { return fmt.Sprintf("%d.%d.%d", intn(10), intn(10), intn(10)) },

	"$randomFirstName":  firstName,
	"$randomLastName":   lastName,
	"$randomFullName":   func() string { return firstName() + " " + lastName() },
	"$randomNamePrefix": func() string { return pick([]string{"Mr.", "Mrs.", "Ms.", "Miss", "Dr."}) },
	"$randomNameSuffix": func() string {
		return pick([]string{"Jr.", "Sr.", "I", "II", "III", "IV", "V", "MD", "DDS", "PhD", "DVM"})
	},

	"$randomJobArea":       func() string { return pick(jobAreas) },
	"$randomJobDescriptor": func() string { return pick(jobDescriptors) },
	"$randomJobTitle":      func() string { return pick(jobDescriptors) + " " + pick(jobAreas) + " " + pick(jobTypes) },
	"$randomJobType":       func() string { return pick(jobTypes) },

	"$randomPhoneNumber":    func() string { return digits(3) + "-" + digits(3) + "-" + digits(4) },
	"$randomPhoneNumberExt": func() string { return digits(2) + "-" + digits(3) + "-" + digits(3) + "-" + digits(4) },
	"$randomCity":           func() string { return pick(cities) },
	"$randomStreetName":     func() string { return lastName() + " " + pick(streetSuffixes) },
	"$randomStreetAddress":  func() string { return strconv.Itoa(between(1, 9999)) + " " + lastName() + " " + pick(streetSuffixes) },
	"$randomCountry":        func() string { return pick(countries) },
	"$randomCountryCode":    func() string { return pick(countryCodes) },
	"$randomLatitude":       func() string { return fmt.Sprintf("%.4f", mrand.Float64()*180-90) },
	"$randomLongitude":      func() string { return fmt.Sprintf("%.4f", mrand.Float64()*360-180) },

	"$randomAvatarImage": func() string {
		return fmt.Sprintf("https://cdn.fakercloud.com/avatars/%s_128.jpg", strings.ToLower(userName()))
	},
	"$randomImageUrl":       func() string { return "http://placeimg.com/640/480" },
	"$randomAbstractImage":  func() string { return image("abstract") },
	"$randomAnimalsImage":   func() string { return image("animals") },
	"$randomBusinessImage":  func() string { return image("business") },
	"$randomCatsImage":      func() string { return image("cats") },
	"$randomCityImage":      func() string { return image("city") },
	"$randomFoodImage":      func() string { return image("food") },
	"$randomNightlifeImage": func() string { return image("nightlife") },
	"$randomFashionImage":   func() string { return image("fashion") },
	"$randomPeopleImage":    func() string { return image("people") },
	"$randomNatureImage":    func() string { return image("nature") },
	"$randomSportsImage":    func() string { return image("sports") },
	"$randomTransportImage": func() string { return image("transport") },
	"$randomImageDataUri":   func() string { return dataURI },

	"$randomBankAccount": func() string { return digits(8) },
	"$randomBankAccountName": func() string {
		return pick([]string{"Home Loan", "Checking", "Savings", "Money Market", "Credit Card", "Auto Loan", "Personal Loan", "Investment"}) + " Account"
	},
	"$randomCreditCardMask":  func() string { return digits(4) },
	"$randomBankAccountBic":  func() string { return strings.ToUpper(alnum(4)) + "NL" + strings.ToUpper(alnum(2)) },
	"$randomBankAccountIban": iban,
	"$randomTransactionType": func() string { return pick([]string{"deposit", "withdrawal", "payment", "invoice"}) },
	"$randomCurrencyCode":    func() string { return pick(currencyCodes) },
	"$randomCurrencyName":    func() string { return pick(currencyNames) },
	"$randomCurrencySymbol":  func() string { return pick([]string{"$", "€", "£", "¥", "₹", "₽", "₩", "₺", "kr", "zł"}) },
	"$randomBitcoin":         bitcoin,

	"$randomCompanyName":   func() string { return lastName() + " " + pick(companySuffixes) },
	"$randomCompanySuffix": func() string { return pick(companySuffixes) },
	"$randomBs":            func() string { return pick(bsVerbs) + " " + pick(bsAdjectives) + " " + pick(bsNouns) },
	"$randomBsAdjective":   func() string { return pick(bsAdjectives) },
	"$randomBsBuzz":        func() string { return pick(bsVerbs) },
	"$randomBsNoun":        func() string { return pick(bsNouns) },

	"$randomCatchPhrase":           func() string { return pick(catchAdjectives) + " " + pick(catchDescriptors) + " " + pick(catchNouns) },
	"$randomCatchPhraseAdjective":  func() string { return pick(catchAdjectives) },
	"$randomCatchPhraseDescriptor": func() string { return pick(catchDescriptors) },
	"$randomCatchPhraseNoun":       func() string { return pick(catchNouns) },

	"$randomDatabaseColumn": func() string {
		return pick([]string{"id", "title", "name", "email", "phone", "token", "group", "category", "password", "comment", "avatar", "status", "createdAt", "updatedAt"})
	},
	"$randomDatabaseType": func() string {
		return pick([]string{"int", "varchar", "text", "date", "datetime", "timestamp", "boolean", "decimal", "bigint", "uuid", "json"})
	},
	"$randomDatabaseCollation": func() string {
		return pick([]string{"utf8_unicode_ci", "utf8_general_ci", "utf8_bin", "ascii_bin", "cp1250_general_ci"})
	},
	"$randomDatabaseEngine": func() string { return pick([]string{"InnoDB", "MyISAM", "MEMORY", "CSV", "BLACKHOLE", "ARCHIVE"}) },

	"$randomDateFuture": func() string { return date(time.Hour, 365*24*time.Hour) },
	"$randomDatePast":   func() string { return date(-365*24*time.Hour, -time.Hour) },
	"$randomDateRecent": func() string { return date(-24*time.Hour, -time.Minute) },
	"$randomWeekday":    func() string { return time.Weekday(intn(7)).String() },
	"$randomMonth":      func() string { return time.Month(between(1, 12)).String() },

	"$randomDomainName":   domainName,
	"$randomDomainSuffix": func() string { return pick(domainSuffixes) },
	"$randomDomainWord":   domainWord,
	"$randomEmail":        func() string { return email(pick([]string{"gmail.com", "yahoo.com", "hotmail.com", "outlook.com"})) },
	"$randomExampleEmail": func() string { return email(pick([]string{"example.com", "example.net", "example.org"})) },
	"$randomUserName":     userName,
	"$randomUrl":          func() string { return pick([]string{"http", "https"}) + "://" + domainName() },

	"$randomFileName": func() string { return pick(lorem) + "_" + pick(lorem) + "." + pick(fileExts) },
	"$randomFileType": func() string { return pick([]string{"application", "audio", "image", "text", "video"}) },
	"$randomFileExt":  func() string { return pick(fileExts) },
	"$randomCommonFileName": func() string {
		return pick(lorem) + "_" + pick(lorem) + "." + pick([]string{"pdf", "mpeg", "wav", "png", "jpeg", "gif", "mp4", "html", "txt"})
	},
	"$randomCommonFileType": func() string { return pick([]string{"video", "audio", "image", "text", "application"}) },
	"$randomCommonFileExt":  func() string { return pick([]string{"pdf", "mpeg", "wav", "png", "jpeg", "gif", "mp4", "html", "txt"}) },
	"$randomFilePath":       filePath,
	"$randomDirectoryPath":  func() string { return "/" + pick(directories) },
	"$randomMimeType":       func() string { return pick(mimeTypes) },

	"$randomPrice":            price,
	"$randomProduct":          func() string { return pick(products) },
	"$randomProductAdjective": func() string { return pick(productAdjectives) },
	"$randomProductMaterial":  func() string { return pick(productMaterials) },
	"$randomProductName":      func() string { return pick(productAdjectives) + " " + pick(productMaterials) + " " + pick(products) },
	"$randomDepartment":       func() string { return pick(departments) },

	"$randomNoun":      func() string { return pick(nouns) },
	"$randomVerb":      func() string { return pick(verbs) },
	"$randomIngverb":   func() string { return pick(ingVerbs) },
	"$randomAdjective": func() string { return pick(adjectives) },
	"$randomWord":      func() string { return pick(append(append([]string{}, nouns...), adjectives...)) },
	"$randomWords":     func() string { return words(between(2, 5), nouns) },
	"$randomPhrase": func() string {
		return "If we " + pick(verbs) + " the " + pick(nouns) + ", we can get to the " + pick(adjectives) + " " + pick(nouns) + " through the " + pick(adjectives) + " " + pick(nouns) + "!"
	},

	"$randomLoremWord":       func() string { return pick(lorem) },
	"$randomLoremWords":      func() string { return words(3, lorem) },
	"$randomLoremSentence":   sentence,
	"$randomLoremSentences":  func() string { return sentences(between(2, 6)) },
	"$randomLoremParagraph":  paragraph,
	"$randomLoremParagraphs": func() string { return paragraph() + "\n \r" + paragraph() + "\n \r" + paragraph() },
	"$randomLoremText":       func() string { return sentences(between(2, 5)) },
	"$randomLoremSlug":       func() string { return strings.ReplaceAll(words(3, lorem), " ", "-") },
	"$randomLoremLines": func() string {
		out := make([]string, between(1, 5))
		for i := range out {
			out[i] = sentence()
		}
		return strings.Join(out, "\n")
	},
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

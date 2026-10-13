package send

import (
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
)

// Two posthero instances sharing the cookie file keep each other's cookies,
// and a cookie one of them removed stays removed.
func TestJarTwoInstances(t *testing.T) {
	file := filepath.Join(t.TempDir(), "cookies.json")
	u, _ := url.Parse("https://shop.example.com/")
	a, b := NewJar(file), NewJar(file)
	a.SetCookies(u, []*http.Cookie{{Name: "a", Value: "1"}})
	if err := a.Save(); err != nil {
		t.Fatal(err)
	}
	b.SetCookies(u, []*http.Cookie{{Name: "b", Value: "2"}})
	if err := b.Save(); err != nil {
		t.Fatal(err)
	}
	names := func(j *Jar) string {
		var out []string
		for _, c := range j.All() {
			out = append(out, c.Name+"="+c.Value)
		}
		return strings.Join(out, ",")
	}
	if got := names(NewJar(file)); got != "a=1,b=2" {
		t.Fatalf("after both saved: %s", got)
	}
	a.Delete("shop.example.com", "a")
	if err := a.Save(); err != nil {
		t.Fatal(err)
	}
	b.SetCookies(u, []*http.Cookie{{Name: "b", Value: "3"}})
	if err := b.Save(); err != nil {
		t.Fatal(err)
	}
	if got := names(NewJar(file)); got != "b=3" {
		t.Fatalf("after a removed its cookie and b saved again: %s", got)
	}
	if got := names(b); got != "b=3" {
		t.Fatalf("b's own view after saving: %s", got)
	}
}

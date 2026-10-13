package jsonx

import "testing"

func TestNoHTMLEscaping(t *testing.T) {
	got, err := Marshal(map[string]string{"u": "https://x/?a=1&b=<2>"})
	if err != nil || string(got) != `{"u":"https://x/?a=1&b=<2>"}` {
		t.Fatalf("%s %v", got, err)
	}
	got, _ = MarshalIndent([]int{1}, "", " ")
	if string(got) != "[\n 1\n]" {
		t.Fatalf("%q", got)
	}
}

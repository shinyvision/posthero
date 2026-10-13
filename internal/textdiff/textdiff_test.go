package textdiff

import (
	"fmt"
	"testing"
)

func TestLines(t *testing.T) {
	got := Lines("GET a\nAccept: x\n\nbody", "GET b\nAccept: x\nX-New: 1\n\nbody\n")
	want := []Line{{Removed, "GET a"}, {Added, "GET b"}, {Same, "Accept: x"}, {Added, "X-New: 1"}, {Same, ""}, {Same, "body"}}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("line %d: got %v, want %v (all: %v)", i, got[i], want[i], got)
		}
	}
	if !Changed(got) || Changed(Lines("a\nb", "a\nb\n")) {
		t.Fatal("Changed is wrong")
	}
	if l := Lines("", "x"); len(l) != 1 || l[0].Kind != Added {
		t.Fatalf("from nothing: %v", l)
	}
}

// A big body changed all over is compared without a table the size of
// both texts multiplied; what is alike at the start and end still is.
func TestLinesBig(t *testing.T) {
	var a, b []byte
	for i := range 20000 {
		a = append(a, fmt.Sprintf("old %d\n", i)...)
		b = append(b, fmt.Sprintf("new %d\n", i)...)
	}
	got := Lines("GET x\n"+string(a)+"end", "GET x\n"+string(b)+"end")
	if len(got) != 40002 || got[0] != (Line{Same, "GET x"}) || got[1].Kind != Removed || got[40000].Kind != Added || got[40001] != (Line{Same, "end"}) {
		t.Fatalf("%d lines: %v … %v", len(got), got[:2], got[len(got)-2:])
	}
}

func TestMerge(t *testing.T) {
	base := "GET /a\nAccept: x\n\nbody"
	// Different lines: both taken.
	got, n := Merge(base, "POST /a\nAccept: x\n\nbody", "GET /a\nAccept: y\n\nbody", "yours", "Postman")
	if n != 0 || got != "POST /a\nAccept: y\n\nbody" {
		t.Fatalf("clean merge: %d %q", n, got)
	}
	// The same line, differently: a conflict, mine first.
	got, n = Merge(base, "PUT /a\nAccept: x\n\nbody", "PATCH /a\nAccept: x\n\nbody", "yours", "Postman")
	want := "<<<<<<< yours\nPUT /a\n=======\nPATCH /a\n>>>>>>> Postman\nAccept: x\n\nbody"
	if n != 1 || got != want {
		t.Fatalf("conflict: %d %q", n, got)
	}
	if line, ok := HasConflicts(got); !ok || line != 1 {
		t.Fatalf("HasConflicts: %d %v", line, ok)
	}
	// The same change on both sides isn't a conflict; lines added at the
	// end by one side come along.
	got, n = Merge(base, "GET /b\nAccept: x\n\nbody", "GET /b\nAccept: x\n\nbody\nmore", "yours", "Postman")
	if n != 0 || got != "GET /b\nAccept: x\n\nbody\nmore" {
		t.Fatalf("same change: %d %q", n, got)
	}
	// Deleted on one side, unchanged on the other.
	got, n = Merge(base, "GET /a\n\nbody", base, "yours", "Postman")
	if n != 0 || got != "GET /a\n\nbody" {
		t.Fatalf("deletion: %d %q", n, got)
	}
	if _, ok := HasConflicts(got); ok {
		t.Fatal("no markers, but HasConflicts says so")
	}
}

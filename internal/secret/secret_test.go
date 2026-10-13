package secret

import (
	"errors"
	"testing"
)

func TestSecret(t *testing.T) {
	MockInit()
	if err := Available(); err != nil {
		t.Fatal(err)
	}
	if _, err := Get("abc"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing password: %v", err)
	}
	if err := Set("abc", "s3cret"); err != nil {
		t.Fatal(err)
	}
	if pw, err := Get("abc"); err != nil || pw != "s3cret" {
		t.Fatalf("got %q, %v", pw, err)
	}
	if err := Delete("abc"); err != nil {
		t.Fatal(err)
	}
	if err := Delete("abc"); err != nil {
		t.Fatalf("deleting twice: %v", err)
	}
}

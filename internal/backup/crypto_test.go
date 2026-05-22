package backup

import (
	"bytes"
	"testing"
)

func TestSealAndOpen_RoundTrip(t *testing.T) {
	plaintext := []byte("hello backup world")
	password := "correct-horse-battery-staple"

	box, err := Seal(plaintext, password)
	if err != nil {
		t.Fatalf("Seal failed: %v", err)
	}

	got, err := Open(box, password)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}

	if !bytes.Equal(got, plaintext) {
		t.Fatalf("round-trip mismatch: got %q want %q", got, plaintext)
	}
}

func TestOpen_WrongPassword(t *testing.T) {
	box, err := Seal([]byte("secret"), "right-password")
	if err != nil {
		t.Fatal(err)
	}

	_, err = Open(box, "wrong-password")
	if err == nil {
		t.Fatal("expected error on wrong password, got nil")
	}
}

func TestSeal_RandomSaltAndNonce(t *testing.T) {
	pt := []byte("x")
	a, _ := Seal(pt, "p")
	b, _ := Seal(pt, "p")
	if bytes.Equal(a.Salt[:], b.Salt[:]) {
		t.Fatal("salt must be random per Seal")
	}
	if bytes.Equal(a.Nonce[:], b.Nonce[:]) {
		t.Fatal("nonce must be random per Seal")
	}
}

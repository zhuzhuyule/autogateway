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

func TestDeriveKey_Deterministic(t *testing.T) {
	salt := []byte("0123456789abcdef")
	a := DeriveKey("password-x", salt)
	b := DeriveKey("password-x", salt)
	if !bytes.Equal(a, b) {
		t.Fatal("DeriveKey must be deterministic for same (password, salt)")
	}
}

func TestDeriveKey_DifferentPasswordYieldsDifferentKey(t *testing.T) {
	salt := []byte("0123456789abcdef")
	a := DeriveKey("password-x", salt)
	b := DeriveKey("password-y", salt)
	if bytes.Equal(a, b) {
		t.Fatal("DeriveKey must differ for different passwords")
	}
}

func TestOpen_TamperedCiphertextRejected(t *testing.T) {
	box, err := Seal([]byte("payload"), "pw")
	if err != nil {
		t.Fatal(err)
	}
	if len(box.Ciphertext) == 0 {
		t.Fatal("empty ciphertext")
	}
	// Flip a bit in the middle of the ciphertext (before the GCM tag).
	box.Ciphertext[0] ^= 0x01
	if _, err := Open(box, "pw"); err == nil {
		t.Fatal("expected error on tampered ciphertext, got nil")
	}
}

func TestOpen_TamperedAADRejected(t *testing.T) {
	box, err := Seal([]byte("payload"), "pw")
	if err != nil {
		t.Fatal(err)
	}
	if len(box.AAD) == 0 {
		t.Fatal("empty AAD")
	}
	// Simulate a downgrade attempt: flip the version byte in the AAD.
	box.AAD = append([]byte(nil), box.AAD...) // own copy to avoid mutating shared backing
	box.AAD[len(box.AAD)-1] ^= 0xFF
	if _, err := Open(box, "pw"); err == nil {
		t.Fatal("expected error on tampered AAD (downgrade), got nil")
	}
}

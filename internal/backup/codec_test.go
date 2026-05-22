package backup

import (
	"bytes"
	"testing"
)

func TestEncodeDecode_RoundTrip(t *testing.T) {
	plaintext := []byte(`{"hello":"world"}`)
	password := "p4ssw0rd"

	blob, err := EncodeContainer(plaintext, password)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if !bytes.HasPrefix(blob, []byte("ACB1")) {
		t.Fatalf("magic missing")
	}

	got, err := DecodeContainer(blob, password)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("round-trip mismatch")
	}
}

func TestDecodeContainer_BadMagic(t *testing.T) {
	bad := bytes.Repeat([]byte{0}, 80)
	_, err := DecodeContainer(bad, "x")
	if err == nil || err.Error() == "" {
		t.Fatal("expected bad-magic error")
	}
}

func TestDecodeContainer_UnsupportedVersion(t *testing.T) {
	blob, _ := EncodeContainer([]byte("x"), "x")
	blob[4] = 0x99 // tamper container_version
	_, err := DecodeContainer(blob, "x")
	if err == nil {
		t.Fatal("expected unsupported version error")
	}
}

func TestDecodeContainer_Truncated(t *testing.T) {
	blob, _ := EncodeContainer([]byte("x"), "x")
	_, err := DecodeContainer(blob[:30], "x")
	if err == nil {
		t.Fatal("expected truncation error")
	}
}

func TestDecodeContainer_UnsupportedKDF(t *testing.T) {
	blob, _ := EncodeContainer([]byte("x"), "x")
	blob[5] = 0x99 // tamper kdf_id
	_, err := DecodeContainer(blob, "x")
	if err == nil {
		t.Fatal("expected unsupported kdf error")
	}
}

func TestDecodeContainer_UnsupportedCipher(t *testing.T) {
	blob, _ := EncodeContainer([]byte("x"), "x")
	blob[6] = 0x99 // tamper cipher_id
	_, err := DecodeContainer(blob, "x")
	if err == nil {
		t.Fatal("expected unsupported cipher error")
	}
}

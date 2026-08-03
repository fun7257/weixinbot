package media

import (
	"bytes"
	"testing"
)

func TestAES128ECBRoundTrip(t *testing.T) {
	key := []byte("0123456789abcdef") // 16 bytes
	plain := []byte("hello weixin bot media plane!")
	ct, err := EncryptAES128ECB(plain, key)
	if err != nil {
		t.Fatal(err)
	}
	if len(ct) != PaddedSize(len(plain)) {
		t.Fatalf("ciphertext len %d want padded %d", len(ct), PaddedSize(len(plain)))
	}
	out, err := DecryptAES128ECB(ct, key)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out, plain) {
		t.Fatalf("roundtrip mismatch: %q vs %q", out, plain)
	}
}

func TestPaddedSize(t *testing.T) {
	if PaddedSize(0) != 16 {
		t.Fatalf("empty pad want 16 got %d", PaddedSize(0))
	}
	if PaddedSize(16) != 32 {
		t.Fatalf("full block still pads: %d", PaddedSize(16))
	}
	if PaddedSize(15) != 16 {
		t.Fatalf("15 -> 16 got %d", PaddedSize(15))
	}
}

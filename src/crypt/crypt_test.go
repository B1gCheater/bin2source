package crypt

import (
	"bytes"
	"encoding/hex"
	"testing"
)

// Vectors from NIST SP 800-38A, F.2.5 (CBC-AES256) and F.5.5 (CTR-AES256).
// The CBC vector is extended with a PKCS#7 block. Both were re-derived from
// OpenSSL before being pasted here, so a mistake in either implementation
// shows up as a mismatch rather than as two agreeing bugs.
const (
	nistKey       = "603deb1015ca71be2b73aef0857d77811f352c073b6108d72d9810a30914dff4"
	nistPlain     = "6bc1bee22e409f96e93d7e117393172aae2d8a571e03ac9c9eb76fac45af8e5130c81c46a35ce411e5fbc1191a0a52eff69f2445df4f9b17ad2b417be66c3710"
	nistCTRIV     = "f0f1f2f3f4f5f6f7f8f9fafbfcfdfeff"
	nistCTRCipher = "601ec313775789a5b7a7f504bbf3d228f443e3ca4d62b59aca84e990cacaf5c52b0930daa23de94ce87017ba2d84988ddfc9c58db67aada613c2dd08457941a6"
	nistCBCIV     = "000102030405060708090a0b0c0d0e0f"
	nistCBCCipher = "f58c4c04d6e5f1ba779eabfb5f7bfbd69cfc4e967edb808d679f777bc6702c7d39f23369a9d9bacfa530e26304231461b2eb05e2c39be9fcda6c19078c6a9d1b3f461796d6b0d6b2e0c2a72b4d80e644"
)

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("bad test vector: %v", err)
	}
	return b
}

func TestCTRMatchesNIST(t *testing.T) {
	plain := mustHex(t, nistPlain)
	got, err := Encrypt(plain, mustHex(t, nistKey), mustHex(t, nistCTRIV), ModeCTR)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if want := mustHex(t, nistCTRCipher); !bytes.Equal(got, want) {
		t.Errorf("CTR output does not match NIST SP 800-38A F.5.5\n got %x\nwant %x", got, want)
	}
	// A stream cipher must not change the length.
	if len(got) != len(plain) {
		t.Errorf("CTR changed the length: %d in, %d out", len(plain), len(got))
	}
}

func TestCBCMatchesNIST(t *testing.T) {
	plain := mustHex(t, nistPlain)
	got, err := Encrypt(plain, mustHex(t, nistKey), mustHex(t, nistCBCIV), ModeCBC)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if want := mustHex(t, nistCBCCipher); !bytes.Equal(got, want) {
		t.Errorf("CBC output does not match NIST SP 800-38A F.2.5 plus PKCS#7\n got %x\nwant %x", got, want)
	}
	// 64 bytes of input become 80: four data blocks and one full padding block.
	if len(got) != 80 {
		t.Errorf("CBC length is %d, want 80", len(got))
	}
}

func TestPKCS7IsSelfDescribing(t *testing.T) {
	for _, n := range []int{0, 1, 15, 16, 17, 31, 32} {
		pad := pkcs7Pad(bytes.Repeat([]byte{0xaa}, n), 16)

		if len(pad)%16 != 0 {
			t.Errorf("n=%d: padded to %d bytes, not a whole block", n, len(pad))
		}
		if len(pad) <= n {
			t.Errorf("n=%d: padding did not grow the input", n)
		}
		// The trailing byte is the pad length and every pad byte repeats it,
		// which is what lets a decoder strip it without being told n.
		count := int(pad[len(pad)-1])
		if count < 1 || count > 16 {
			t.Errorf("n=%d: pad byte is %d, want 1..16", n, count)
		}
		if !bytes.Equal(pad[len(pad)-count:], bytes.Repeat([]byte{byte(count)}, count)) {
			t.Errorf("n=%d: padding bytes are not %d repeats of %d", n, count, count)
		}
		if !bytes.Equal(pad[:n], bytes.Repeat([]byte{0xaa}, n)) {
			t.Errorf("n=%d: padding overwrote the input", n)
		}
	}
}

func TestPKCS7AlwaysAddsABlockWhenAligned(t *testing.T) {
	// Without this a decoder cannot tell a full block of data from no padding.
	pad := pkcs7Pad(bytes.Repeat([]byte{0x00}, 16), 16)
	if len(pad) != 32 {
		t.Fatalf("aligned input padded to %d bytes, want 32", len(pad))
	}
	if pad[31] != 16 {
		t.Errorf("pad byte is %d, want 16", pad[31])
	}
}

func TestNewIVIsRandomAndBlockSized(t *testing.T) {
	a, err := NewIV()
	if err != nil {
		t.Fatalf("NewIV: %v", err)
	}
	if len(a) != 16 {
		t.Fatalf("IV is %d bytes, want 16", len(a))
	}

	seen := map[string]bool{}
	for i := 0; i < 64; i++ {
		iv, err := NewIV()
		if err != nil {
			t.Fatalf("NewIV: %v", err)
		}
		seen[string(iv)] = true
	}
	if len(seen) != 64 {
		t.Errorf("only %d distinct IVs out of 64, the generator is not random", len(seen))
	}
}

func TestEncryptRejectsBadInput(t *testing.T) {
	key := mustHex(t, nistKey)
	iv := mustHex(t, nistCBCIV)

	if _, err := Encrypt([]byte("x"), key, iv[:8], ModeCTR); err == nil {
		t.Error("a short IV was accepted")
	}
	if _, err := Encrypt([]byte("x"), key[:16], iv, ModeCTR); err == nil {
		t.Error("a 16 byte key was accepted for AES-256")
	}
	if _, err := Encrypt([]byte("x"), key, iv, "AES256ECB"); err == nil {
		t.Error("an unknown mode was accepted")
	}
}

// Package crypt implements the AES-256 modes bin2source can apply to the
// bytes it embeds. It is intentionally encryption-only: the generated source
// files carry the ciphertext, never the plaintext.
package crypt

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
)

// Cipher mode names. These strings are written verbatim into the generated
// source comments, so they double as the public name of each mode.
const (
	ModeCTR = "AES256CTR"
	ModeCBC = "AES256CBC"
)

// KeySize is the required key length in bytes: AES-256.
const KeySize = 32

// NewIV returns a fresh random IV of one AES block.
func NewIV() ([]byte, error) {
	iv := make([]byte, aes.BlockSize)
	if _, err := rand.Read(iv); err != nil {
		return nil, fmt.Errorf("generating IV: %w", err)
	}
	return iv, nil
}

// Encrypt applies mode to plain using key and iv.
//
// CTR is a stream cipher and preserves the input length exactly. CBC pads to
// the next block boundary with PKCS#7 as described in RFC 5652 section 6.3,
// so its output is always larger than the input.
func Encrypt(plain, key, iv []byte, mode string) ([]byte, error) {
	if len(iv) != aes.BlockSize {
		return nil, fmt.Errorf("iv must be %d bytes, got %d", aes.BlockSize, len(iv))
	}
	// crypto/aes also accepts 16 and 24 byte keys for AES-128 and AES-192.
	// The mode names promise 256, so a short key is refused rather than
	// quietly producing a weaker cipher than the generated comment claims.
	if len(key) != KeySize {
		return nil, fmt.Errorf("key must be %d bytes for AES-256, got %d", KeySize, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	switch mode {
	case ModeCTR:
		out := make([]byte, len(plain))
		cipher.NewCTR(block, iv).XORKeyStream(out, plain)
		return out, nil

	case ModeCBC:
		padded := pkcs7Pad(plain, aes.BlockSize)
		out := make([]byte, len(padded))
		cipher.NewCBCEncrypter(block, iv).CryptBlocks(out, padded)
		return out, nil

	default:
		return nil, errors.New("unknown cipher mode: " + mode)
	}
}

// pkcs7Pad appends n copies of the byte n, where n is whatever fills the
// input up to the next block boundary. A full extra block is added when the
// input is already aligned, which is what makes the padding self-describing:
// a decoder always strips at least one byte and never needs to be told the
// original length.
func pkcs7Pad(b []byte, blockSize int) []byte {
	n := blockSize - len(b)%blockSize
	out := make([]byte, len(b)+n)
	copy(out, b)
	for i := len(b); i < len(out); i++ {
		out[i] = byte(n)
	}
	return out
}

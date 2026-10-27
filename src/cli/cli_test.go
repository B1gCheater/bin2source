package cli

import (
	"bytes"
	"compress/flate"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bin2source/src/compress"
	"bin2source/src/crypt"
	"bin2source/src/emit"
)

// inputDigest is the helper the pipeline expects the caller to have computed
// from the file on disk.
func inputDigest(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// The digest travels in Meta and is read straight out of the generated source
// by the consumer, so it has to be the standard SHA-256 of the bytes on disk.
// The vectors are the well known ones, which pins the algorithm rather than
// just checking that the tool agrees with itself.
func TestBuildPayloadCarriesTheInputDigest(t *testing.T) {
	vectors := []struct {
		name string
		text string
		want string
	}{
		{
			name: "empty",
			text: "",
			want: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		},
		{
			name: "abc",
			text: "abc",
			want: "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad",
		},
		{
			name: "longer",
			text: "abcdbcdecdefdefgefghfghighijhijkijkljklmklmnlmnomnopnopq",
			want: "248d6a61d20638b8e5c026930c3e6039a33ce45964ff2167f6ecedd419db06c1",
		},
	}

	for _, tc := range vectors {
		t.Run(tc.name, func(t *testing.T) {
			_, meta, err := buildPayload([]byte(tc.text), pipeline{ivMode: 1},
				emit.Meta{Sha256Hex: tc.want})
			if err != nil {
				t.Fatal(err)
			}
			if meta.Sha256Hex != tc.want {
				t.Errorf("meta digest is %q, want %q", meta.Sha256Hex, tc.want)
			}
		})
	}
}

// The digest must describe the input file, not the array: after a consumer
// decrypts the payload it has to land on the very same value.
func TestBuildPayloadKeepsThePlaintextDigestWhenEncrypting(t *testing.T) {
	plain := []byte("the quick brown fox jumps over the lazy dog")
	want := inputDigest(plain)

	key := make([]byte, crypt.KeySize)
	for i := range key {
		key[i] = 0xff
	}

	for _, mode := range []string{crypt.ModeCTR, crypt.ModeCBC} {
		t.Run(mode, func(t *testing.T) {
			payload, meta, err := buildPayload(plain, pipeline{cipher: mode, key: key, ivMode: 1},
				emit.Meta{Sha256Hex: want})
			if err != nil {
				t.Fatal(err)
			}
			if meta.Sha256Hex != want {
				t.Errorf("digest is %q, want the digest of the plaintext %q", meta.Sha256Hex, want)
			}
			if strings.Contains(hex.EncodeToString(payload), want) {
				t.Error("the plaintext digest turned up inside the ciphertext")
			}
		})
	}
}

func TestPickCompression(t *testing.T) {
	tests := []struct {
		name       string
		codec      string
		level      int
		codecGiven bool
		levelGiven bool
		wantCode   string
		wantLvl    int
		wantErr    string
	}{
		{name: "absent", level: -1, wantCode: "", wantLvl: 0},
		{name: "deflate takes the default level", codec: "deflate", codecGiven: true, level: -1,
			wantCode: "deflate", wantLvl: 9},
		{name: "zstd takes the default level", codec: "zstd", codecGiven: true, level: -1,
			wantCode: "zstd", wantLvl: 19},
		{name: "zstd accepts level 22", codec: "zstd", codecGiven: true, level: 22, levelGiven: true,
			wantCode: "zstd", wantLvl: 22},
		{name: "explicit level", codec: "deflate", codecGiven: true, level: 1, levelGiven: true,
			wantCode: "deflate", wantLvl: 1},
		{name: "level alone is refused", level: 5, levelGiven: true, wantErr: "-level only applies"},
		{name: "a negative level alone is refused", level: -1, levelGiven: true,
			wantErr: "-level only applies"},
		{name: "unknown codec", codec: "xz", codecGiven: true, level: -1,
			wantErr: "unknown compression codec"},
		{name: "codec is case sensitive", codec: "Deflate", codecGiven: true, level: -1,
			wantErr: "unknown compression codec"},
		{name: "codec is case sensitive, zstd", codec: "ZSTD", codecGiven: true, level: -1,
			wantErr: "unknown compression codec"},
		// deflate's 9 is not a shared ceiling: zstd has to be allowed past it.
		{name: "deflate stops at 9", codec: "deflate", codecGiven: true, level: 22, levelGiven: true,
			wantErr: "takes -level 1..9"},
		{name: "zstd stops at 22", codec: "zstd", codecGiven: true, level: 23, levelGiven: true,
			wantErr: "takes -level 1..22"},
		{name: "level below the range", codec: "deflate", codecGiven: true, level: 0, levelGiven: true,
			wantErr: "takes -level 1..9"},
		{name: "level above the range", codec: "deflate", codecGiven: true, level: 10, levelGiven: true,
			wantErr: "takes -level 1..9"},
		// "-compress --level 9" reaches the flag package as -compress with the
		// value "--level", so the mistake has to be caught here by shape.
		{name: "a flag swallowed as the codec", codec: "--level", codecGiven: true, level: -1,
			wantErr: "needs a codec name"},
		// The flag having been given is what counts, not the value looking
		// like the "not given" sentinel: both of these used to parse as
		// "absent" and silently change what the run does.
		{name: "an empty name is a missing name, not no compression", codec: "", codecGiven: true,
			level: -1, wantErr: "needs a codec name"},
		{name: "a negative level is out of range, not absent", codec: "deflate", codecGiven: true,
			level: -1, levelGiven: true, wantErr: "takes -level 1..9, got -1"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			codec, level, err := pickCompression(tc.codec, tc.level, tc.codecGiven, tc.levelGiven)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want one containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if codec != tc.wantCode || level != tc.wantLvl {
				t.Errorf("got codec=%q level=%d, want %q and %d", codec, level, tc.wantCode, tc.wantLvl)
			}
		})
	}
}

// Without -compress the array is the file and the only digest is the file's,
// so nothing about an existing invocation's output changes.
func TestBuildPayloadWithoutCompressionSetsNoStageFields(t *testing.T) {
	plain := []byte("unchanged")
	payload, meta, err := buildPayload(plain, pipeline{ivMode: 1}, emit.Meta{Sha256Hex: inputDigest(plain)})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(payload, plain) {
		t.Error("the payload was altered without -compress")
	}
	if meta.Codec != "" || meta.CodecSumHex != "" || meta.ArraySumHex != "" || meta.RawLen != 0 {
		t.Errorf("compression fields leaked into an uncompressed run: %+v", meta)
	}
}

// Each stage digest has to name the bytes that stage produced, so a consumer
// can walk the pipeline backwards and stop at the step that went wrong.
func TestBuildPayloadStageDigestsNameTheirStage(t *testing.T) {
	plain := bytes.Repeat([]byte("compress me please, please, please\n"), 40)
	key := bytes.Repeat([]byte{0x11}, crypt.KeySize)
	m := emit.Meta{Sha256Hex: inputDigest(plain)}

	payload, meta, err := buildPayload(plain,
		pipeline{codec: compress.CodecDeflate, level: 9, cipher: crypt.ModeCTR, key: key, ivMode: 1}, m)
	if err != nil {
		t.Fatal(err)
	}

	// What the codec produced, computed the same way the pipeline did.
	compressed, err := compress.Apply(plain, compress.CodecDeflate, 9)
	if err != nil {
		t.Fatal(err)
	}

	if meta.Codec != compress.CodecDeflate {
		t.Errorf("meta.Codec is %q, want %q", meta.Codec, compress.CodecDeflate)
	}
	if meta.Level != 9 {
		t.Errorf("meta.Level is %d, want 9", meta.Level)
	}
	if meta.RawLen != len(plain) {
		t.Errorf("meta.RawLen is %d, want the input size %d", meta.RawLen, len(plain))
	}
	if want := hex.EncodeToString(digestOf(compressed)); meta.CodecSumHex != want {
		t.Errorf("codec digest is %q, want %q", meta.CodecSumHex, want)
	}
	if want := hex.EncodeToString(digestOf(payload)); meta.ArraySumHex != want {
		t.Errorf("array digest is %q, want %q", meta.ArraySumHex, want)
	}
	if meta.Sha256Hex != inputDigest(plain) {
		t.Error("the input digest was overwritten by a later stage")
	}

	// And the digest the consumer would compute by hand, one stage at a time.
	if got := inputDigest(decryptCTR(t, payload, key)); got != meta.CodecSumHex {
		t.Errorf("decrypting the array gives %q, not the recorded codec digest %q",
			got, meta.CodecSumHex)
	}
	inflated := inflate(t, compressed)
	if got := inputDigest(inflated); got != meta.Sha256Hex {
		t.Errorf("inflating the codec output gives %q, not the input digest %q",
			got, meta.Sha256Hex)
	}
	if !bytes.Equal(inflated, plain) {
		t.Error("inflating the codec output does not reproduce the input")
	}
}

// The array digest has to cover the IV when the IV leads the array, otherwise
// a consumer checking it would have to strip those 16 bytes first and could
// slice in the wrong place.
func TestArrayDigestCoversTheIVWhenItLeadsTheArray(t *testing.T) {
	plain := []byte("payload")
	key := bytes.Repeat([]byte{0x22}, crypt.KeySize)
	m := emit.Meta{Sha256Hex: inputDigest(plain)}

	withIV, metaIV, err := buildPayload(plain,
		pipeline{cipher: crypt.ModeCTR, key: key, ivMode: 1}, m)
	if err != nil {
		t.Fatal(err)
	}
	withoutIV, metaComment, err := buildPayload(plain,
		pipeline{cipher: crypt.ModeCTR, key: key, ivMode: 0}, m)
	if err != nil {
		t.Fatal(err)
	}

	if len(withIV) != len(withoutIV)+aes.BlockSize {
		t.Errorf("iv in array gave %d bytes, iv in comment gave %d", len(withIV), len(withoutIV))
	}
	if metaIV.ArraySumHex != inputDigest(withIV) {
		t.Error("with -iv 1 the array digest does not cover the IV")
	}
	if metaComment.ArraySumHex != inputDigest(withoutIV) {
		t.Error("with -iv 0 the array digest does not cover the ciphertext")
	}
	if !metaIV.IVInArray || metaComment.IVInArray {
		t.Error("IVInArray does not follow -iv")
	}
}

// The tool never overrules the request: input that does not compress still
// goes through the codec and into the array, with a warning on stderr. The
// alternative, silently dropping the codec, would make the shape of the
// output depend on the data.
func TestBuildPayloadDoesNotSkipUnprofitableCompression(t *testing.T) {
	// A tiny input: DEFLATE needs more bytes to describe it than it saves.
	plain := []byte("AB")
	payload, meta, err := buildPayload(plain,
		pipeline{codec: compress.CodecDeflate, level: 9, ivMode: 1},
		emit.Meta{Sha256Hex: inputDigest(plain)})
	if err != nil {
		t.Fatal(err)
	}
	if meta.Codec != compress.CodecDeflate {
		t.Error("the codec was dropped because it did not pay off")
	}
	if meta.RawLen != len(plain) {
		t.Errorf("RawLen is %d, want %d", meta.RawLen, len(plain))
	}
	if !bytes.Equal(inflate(t, payload), plain) {
		t.Error("the array does not inflate back to the input")
	}
	if len(payload) <= len(plain) {
		t.Logf("note: %d bytes of input compressed to %d, which is unusual", len(plain), len(payload))
	}
}

// digestOf returns the raw SHA-256 sum, for comparing against hex headers.
func digestOf(b []byte) []byte {
	sum := sha256.Sum256(b)
	return sum[:]
}

// decryptCTR reverses crypt.ModeCTR through the standard library, so the test
// does not lean on the package under test to check the pipeline's ordering.
func decryptCTR(t *testing.T, payload, key []byte) []byte {
	t.Helper()
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	iv, ct := payload[:aes.BlockSize], payload[aes.BlockSize:]
	out := make([]byte, len(ct))
	cipher.NewCTR(block, iv).XORKeyStream(out, ct)
	return out
}

// inflate decodes a raw DEFLATE stream, the form the generator writes.
func inflate(t *testing.T, data []byte) []byte {
	t.Helper()
	r := flate.NewReader(bytes.NewReader(data))
	defer r.Close()
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("the array is not a decodable raw DEFLATE stream: %v", err)
	}
	return out
}

func TestPickLanguage(t *testing.T) {
	tests := []struct {
		name                 string
		c, cpp, golang, rust bool
		python               bool
		want                 string
		wantErr              string
	}{
		{name: "c", c: true, want: "c"},
		{name: "cpp", cpp: true, want: "cpp"},
		{name: "go", golang: true, want: "go"},
		{name: "rust", rust: true, want: "rust"},
		{name: "python", python: true, want: "python"},
		{name: "none", wantErr: "no output language"},
		{name: "two", c: true, rust: true, wantErr: "only one output language"},
		{name: "all", c: true, cpp: true, golang: true, rust: true, python: true,
			wantErr: "only one output language"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := pickLanguage(tc.c, tc.cpp, tc.golang, tc.rust, tc.python)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want one containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

const key64 = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"

func TestParseKey(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		wantErr string
	}{
		{name: "64 lowercase", in: strings.ToLower(key64)},
		{name: "64 uppercase", in: strings.ToUpper(key64)},
		{name: "0x prefix is tolerated", in: "0x" + key64},
		{name: "0X prefix is tolerated", in: "0X" + strings.ToUpper(key64)},
		{name: "too short", in: key64[:62], wantErr: "64 hex characters"},
		{name: "too long", in: key64 + "ff", wantErr: "64 hex characters"},
		{name: "empty", in: "", wantErr: "64 hex characters"},
		{name: "not hex", in: strings.Repeat("z", 64), wantErr: "not valid hex"},
		{name: "odd length", in: key64[:63], wantErr: "not valid hex"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			key, err := parseKey(tc.in)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want one containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(key) != crypt.KeySize {
				t.Errorf("key is %d bytes, want %d", len(key), crypt.KeySize)
			}
		})
	}
}

func TestPickCipher(t *testing.T) {
	t.Run("neither means plaintext", func(t *testing.T) {
		mode, key, err := pickCipher("", "", false, false)
		if err != nil || mode != "" || key != nil {
			t.Fatalf("got mode=%q key=%v err=%v", mode, key, err)
		}
	})
	t.Run("ctr", func(t *testing.T) {
		mode, _, err := pickCipher(key64, "", true, false)
		if err != nil || mode != crypt.ModeCTR {
			t.Fatalf("got mode=%q err=%v", mode, err)
		}
	})
	t.Run("cbc", func(t *testing.T) {
		mode, _, err := pickCipher("", key64, false, true)
		if err != nil || mode != crypt.ModeCBC {
			t.Fatalf("got mode=%q err=%v", mode, err)
		}
	})
	t.Run("both is rejected", func(t *testing.T) {
		if _, _, err := pickCipher(key64, key64, true, true); err == nil ||
			!strings.Contains(err.Error(), "mutually exclusive") {
			t.Fatalf("err = %v, want a mutual exclusion error", err)
		}
	})
	t.Run("a bad key is reported even with a valid mode", func(t *testing.T) {
		if _, _, err := pickCipher("short", "", true, false); err == nil {
			t.Fatal("a short key was accepted")
		}
	})

	// An empty key is what an unset shell variable puts on the command line,
	// and reading a -ctr with an empty value as "no encryption" would write
	// the file in the clear and exit zero. What the flag was given matters,
	// not whether the value looks like the absent case.
	t.Run("an empty ctr key is refused, not read as plaintext", func(t *testing.T) {
		if _, _, err := pickCipher("", "", true, false); err == nil ||
			!strings.Contains(err.Error(), "64 hex characters") {
			t.Fatalf("err = %v, want a key length error", err)
		}
	})
	t.Run("an empty cbc key is refused, not read as plaintext", func(t *testing.T) {
		if _, _, err := pickCipher("", "", false, true); err == nil ||
			!strings.Contains(err.Error(), "64 hex characters") {
			t.Fatalf("err = %v, want a key length error", err)
		}
	})
	t.Run("an empty ctr next to a real cbc is still two ciphers", func(t *testing.T) {
		if _, _, err := pickCipher("", key64, true, true); err == nil ||
			!strings.Contains(err.Error(), "mutually exclusive") {
			t.Fatalf("err = %v, want a mutual exclusion error", err)
		}
	})
}

func TestPickIV(t *testing.T) {
	tests := []struct {
		name    string
		v       int
		given   bool
		cipher  string
		want    int
		wantErr string
	}{
		{name: "not given defaults to the IV leading the array", v: -1, cipher: crypt.ModeCTR, want: 1},
		{name: "not given defaults even without a cipher", v: -1, want: 1},
		{name: "0 keeps the IV in the comment", v: 0, given: true, cipher: crypt.ModeCTR, want: 0},
		{name: "1 leads the array", v: 1, given: true, cipher: crypt.ModeCBC, want: 1},
		{name: "given without a cipher is refused", v: 0, given: true,
			wantErr: "-iv only applies"},
		// An explicit -1 is a bug in the caller, not the absent case, and it
		// used to be indistinguishable from one because -1 is the sentinel.
		{name: "an explicit -1 is out of range, not absent", v: -1, given: true, cipher: crypt.ModeCTR,
			wantErr: "accepts 0 or 1, got -1"},
		{name: "2 is out of range", v: 2, given: true, cipher: crypt.ModeCTR,
			wantErr: "accepts 0 or 1, got 2"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := pickIV(tc.v, tc.given, tc.cipher)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want one containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %d, want %d", got, tc.want)
			}
		})
	}
}

func TestResolvePaths(t *testing.T) {
	dir := t.TempDir()
	withSep := dir + string(filepath.Separator)

	t.Run("default lands in the current directory", func(t *testing.T) {
		id, out, err := resolvePaths("assets/logo.png", "", "", "h")
		if err != nil {
			t.Fatal(err)
		}
		if id != "logo_png" || out != "logo_png.h" {
			t.Errorf("got id=%q out=%q, want logo_png and logo_png.h", id, out)
		}
	})

	t.Run("output directory keeps the derived name", func(t *testing.T) {
		id, out, err := resolvePaths("logo.png", withSep, "", "go")
		if err != nil {
			t.Fatal(err)
		}
		if id != "logo_png" || out != filepath.Join(dir, "logo_png.go") {
			t.Errorf("got id=%q out=%q", id, out)
		}
	})

	t.Run("output file drives the identifier", func(t *testing.T) {
		id, out, err := resolvePaths("logo.png", "some/where/renamed.rs", "", "rs")
		if err != nil {
			t.Fatal(err)
		}
		if id != "renamed" || out != "some/where/renamed.rs" {
			t.Errorf("got id=%q out=%q", id, out)
		}
	})

	t.Run("name overrides everything", func(t *testing.T) {
		id, out, err := resolvePaths("logo.png", withSep, "custom", "rs")
		if err != nil {
			t.Fatal(err)
		}
		if id != "custom" || out != filepath.Join(dir, "custom.rs") {
			t.Errorf("got id=%q out=%q", id, out)
		}
	})

	t.Run("a digit leading name gets an underscore", func(t *testing.T) {
		id, _, err := resolvePaths("1logo.png", "", "", "h")
		if err != nil {
			t.Fatal(err)
		}
		if id != "_1logo_png" {
			t.Errorf("got id=%q, want _1logo_png", id)
		}
	})

	t.Run("a name with no usable ASCII left is refused", func(t *testing.T) {
		// Nothing survives sanitizing, so guessing would be worse than failing.
		if _, _, err := resolvePaths("图标", "", "", "h"); err == nil {
			t.Fatal("expected an error telling the user to pass -name")
		}
	})

	t.Run("non-ASCII next to ASCII degrades but still works", func(t *testing.T) {
		// The ASCII part survives and a warning is printed, so the user can
		// still get output without passing -name.
		id, _, err := resolvePaths("图标.png", "", "", "h")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if id != "png" {
			t.Errorf("got id=%q, want png", id)
		}
	})

	t.Run("missing parent directories are created", func(t *testing.T) {
		nested := filepath.Join(dir, "a", "b", "c") + string(filepath.Separator)
		if _, out, err := resolvePaths("logo.png", nested, "", "h"); err != nil {
			t.Fatal(err)
		} else if out != filepath.Join(dir, "a", "b", "c", "logo_png.h") {
			t.Errorf("got out=%q", out)
		}
	})
}

// Both spellings of "write where the input is" have to be refused: -o naming
// the input outright, and the default name derived from a file that already
// carries the target extension. The input is read into memory before
// anything is written, so without the check the run would succeed and leave
// the file replaced by generated source.
func TestRunRefusesToWriteOverTheInput(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "widget.h")
	const content = "not actually a header"
	if err := os.WriteFile(in, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, args := range [][]string{
		{"-f", in, "-c", "-o", in},
		{"-f", in, "-c", "-o", dir},
	} {
		err := Run(args)
		if err == nil || !strings.Contains(err.Error(), "input file") {
			t.Fatalf("Run(%v) = %v, want a refusal naming the input file", args, err)
		}
		if got, readErr := os.ReadFile(in); readErr != nil || string(got) != content {
			t.Fatalf("Run(%v) wrote over the input: %q, %v", args, got, readErr)
		}
	}
}

// Flags whose empty or sentinel value used to be indistinguishable from "not
// given" must be refused instead of silently changing what the run does: a
// -ctr with an empty value would have written a plaintext array and exited
// zero.
func TestRunRefusesValuesThatLookLikeNotGiven(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "a.bin")
	if err := os.WriteFile(in, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		args []string
		want string
	}{
		{"an empty ctr key", []string{"-f", in, "-c", "-ctr", ""},
			"key must be 64 hex characters"},
		{"an empty cbc key", []string{"-f", in, "-c", "-cbc", ""},
			"key must be 64 hex characters"},
		{"an explicit -iv -1", []string{"-f", in, "-c", "-ctr", key64, "-iv", "-1"},
			"-iv accepts 0 or 1, got -1"},
		{"an empty codec name", []string{"-f", in, "-c", "-compress", ""},
			"needs a codec name"},
		{"an explicit -level -1", []string{"-f", in, "-c", "-compress", "deflate", "-level", "-1"},
			"takes -level 1..9, got -1"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := Run(tc.args)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Run = %v, want an error containing %q", err, tc.want)
			}
		})
	}
}

// The -b64 and --base64 spellings both have to reach the generator, and the
// file it writes has to carry the base64 constant instead of the array.
func TestRunBase64Flag(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "widget.bin")
	if err := os.WriteFile(in, []byte("hello base64"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, flag := range []string{"-b64", "--base64"} {
		t.Run(flag, func(t *testing.T) {
			out := t.TempDir()
			if err := Run([]string{"-f", in, "-c", flag, "-o", out}); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(filepath.Join(out, "widget_bin.h"))
			if err != nil {
				t.Fatal(err)
			}
			src := string(got)
			if !strings.Contains(src, "widget_bin_b64") {
				t.Errorf("no base64 constant in:\n%s", src)
			}
			if strings.Contains(src, "unsigned char widget_bin[]") {
				t.Errorf("the byte array was emitted as well:\n%s", src)
			}
		})
	}
}

// The help text has to mention the flag, or the only way to find it is the
// repository.
func TestUsageListsTheBase64Flag(t *testing.T) {
	if got := capture(t, printUsage); !strings.Contains(got, "-b64, --base64") {
		t.Error("the help text does not mention -b64")
	}
}

func TestIsIdent(t *testing.T) {
	valid := []string{"main", "assets", "_x", "a1", "CamelCase", "_"}
	for _, s := range valid {
		if !isIdent(s) {
			t.Errorf("isIdent(%q) = false, want true", s)
		}
	}
	invalid := []string{"", "1bad", "has space", "has-dash", "dot.dot", "图标"}
	for _, s := range invalid {
		if isIdent(s) {
			t.Errorf("isIdent(%q) = true, want false", s)
		}
	}
}

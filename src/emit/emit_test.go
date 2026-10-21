package emit

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"bin2source/src/compress"
	"bin2source/src/crypt"
)

// sample spans three source lines plus a partial one, and starts with a zero
// byte so a dropped leading element cannot hide.
func sample() []byte {
	b := make([]byte, 0, 200)
	b = append(b, 0x00)
	for i := 1; i < 200; i++ {
		b = append(b, byte(i))
	}
	return b
}

// sha256Sum is the reference digest the generators are checked against.
func sha256Sum(data []byte) []byte {
	sum := sha256.Sum256(data)
	return sum[:]
}

func generate(t *testing.T, lang string, data []byte, m Meta) string {
	t.Helper()
	g, ok := Get(lang)
	if !ok {
		t.Fatalf("no generator registered for %q", lang)
	}
	var buf bytes.Buffer
	if err := g.Generate(&buf, data, m); err != nil {
		t.Fatalf("Generate(%s): %v", lang, err)
	}
	return buf.String()
}

// hexByteRE finds array elements. The comment never uses a 0x prefix, so this
// only ever matches the payload.
var hexByteRE = regexp.MustCompile(`0x([0-9a-f]{2})`)

// hexTextRE finds the contents of a double quoted string.
var hexTextRE = regexp.MustCompile(`"([0-9a-f]*)"`)

// anyTextRE finds the contents of a double quoted literal of any alphabet,
// including one a Rust continuation breaks across lines.
var anyTextRE = regexp.MustCompile(`(?s)"([^"]*)"`)

// quotedText returns everything inside double quotes in s. It is how the
// related test reads back the digest literal, which the generator may split
// over two lines.
func quotedText(s string) string {
	var b strings.Builder
	for _, m := range hexTextRE.FindAllStringSubmatch(s, -1) {
		b.WriteString(m[1])
	}
	return b.String()
}

// arrayBody returns the source between the payload's opening and closing
// braces so the hex patterns above cannot reach anything else in the file.
func arrayBody(t *testing.T, lang, src string) string {
	t.Helper()

	open, close := "{", "}"
	if lang == LangRust {
		open, close = "[", "]"
	}
	i := strings.Index(src, open)
	j := strings.LastIndex(src, close)
	if i < 0 || j < i {
		t.Fatalf("%s: no array found in:\n%s", lang, src)
	}
	return src[i+1 : j]
}

// declaredText returns the string a digest declaration folds down to.
//
// The value is scraped by walking the quotes, not by looking for the first
// newline or closing parenthesis: the shared comment at the top of the file
// mentions "(array[0:16])" long before the declaration turns up.
func declaredText(t *testing.T, src, name string) string {
	t.Helper()

	i := strings.Index(src, name)
	if i < 0 {
		t.Fatalf("no declaration of %q in:\n%s", name, src)
	}
	// The name is the whole left hand side, "=" included, so the first literal
	// after it belongs to this declaration and nothing else.
	rest := src[i+len(name):]

	var b strings.Builder
	for {
		open := strings.IndexByte(rest, '"')
		if open < 0 {
			t.Fatalf("the declaration of %q has no string in:\n%s", name, src)
		}
		rest = rest[open+1:]

		end := strings.IndexByte(rest, '"')
		if end < 0 {
			t.Fatalf("unterminated string in the declaration of %q in:\n%s", name, src)
		}
		b.WriteString(rest[:end])
		rest = rest[end+1:]

		// Only whitespace, a comma or a newline may separate this literal from
		// the next one; anything else ends the declaration.
		trimmed := strings.TrimSpace(rest)
		if !strings.HasPrefix(trimmed, `"`) {
			break
		}
		rest = trimmed
	}
	return b.String()
}

// base64Name is the identifier the base64 payload constant is declared under,
// in the language's own casing.
func base64Name(lang, base string) string {
	switch lang {
	case LangGo:
		return Pascal(base) + "B64"
	case LangRust, LangPython:
		return strings.ToUpper(base) + "_B64"
	}
	return base + "_b64"
}

// base64Constant returns the base64 text a generated file carries, starting at
// the first quoted literal after ident. wantLen is the length the text should
// have, which fixes how many quoted chunks belong to it, so the digest
// literals further down are never collected by accident.
//
// The chunks are glued back by dropping the separators, because the languages
// disagree about where those go: nothing between adjacent literals for C, C++
// and Python, a plus for Go, and a backslash plus the line that follows for
// Rust, which keeps its text inside one literal.
func base64Constant(t *testing.T, src, ident string, wantLen int) string {
	t.Helper()

	i := strings.Index(src, ident)
	if i < 0 {
		t.Fatalf("no %s in:\n%s", ident, src)
	}

	clean := strings.NewReplacer("\\", "", "\n", "", "\r", "", "\t", "", " ", "").Replace
	var b strings.Builder
	for _, m := range anyTextRE.FindAllStringSubmatch(src[i:], -1) {
		b.WriteString(clean(m[1]))
		if b.Len() >= wantLen {
			break
		}
	}
	return b.String()
}

func TestArrayContentSurvivesGeneration(t *testing.T) {
	data := sample()
	want := hex.EncodeToString(data)

	for _, lang := range []string{LangC, LangCpp, LangGo, LangRust} {
		t.Run(lang, func(t *testing.T) {
			src := generate(t, lang, data, Meta{Name: "logo_png"})

			// Only the array body, so the digest literal in the same file
			// cannot be mistaken for payload.
			var got strings.Builder
			for _, m := range hexByteRE.FindAllStringSubmatch(arrayBody(t, lang, src), -1) {
				got.WriteString(m[1])
			}
			if got.String() != want {
				t.Errorf("array does not reproduce the input: %d hex chars, want %d",
					got.Len(), len(want))
			}
		})
	}

	t.Run("python", func(t *testing.T) {
		src := generate(t, LangPython, data, Meta{Name: "logo_png"})

		// bytes.fromhex takes one or more adjacent string literals.
		i := strings.Index(src, "bytes.fromhex(")
		j := strings.LastIndex(src, ")")
		if i < 0 || j < i {
			t.Fatalf("no bytes.fromhex call in:\n%s", src)
		}
		if got := quotedText(src[i:j]); got != want {
			t.Errorf("hex literals do not reproduce the input: %d chars, want %d",
				len(got), len(want))
		}
	})
}

// A newline inside a Go composite literal is only legal when the preceding
// element ends with a comma. Missing ones are a syntax error, not a warning.
func TestGoArrayLinesEndWithAComma(t *testing.T) {
	src := generate(t, LangGo, sample(), Meta{Name: "logo_png"})

	inArray, lines := false, 0
	for _, line := range strings.Split(src, "\n") {
		switch {
		case strings.HasSuffix(line, "= []byte{"):
			inArray = true
		case inArray && line == "}":
			inArray = false
		case inArray:
			lines++
			if !strings.HasSuffix(line, ",") {
				t.Errorf("line %q does not end with a comma", line)
			}
		}
	}
	if lines == 0 {
		t.Fatal("never found the array body")
	}
}

func TestLengthConstantMatchesTheArray(t *testing.T) {
	data := sample()
	n := len(data)

	tests := []struct {
		lang string
		want string
	}{
		{LangC, "_len = " + strconv.Itoa(n)},
		{LangCpp, "_len = " + strconv.Itoa(n)},
		{LangGo, "Len = len("},
		{LangRust, fmt.Sprintf("[u8; %d]", n)},
		{LangPython, "_LEN = len("},
	}
	for _, tc := range tests {
		t.Run(tc.lang, func(t *testing.T) {
			if src := generate(t, tc.lang, data, Meta{Name: "logo_png"}); !strings.Contains(src, tc.want) {
				t.Errorf("output does not contain %q:\n%s", tc.want, src)
			}
		})
	}

	// Rust has to spell the count out in the type as well.
	if src := generate(t, LangRust, data, Meta{Name: "logo_png"}); !strings.Contains(src, "_LEN: usize") {
		t.Error("Rust output has no length constant")
	}
}

func TestCipherDetailsOnlyAppearWhenEncrypted(t *testing.T) {
	meta := Meta{
		Name:      "logo_png",
		Cipher:    crypt.ModeCBC,
		IVHex:     "00112233445566778899aabbccddeeff",
		KeyHex:    strings.Repeat("ff", 32),
		IVInArray: true,
	}

	for _, lang := range []string{LangC, LangCpp, LangGo, LangRust, LangPython} {
		t.Run(lang+"/encrypted", func(t *testing.T) {
			src := generate(t, lang, sample(), meta)
			for _, want := range []string{
				"AES256CBC (PKCS#7)",
				"iv      : 00112233445566778899aabbccddeeff  (array[0:16])",
				"key     : " + strings.Repeat("ff", 32),
			} {
				if !strings.Contains(src, want) {
					t.Errorf("missing %q", want)
				}
			}
		})

		t.Run(lang+"/plain", func(t *testing.T) {
			src := generate(t, lang, sample(), Meta{Name: "logo_png"})
			for _, unwanted := range []string{"AES256", "iv ", "key "} {
				if strings.Contains(src, unwanted) {
					t.Errorf("plain output mentions %q", unwanted)
				}
			}
		})
	}
}

// stageConst is the declaration each language is expected to produce for the
// identifier base logo_png and a digest suffix such as "sha256",
// "deflate_sha256" or "array_sha256".
func stageConst(lang, suffix string) string {
	switch lang {
	case LangC:
		return "static const char logo_png_" + suffix
	case LangCpp:
		return "static constexpr const char logo_png_" + suffix
	case LangGo:
		return "const LogoPng" + Pascal(suffix) + " ="
	case LangRust:
		return "pub const LOGO_PNG_" + strings.ToUpper(suffix) + ": &str ="
	case LangPython:
		return "LOGO_PNG_" + strings.ToUpper(suffix) + " ="
	}
	return ""
}

// sha256Const is the declaration each language is expected to produce for the
// input file's digest.
func sha256Const(lang string) string { return stageConst(lang, "sha256") }

// The comment describes the pipeline in the order it ran, and says "raw" so a
// consumer's decoder is not left guessing whether a zlib or gzip wrapper is in
// the way. It has to stay out of the output when nothing was compressed.
func TestCompressionLineAppearsOnlyWhenCompressed(t *testing.T) {
	compressed := Meta{
		Name:      "logo_png",
		Codec:     compress.CodecDeflate,
		CodecWire: compress.WireRaw,
		Level:     9,
		RawLen:    300,
	}

	for _, lang := range []string{LangC, LangCpp, LangGo, LangRust, LangPython} {
		t.Run(lang+"/compressed", func(t *testing.T) {
			src := generate(t, lang, sample(), compressed)
			if !strings.Contains(src, "deflate : raw, level 9") {
				t.Errorf("missing the compression line:\n%s", src)
			}
		})

		t.Run(lang+"/plain", func(t *testing.T) {
			src := generate(t, lang, sample(), Meta{Name: "logo_png"})
			for _, unwanted := range []string{"deflate", "raw, level", "level 9"} {
				if strings.Contains(src, unwanted) {
					t.Errorf("uncompressed output mentions %q:\n%s", unwanted, src)
				}
			}
		})
	}
}

// The word after the codec name belongs to the codec, and it is not always
// "raw". Zstandard has no bare variant, so describing its frame as raw would
// send a consumer looking for a decoder option that does not exist. Walking
// the registry keeps the check honest for codecs added later.
func TestWireWordComesFromTheCodec(t *testing.T) {
	for _, name := range compress.Supported() {
		spec, ok := compress.Get(name)
		if !ok {
			t.Fatalf("Supported lists %q but Get does not find it", name)
		}
		t.Run(name, func(t *testing.T) {
			meta := Meta{
				Name:      "logo_png",
				Codec:     name,
				CodecWire: spec.Wire,
				Level:     9,
				RawLen:    300,
			}
			src := generate(t, LangC, sample(), meta)

			want := fmt.Sprintf("%-7s : %s, level 9", name, spec.Wire)
			if !strings.Contains(src, want) {
				t.Errorf("comment is missing %q:\n%s", want, src)
			}
			if spec.Wire != compress.WireRaw && strings.Contains(src, ": raw,") {
				t.Errorf("%s output is %s but the comment claims raw", name, spec.Wire)
			}
		})
	}
}

// The codec line is padded so its colon lands in the same column as the iv and
// key lines, which is what makes the block read as a table.
func TestCommentLabelsLineUp(t *testing.T) {
	meta := Meta{
		Name:      "logo_png",
		Codec:     compress.CodecDeflate,
		CodecWire: compress.WireRaw,
		Level:     9,
		Cipher:    crypt.ModeCBC,
		IVHex:     strings.Repeat("00", 16),
		KeyHex:    strings.Repeat("11", 32),
		IVInArray: true,
	}

	src := generate(t, LangC, sample(), meta)
	for _, want := range []string{
		"deflate : raw, level 9",
		"iv      : " + strings.Repeat("00", 16),
		"key     : " + strings.Repeat("11", 32),
	} {
		if !strings.Contains(src, want) {
			t.Errorf("comment does not line up, missing %q:\n%s", want, src)
		}
	}
}

// rawLenConst is the declaration each language is expected to produce for the
// pre-compression size of logo_png.
func rawLenConst(lang string, n int) string {
	switch lang {
	case LangC:
		return fmt.Sprintf("static const unsigned int logo_png_raw_len = %d;", n)
	case LangCpp:
		return fmt.Sprintf("static constexpr unsigned int logo_png_raw_len = %d;", n)
	case LangGo:
		return fmt.Sprintf("const LogoPngRawLen = %d", n)
	case LangRust:
		return fmt.Sprintf("pub const LOGO_PNG_RAW_LEN: usize = %d;", n)
	case LangPython:
		return fmt.Sprintf("LOGO_PNG_RAW_LEN = %d", n)
	}
	return ""
}

// A raw DEFLATE stream carries no uncompressed length, so without this
// constant a C caller has nothing to size a buffer with. It is also the one
// number that cannot be derived from the array, which is why it is written and
// not left to the consumer.
func TestRawLenIsRecordedOnlyWhenCompressed(t *testing.T) {
	const rawLen = 1047
	compressed := Meta{
		Name:      "logo_png",
		Codec:     compress.CodecDeflate,
		CodecWire: compress.WireRaw,
		Level:     9,
		RawLen:    rawLen,
	}

	for _, lang := range []string{LangC, LangCpp, LangGo, LangRust, LangPython} {
		t.Run(lang+"/compressed", func(t *testing.T) {
			src := generate(t, lang, sample(), compressed)
			want := rawLenConst(lang, rawLen)
			if !strings.Contains(src, want) {
				t.Errorf("missing %q:\n%s", want, src)
			}
		})

		t.Run(lang+"/plain", func(t *testing.T) {
			src := generate(t, lang, sample(), Meta{Name: "logo_png"})
			if strings.Contains(src, "raw_len") || strings.Contains(src, "RawLen") ||
				strings.Contains(src, "RAW_LEN") {
				t.Errorf("uncompressed output carries a raw length:\n%s", src)
			}
		})
	}
}

// The set of digest constants is decided by the flags rather than by a count:
// the input digest is always written, the codec digest exactly when the
// payload was compressed, and the array digest exactly when it was encrypted.
// A consumer can rely on that rule instead of probing for what exists, and no
// digest is ever written twice under two names.
func TestStageDigestsFollowTheFlags(t *testing.T) {
	const (
		inputHex = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
		codecHex = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		arrayHex = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	)

	base := Meta{Name: "logo_png", Sha256Hex: inputHex}
	compressed := base
	compressed.Codec, compressed.CodecWire, compressed.Level = compress.CodecDeflate, compress.WireRaw, 9
	compressed.RawLen = 300
	compressed.CodecSumHex = codecHex

	encrypted := base
	encrypted.Cipher, encrypted.IVHex = crypt.ModeCTR, strings.Repeat("00", 16)
	encrypted.KeyHex, encrypted.IVInArray = strings.Repeat("11", 32), true
	encrypted.ArraySumHex = arrayHex

	both := encrypted
	both.Codec, both.CodecWire, both.Level = compress.CodecDeflate, compress.WireRaw, 9
	both.RawLen = 300
	both.CodecSumHex = codecHex

	languages := []string{LangC, LangCpp, LangGo, LangRust, LangPython}

	tests := []struct {
		name   string
		meta   Meta
		digest map[string]string // suffix -> expected value
		absent []string          // values that must not appear anywhere
	}{
		{
			name:   "plain",
			meta:   base,
			digest: map[string]string{"sha256": inputHex},
			absent: []string{codecHex, arrayHex},
		},
		{
			name:   "compressed",
			meta:   compressed,
			digest: map[string]string{"sha256": inputHex, "deflate_sha256": codecHex},
			absent: []string{arrayHex},
		},
		{
			name:   "encrypted",
			meta:   encrypted,
			digest: map[string]string{"sha256": inputHex, "array_sha256": arrayHex},
			absent: []string{codecHex},
		},
		{
			name: "compressed and encrypted",
			meta: both,
			digest: map[string]string{
				"sha256":         inputHex,
				"deflate_sha256": codecHex,
				"array_sha256":   arrayHex,
			},
		},
	}

	for _, tc := range tests {
		for _, lang := range languages {
			t.Run(tc.name+"/"+lang, func(t *testing.T) {
				src := generate(t, lang, sample(), tc.meta)

				for suffix, want := range tc.digest {
					decl := stageConst(lang, suffix)
					if !strings.Contains(src, decl) {
						t.Errorf("missing the declaration %q:\n%s", decl, src)
						continue
					}
					if got := declaredText(t, src, decl); got != want {
						t.Errorf("%s holds %q, want %q", suffix, got, want)
					}
				}
				for _, value := range tc.absent {
					if strings.Contains(src, value) {
						t.Errorf("a digest that no stage produced is present: %q", value)
					}
				}
			})
		}
	}
}

// The codec name is part of the digest's identifier, so a second codec cannot
// quietly reuse the first one's constant.
func TestCodecNameIsPartOfTheDigestIdentifier(t *testing.T) {
	meta := Meta{
		Name:        "logo_png",
		Codec:       compress.CodecDeflate,
		CodecWire:   compress.WireRaw,
		Level:       9,
		RawLen:      300,
		CodecSumHex: strings.Repeat("aa", 32),
	}

	for _, lang := range []string{LangC, LangCpp, LangGo, LangRust, LangPython} {
		t.Run(lang, func(t *testing.T) {
			src := generate(t, lang, sample(), meta)
			if !strings.Contains(src, stageConst(lang, compress.CodecDeflate+"_sha256")) {
				t.Errorf("the codec digest is not named after the codec:\n%s", src)
			}
		})
	}
}

// Every digest has to be readable as the same 64 lowercase hex characters that
// sha256sum prints, in every language.
func TestSha256ConstantHoldsTheDigest(t *testing.T) {
	want := hex.EncodeToString(sha256Sum(sample()))

	for _, lang := range []string{LangC, LangCpp, LangGo, LangRust, LangPython} {
		t.Run(lang, func(t *testing.T) {
			decl := sha256Const(lang)
			src := generate(t, lang, sample(), Meta{Name: "logo_png", Sha256Hex: want})

			if !strings.Contains(src, decl) {
				t.Errorf("output does not declare the digest as %q:\n%s", decl, src)
			}
			if got := declaredText(t, src, decl); got != want {
				t.Errorf("digest is %q, want %q", got, want)
			}
			if strings.ContainsAny(want, "ABCDEF") {
				t.Fatalf("the test's own digest is not lowercase: %q", want)
			}
		})
	}
}

// The digest describes the input file, so it must not follow the payload into
// the ciphertext. A consumer decrypts the array, hashes the result and needs
// to land on exactly this value.
func TestSha256IsCarriedThroughEncryption(t *testing.T) {
	want := hex.EncodeToString(sha256Sum(sample()))
	meta := Meta{
		Name:      "logo_png",
		Cipher:    crypt.ModeCTR,
		IVHex:     strings.Repeat("ab", 16),
		KeyHex:    strings.Repeat("ff", 32),
		IVInArray: true,
		Sha256Hex: want,
	}

	for _, lang := range []string{LangC, LangCpp, LangGo, LangRust, LangPython} {
		t.Run(lang, func(t *testing.T) {
			src := generate(t, lang, sample(), meta)
			if got := declaredText(t, src, sha256Const(lang)); got != want {
				t.Errorf("encrypted output has digest %q, want %q", got, want)
			}
		})
	}
}

// Missing metadata must not produce a declaration with no digest in it: an
// empty string compares equal to nothing a hash could be, but it would still
// look like a usable check.
func TestEmptyDigestStillDeclaresAnEmptyString(t *testing.T) {
	for _, lang := range []string{LangC, LangCpp, LangGo, LangRust, LangPython} {
		t.Run(lang, func(t *testing.T) {
			src := generate(t, lang, sample(), Meta{Name: "logo_png"})
			if got := declaredText(t, src, sha256Const(lang)); got != "" {
				t.Errorf("empty digest was rendered as %q", got)
			}
		})
	}
}

// A missing semicolon is not a formatting slip in C and C++: it is a syntax
// error in a header that looked fine and reached the compiler. Go and Rust
// want semicolons too, Python wants none.
//
// The assertion is a plain containment test on the generated text, which is
// what the compiler sees: the digest literal followed immediately by whatever
// the language puts after it.
func TestSha256DeclarationIsTerminated(t *testing.T) {
	want := hex.EncodeToString(sha256Sum(sample()))
	tests := []struct {
		lang string
		decl string
		end  string
	}{
		{LangC, "static const char logo_png_sha256", "\"" + want + "\"\n);"},
		{LangCpp, "static constexpr const char logo_png_sha256", "\"" + want + "\"\n);"},
		{LangGo, "const LogoPngSha256 =", "\"" + want + "\"\n"},
		{LangRust, "pub const LOGO_PNG_SHA256: &str =", "\"" + want + "\";"},
		{LangPython, "LOGO_PNG_SHA256 =", "\"" + want + "\"\n)"},
	}

	for _, tc := range tests {
		t.Run(tc.lang, func(t *testing.T) {
			src := generate(t, tc.lang, sample(), Meta{Name: "logo_png", Sha256Hex: want})

			// Searching from the declaration name keeps the payload array
			// above it out of the picture.
			rest := src[strings.Index(src, tc.decl)+len(tc.decl):]
			if !strings.Contains(rest, tc.end) {
				t.Errorf("declaration of %q does not close with %q:\n%s",
					tc.decl, tc.end, rest)
			}
		})
	}
}

// No generated line may carry trailing whitespace, and the digest itself must
// stay lowercase: it is compared against tool output as text.
//
// The metadata here turns every optional declaration on at once, because the
// wrapped literal form is where a stray blank would hide.
func TestSha256DeclarationIsClean(t *testing.T) {
	want := hex.EncodeToString(sha256Sum(sample()))
	meta := Meta{
		Name:        "logo_png",
		Codec:       compress.CodecDeflate,
		CodecWire:   compress.WireRaw,
		Level:       9,
		RawLen:      400,
		Cipher:      crypt.ModeCBC,
		IVHex:       strings.Repeat("ab", 16),
		KeyHex:      strings.Repeat("ff", 32),
		IVInArray:   true,
		Sha256Hex:   want,
		CodecSumHex: strings.Repeat("cd", 32),
		ArraySumHex: strings.Repeat("ef", 32),
	}

	for _, tc := range []struct {
		name   string
		base64 bool
	}{{"hex", false}, {"base64", true}} {
		m := meta
		m.Base64 = tc.base64
		for _, lang := range []string{LangC, LangCpp, LangGo, LangRust, LangPython} {
			t.Run(tc.name+"/"+lang, func(t *testing.T) {
				src := generate(t, lang, sample(), m)

				for i, line := range strings.Split(src, "\n") {
					if strings.TrimRight(line, " \t") != line {
						t.Errorf("line %d has trailing whitespace: %q", i+1, line)
					}
				}
				if got := declaredText(t, src, sha256Const(lang)); got != strings.ToLower(want) {
					t.Errorf("digest %q is not the lowercase hex text %q", got, strings.ToLower(want))
				}
			})
		}
	}
}

func TestIVPositionIsStated(t *testing.T) {
	for _, tc := range []struct {
		inArray bool
		want    string
	}{
		{true, "(array[0:16])"},
		{false, "(comment only)"},
	} {
		meta := Meta{
			Name:      "x",
			Cipher:    crypt.ModeCTR,
			IVHex:     strings.Repeat("00", 16),
			KeyHex:    strings.Repeat("11", 32),
			IVInArray: tc.inArray,
		}
		if src := generate(t, LangC, sample(), meta); !strings.Contains(src, tc.want) {
			t.Errorf("IVInArray=%v: output does not say %q", tc.inArray, tc.want)
		}
	}
}

// Empty input must still produce something the target language accepts.
func TestEmptyInput(t *testing.T) {
	for _, lang := range []string{LangC, LangCpp, LangGo, LangRust, LangPython} {
		t.Run(lang, func(t *testing.T) {
			g, _ := Get(lang)
			var buf bytes.Buffer
			if err := g.Generate(&buf, nil, Meta{Name: "empty"}); err != nil {
				t.Fatalf("Generate: %v", err)
			}
			src := buf.String()

			// The length constant must say zero even though C needs a
			// placeholder element to have a legal empty initialiser.
			reportsZero := strings.Contains(src, "= 0") || // C, C++ literal
				strings.Contains(src, ".len()") // Rust
			if !reportsZero && !strings.Contains(src, "len(") { // Go, Python
				t.Errorf("empty output does not report a zero length:\n%s", src)
			}
			if lang == LangGo && !strings.Contains(src, "[]byte{\n}") {
				t.Errorf("Go empty slice is not empty:\n%s", src)
			}
		})
	}
}

func TestIncludeGuardMatchesTheIdentifier(t *testing.T) {
	// C and C++ keep the lower snake name and add a guard around it.
	if src := generate(t, LangC, sample(), Meta{Name: "_1logo_png"}); !strings.Contains(src, "#ifndef _1LOGO_PNG_H") {
		t.Errorf("C guard is wrong:\n%s", src[:200])
	}
	if src := generate(t, LangCpp, sample(), Meta{Name: "logo"}); !strings.Contains(src, "#ifndef LOGO_HPP") {
		t.Errorf("C++ guard is wrong:\n%s", src[:200])
	}
}

func TestRustModuleWrapper(t *testing.T) {
	plain := generate(t, LangRust, sample(), Meta{Name: "logo"})
	if strings.Contains(plain, "pub mod") {
		t.Error("a module was added without -pkg")
	}

	wrapped := generate(t, LangRust, sample(), Meta{Name: "logo", Pkg: "assets"})
	if !strings.Contains(wrapped, "pub mod assets {") || !strings.HasSuffix(wrapped, "}\n") {
		t.Errorf("Rust module wrapper is malformed:\n%s", wrapped)
	}
	if !strings.Contains(wrapped, "\tpub const LOGO") {
		t.Error("module contents are not indented")
	}
}

func TestGoPackageDefaultsToMain(t *testing.T) {
	if src := generate(t, LangGo, sample(), Meta{Name: "logo"}); !strings.Contains(src, "package main") {
		t.Error("Go output does not default to package main")
	}
	if src := generate(t, LangGo, sample(), Meta{Name: "logo", Pkg: "assets"}); !strings.Contains(src, "package assets") {
		t.Error("-pkg was ignored")
	}
	// The marker the Go toolchain looks for to skip generated files.
	if src := generate(t, LangGo, sample(), Meta{Name: "logo"}); !strings.HasPrefix(src, "// Code generated by bin2source. DO NOT EDIT.\n") {
		t.Error("Go output is missing the generated-file marker")
	}
}

// constantNoteNames pulls the identifiers out of the "Constants:" section of a
// generated comment. A note line is the identifier, some padding, and then the
// description, which is enough to take the first field of each line once the
// section has started.
func constantNoteNames(src string) []string {
	var names []string
	inSection := false
	for _, line := range strings.Split(src, "\n") {
		trimmed := strings.TrimLeft(strings.TrimSpace(line), "*/#")
		trimmed = strings.TrimSpace(trimmed)
		if trimmed == "Constants:" {
			inSection = true
			continue
		}
		if !inSection {
			continue
		}
		if trimmed == "" {
			// The next blank line ends the section.
			if len(names) > 0 {
				break
			}
			continue
		}
		if f := strings.Fields(trimmed); len(f) > 0 {
			names = append(names, f[0])
		}
	}
	return names
}

// The notes are only worth writing if they name the constants that actually
// follow. A comment describing logo_png_sha256 while the code declares
// logo_png_raw_sha256 is worse than no comment at all, so each name in the
// section has to appear in the code below it.
func TestConstantNotesNameTheConstantsThatFollow(t *testing.T) {
	meta := Meta{
		Name:        "logo_png",
		Codec:       compress.CodecZstd,
		CodecWire:   compress.WireFrame,
		Level:       19,
		RawLen:      400,
		Cipher:      crypt.ModeCBC,
		IVHex:       strings.Repeat("ab", 16),
		KeyHex:      strings.Repeat("ff", 32),
		IVInArray:   true,
		Sha256Hex:   hex.EncodeToString(sha256Sum(sample())),
		CodecSumHex: strings.Repeat("cd", 32),
		ArraySumHex: strings.Repeat("ef", 32),
	}

	for _, tc := range []struct {
		name   string
		base64 bool
	}{{"hex", false}, {"base64", true}} {
		m := meta
		m.Base64 = tc.base64
		for _, lang := range []string{LangC, LangCpp, LangGo, LangRust, LangPython} {
			t.Run(tc.name+"/"+lang, func(t *testing.T) {
				src := generate(t, lang, sample(), m)

				names := constantNoteNames(src)
				if len(names) == 0 {
					t.Fatalf("no constants were described:/n%s", src)
				}
				// Once in the comment, once where it is declared.
				for _, name := range names {
					if n := strings.Count(src, name); n < 2 {
						t.Errorf("the note names %q but it appears %d time(s) in the output", name, n)
					}
				}
				// The array, both lengths and all three digests.
				if len(names) != 6 {
					t.Errorf("described %d constants, want 6: %v", len(names), names)
				}
			})
		}
	}
}

// A plain copy of the file needs no glossary: the array is the file, so there
// is only one thing each constant could mean.
func TestPlainOutputHasNoConstantNotes(t *testing.T) {
	for _, lang := range []string{LangC, LangCpp, LangGo, LangRust, LangPython} {
		t.Run(lang, func(t *testing.T) {
			src := generate(t, lang, sample(), Meta{Name: "logo_png"})
			if strings.Contains(src, "Constants:") {
				t.Errorf("untransformed output explains constants:/n%s", src)
			}
		})
	}
}

// The descriptions follow the pipeline: a payload that was never encrypted
// should not be described in terms of encrypting.
func TestConstantNotesDescribeOnlyTheStepsThatRan(t *testing.T) {
	compressed := Meta{
		Name: "logo_png", Codec: compress.CodecDeflate, CodecWire: compress.WireRaw,
		Level: 9, RawLen: 400, Sha256Hex: hex.EncodeToString(sha256Sum(sample())),
		CodecSumHex: strings.Repeat("cd", 32),
	}

	src := generate(t, LangC, sample(), compressed)
	if strings.Contains(src, "encrypting") {
		t.Errorf("a compressed but unencrypted payload mentions encrypting:/n%s", src)
	}
	if !strings.Contains(src, "digest of the input file, before compressing") {
		t.Error("the input digest is not placed before compressing")
	}
}

// The base64 text has to decode back to the exact payload, in every language:
// the encoding is only worth anything if a consumer's decoder reads what the
// generator meant.
func TestBase64ConstantDecodesToThePayload(t *testing.T) {
	data := sample()
	want := base64.StdEncoding.EncodeToString(data)

	for _, lang := range []string{LangC, LangCpp, LangGo, LangRust, LangPython} {
		t.Run(lang, func(t *testing.T) {
			src := generate(t, lang, data, Meta{Name: "logo_png", Base64: true})

			got := base64Constant(t, src, base64Name(lang, "logo_png"), len(want))
			if got != want {
				t.Fatalf("the constant carries %d characters, want %d", len(got), len(want))
			}
			decoded, err := base64.StdEncoding.DecodeString(got)
			if err != nil {
				t.Fatalf("the constant is not decodable base64: %v", err)
			}
			if !bytes.Equal(decoded, data) {
				t.Errorf("decoding the constant gave %d bytes, want %d", len(decoded), len(data))
			}
		})
	}
}

// The encoding of nothing is an empty string, which still has to come out as a
// legal literal with a legal length constant in every language.
func TestBase64EmptyPayload(t *testing.T) {
	length := map[string]string{
		LangC:      "empty_len = 0;",
		LangCpp:    "empty_len = 0;",
		LangGo:     "const EmptyLen = 0",
		LangRust:   "EMPTY_LEN: usize = 0;",
		LangPython: "EMPTY_LEN = 0",
	}

	for lang, wantLen := range length {
		t.Run(lang, func(t *testing.T) {
			src := generate(t, lang, nil, Meta{Name: "empty", Base64: true})

			if got := base64Constant(t, src, base64Name(lang, "empty"), 0); got != "" {
				t.Errorf("the constant carries %q, want an empty string", got)
			}
			if !strings.Contains(src, `""`) {
				t.Errorf("no empty string literal in:\n%s", src)
			}
			if !strings.Contains(src, wantLen) {
				t.Errorf("the length constant is not %q:\n%s", wantLen, src)
			}
		})
	}
}

// The comment has to say the payload is base64 and to name the constant it
// describes: a consumer left to infer the encoding from the text is a consumer
// who will get it wrong.
func TestBase64IsAnnouncedInTheComment(t *testing.T) {
	meta := Meta{
		Name:        "logo_png",
		Base64:      true,
		Codec:       compress.CodecZstd,
		CodecWire:   compress.WireFrame,
		Level:       19,
		RawLen:      400,
		Cipher:      crypt.ModeCBC,
		IVHex:       strings.Repeat("ab", 16),
		KeyHex:      strings.Repeat("ff", 32),
		IVInArray:   true,
		Sha256Hex:   hex.EncodeToString(sha256Sum(sample())),
		CodecSumHex: strings.Repeat("cd", 32),
		ArraySumHex: strings.Repeat("ef", 32),
	}

	for _, lang := range []string{LangC, LangCpp, LangGo, LangRust, LangPython} {
		t.Run(lang, func(t *testing.T) {
			src := generate(t, lang, sample(), meta)

			if !strings.Contains(src, "base64  : RFC 4648 standard, padded") {
				t.Errorf("the comment does not announce base64:\n%s", src)
			}
			name := base64Name(lang, "logo_png")
			if n := strings.Count(src, name); n < 2 {
				t.Errorf("%s appears %d time(s), want the note and the declaration", name, n)
			}
		})
	}
}

// Without base64 a plain copy of the file needs no glossary; with it the array
// is no longer a copy, so the names stop explaining themselves.
func TestPlainBase64OutputExplainsTheConstants(t *testing.T) {
	src := generate(t, LangC, sample(), Meta{Name: "logo_png", Base64: true})
	if !strings.Contains(src, "Constants:") {
		t.Errorf("base64 output does not explain its constants:\n%s", src)
	}
	if !strings.Contains(src, "bytes the base64 text decodes to") {
		t.Errorf("the length constant is not described in decoded bytes:\n%s", src)
	}
}

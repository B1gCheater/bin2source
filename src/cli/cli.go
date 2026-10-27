// Package cli parses bin2source arguments, validates them and drives the
// encryption and code generation steps.
package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"bin2source/src/compress"
	"bin2source/src/crypt"
	"bin2source/src/emit"
)

const usage = `bin2source - embed any file as a byte array in C, C++, Go, Rust or Python.

Usage:
  bin2source -f <file> <-c|-cpp|-go|-rust|-python> [options]

Required:
  -f, --file <path>        Input file to embed. Exactly one.
  -c                       Emit a C header           (.h)
  -cpp                     Emit a C++ header         (.hpp)
  -go                      Emit a Go source file     (.go)
  -rust                    Emit a Rust source file   (.rs)
  -python                  Emit a Python module      (.py)

Compression (optional, runs before encryption):
  -z, --compress <codec>   Compress the input on its way into the array.

                           deflate   raw DEFLATE, RFC 1951. 2 bytes of
                                     overhead on empty input and a few
                                     on small ones, read back through zlib.

                           zstd      Zstandard, RFC 8478. 9 bytes of
                                     overhead on empty input and about
                                     13 on small ones, and the better
                                     choice for anything with repeats
                                     further apart than 32 KB, which
                                     DEFLATE cannot see.

  --level <n>              Compression level, and each codec owns its range:
                           deflate 1..9, default 9; zstd 1..22, default 19.
                           A higher level looks harder for matches and so
                           usually gives fewer bytes, but neither format
                           promises strictly fewer; go with the default
                           unless you have measured this file.
                           zstd frames declare their content size, so a
                           decoder sizes itself by the asset rather than
                           by this number. Small resources still favour
                           deflate: its frame starts at 2 bytes, zstd's
                           at 9.
                           Only meaningful together with -compress.

Encryption (optional, AES-256):
  -ctr,  --aes256ctr <hex> Encrypt with AES-256-CTR.
                           64 hex characters, no 0x prefix.

  -cbc,  --aes256cbc <hex> Encrypt with AES-256-CBC and PKCS#7 padding.
                           64 hex characters, no 0x prefix.

  -iv <0|1>                0  The IV goes in the source comment only and the
                              array holds the ciphertext alone.
							  
                          1  The IV is prepended to the array as its first
                              16 bytes. This is the default.
                           Only meaningful together with -ctr or -cbc.

Output:
  -o, --output <path>      Output file, or a directory that keeps the default
                           file name. Default: ./<name>.<ext>
  -name <ident>            Override the identifier base name.
  -pkg <ident>             Go package name (default main) and Rust module
                           name (default: no module wrapper).
  -b64, --base64           Write the payload as base64 text instead of a hex
                           byte array. The source file shrinks to about a
                           quarter, at the price of one decode in the
                           consumer: the bytes, the lengths and the digests
                           all keep their meaning. It is applied last, to
                           the bytes as they are after compression and after
                           encryption.

Other:
  -h, --help               Show this help.
`

// Run executes the tool with the arguments that follow the program name.
func Run(args []string) error {
	fs := flag.NewFlagSet("bin2source", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // errors are reported by us, not by the flag package

	var (
		file                     string
		langC, langCpp           bool
		langGo, langRust, langPy bool
		ctrKey, cbcKey           string
		ivMode                   int
		codecName                string
		level                    int
		output, name, pkg        string
		base64Out                bool
	)

	// Short and long spellings point at the same variable, so the flag
	// package happily accepts both -f and --file for a single option.
	fs.StringVar(&file, "f", "", "")
	fs.StringVar(&file, "file", "", "")
	fs.BoolVar(&langC, "c", false, "")
	fs.BoolVar(&langCpp, "cpp", false, "")
	fs.BoolVar(&langGo, "go", false, "")
	fs.BoolVar(&langRust, "rust", false, "")
	fs.BoolVar(&langPy, "python", false, "")
	fs.StringVar(&ctrKey, "ctr", "", "")
	fs.StringVar(&ctrKey, "aes256ctr", "", "")
	fs.StringVar(&cbcKey, "cbc", "", "")
	fs.StringVar(&cbcKey, "aes256cbc", "", "")
	fs.IntVar(&ivMode, "iv", -1, "") // -1 marks "not given"
	fs.StringVar(&codecName, "z", "", "")
	fs.StringVar(&codecName, "compress", "", "")
	fs.IntVar(&level, "level", -1, "") // -1 marks "not given"
	fs.StringVar(&output, "o", "", "")
	fs.StringVar(&output, "output", "", "")
	fs.StringVar(&name, "name", "", "")
	fs.StringVar(&pkg, "pkg", "", "")
	fs.BoolVar(&base64Out, "b64", false, "")
	fs.BoolVar(&base64Out, "base64", false, "")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			printUsage()
			return nil
		}
		return err
	}

	// The flag variables use shapes that mean "not given" (-1, ""), and a real
	// value can wear the same shape: -ctr "" and -level -1 both parse as if the
	// option had been left off. Visit reports the flags that were actually on
	// the command line, so the two readings can be told apart where the
	// difference matters: doing nothing silently is how a mistyped script
	// ships an unencrypted file.
	given := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { given[f.Name] = true })

	// "-compress --level 9" is accepted by the flag package as -compress with
	// the value "--level", leaving the 9 behind as a stray argument. Reporting
	// the stray argument would send the user looking in the wrong place, so
	// the swallowed flag is caught first.
	if strings.HasPrefix(codecName, "-") {
		return missingCodecError(codecName)
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected extra argument %q (see -h)", fs.Arg(0))
	}

	lang, err := pickLanguage(langC, langCpp, langGo, langRust, langPy)
	if err != nil {
		return err
	}
	if file == "" {
		return errors.New("no input file given: -f/--file is required")
	}
	if pkg != "" && !isIdent(pkg) {
		return fmt.Errorf("-pkg %q is not a valid identifier", pkg)
	}

	gen, ok := emit.Get(lang)
	if !ok {
		return fmt.Errorf("no generator registered for %q", lang)
	}

	mode, key, err := pickCipher(ctrKey, cbcKey,
		given["ctr"] || given["aes256ctr"],
		given["cbc"] || given["aes256cbc"])
	if err != nil {
		return err
	}
	ivMode, err = pickIV(ivMode, given["iv"], mode)
	if err != nil {
		return err
	}

	codec, lvl, err := pickCompression(codecName, level,
		given["z"] || given["compress"], given["level"])
	if err != nil {
		return err
	}

	info, err := os.Stat(file)
	if err != nil {
		return fmt.Errorf("input file: %w", err)
	}
	if info.IsDir() {
		return fmt.Errorf("input file %s is a directory", file)
	}

	id, outPath, err := resolvePaths(file, output, name, gen.Ext())
	if err != nil {
		return err
	}

	// The output may not be the input. Everything is read into memory before
	// anything is written, so the collision would not fail here; it would
	// succeed and quietly replace the file being embedded with generated
	// source, which is how "bin2source -f widget.h -c" eats widget.h. Ask the
	// file system rather than compare strings, so a relative path, a case
	// fold or a symlink spelling the same file is still seen for what it is.
	if outInfo, statErr := os.Stat(outPath); statErr == nil && os.SameFile(info, outInfo) {
		return fmt.Errorf("output file %s is the input file: pass a different -o", outPath)
	}

	plain, err := os.ReadFile(file)
	if err != nil {
		return fmt.Errorf("reading %s: %w", file, err)
	}

	// The digest is taken over exactly the bytes read from disk, before
	// compression and before encryption, so it identifies the original file
	// no matter what the rest of the pipeline makes of it.
	sumHex := sha256Hex(plain)

	payload, meta, err := buildPayload(plain, pipeline{
		codec:  codec,
		level:  lvl,
		cipher: mode,
		key:    key,
		ivMode: ivMode,
	}, emit.Meta{Name: id, Pkg: pkg, Sha256Hex: sumHex, Base64: base64Out})
	if err != nil {
		return err
	}

	if err := writeSource(outPath, gen, payload, meta); err != nil {
		return err
	}

	// The size before compression is worth printing: it is what a user checks
	// to decide whether -compress earned its place on this file.
	if meta.Codec != "" {
		printSuccess("wrote %s (%d bytes, %d before %s)",
			outPath, len(payload), meta.RawLen, meta.Codec)
	} else {
		printSuccess("wrote %s (%d bytes)", outPath, len(payload))
	}
	return nil
}

// pickLanguage enforces "exactly one target language".
func pickLanguage(c, cpp, golang, rust, python bool) (string, error) {
	var chosen []string
	for _, l := range []struct {
		key string
		set bool
	}{
		{emit.LangC, c},
		{emit.LangCpp, cpp},
		{emit.LangGo, golang},
		{emit.LangRust, rust},
		{emit.LangPython, python},
	} {
		if l.set {
			chosen = append(chosen, "-"+l.key)
		}
	}

	switch len(chosen) {
	case 0:
		return "", errors.New("no output language given: pass exactly one of -c, -cpp, -go, -rust, -python")
	case 1:
		return chosen[0][1:], nil
	default:
		return "", fmt.Errorf("only one output language may be given, got %s", strings.Join(chosen, " "))
	}
}

// pickCipher returns the mode and the decoded key, or an empty mode when the
// payload stays in the clear.
//
// ctrGiven and cbcGiven say whether the flags were on the command line at
// all, which is not the same as whether their values are non-empty: an empty
// key is exactly what an unset shell variable puts there, and reading it as
// "no encryption" would write the file in the clear and exit zero.
func pickCipher(ctrKey, cbcKey string, ctrGiven, cbcGiven bool) (string, []byte, error) {
	if ctrGiven && cbcGiven {
		return "", nil, errors.New("-ctr and -cbc are mutually exclusive")
	}

	mode, keyHex := "", ""
	switch {
	case ctrGiven:
		mode, keyHex = crypt.ModeCTR, ctrKey
	case cbcGiven:
		mode, keyHex = crypt.ModeCBC, cbcKey
	default:
		return "", nil, nil
	}

	// An empty key is not special cased: parseKey reports it with the same
	// words as any other wrong length, "got 0".
	key, err := parseKey(keyHex)
	if err != nil {
		return "", nil, err
	}
	return mode, key, nil
}

// pickIV resolves the -iv flag.
//
// given says whether the flag was on the command line, and it is needed
// because the value alone cannot: -1 is the sentinel for "not given", and a
// user who passes -iv -1 deserves the range error, not a silent default.
// The default is 1, the IV leading the array, so a consumer who ignores the
// comment still has what it needs to decrypt.
func pickIV(v int, given bool, cipher string) (int, error) {
	if given && cipher == "" {
		return 0, errors.New("-iv only applies to encrypted output: pass -ctr or -cbc")
	}
	if !given {
		return 1, nil
	}
	if v != 0 && v != 1 {
		return 0, fmt.Errorf("-iv accepts 0 or 1, got %d", v)
	}
	return v, nil
}

// pickCompression validates -compress and -level together and returns the
// codec to run, or an empty name when the payload reaches the array as it was
// read. codecGiven and levelGiven say whether the flags were on the command
// line; the values alone would confuse "-level -1" with a level that was
// never given, and "-compress with an empty name" with no compression at
// all. Both readings are wrong, and this is where they are told apart.
func pickCompression(name string, level int, codecGiven, levelGiven bool) (string, int, error) {
	if !codecGiven {
		if levelGiven {
			return "", 0, fmt.Errorf("-level only applies to compressed output: pass -compress %s",
				compress.CodecDeflate)
		}
		return "", 0, nil
	}

	// A codec name is required, and this is where "required" has to be
	// enforced rather than left to the flag package: a bare trailing
	// "-compress" fails to parse, but "-compress --level 9" is accepted as
	// -compress with the value "--level", and a -compress given an empty
	// value parses to an empty name. Both mistakes arrive here spelled as a
	// codec.
	if name == "" || strings.HasPrefix(name, "-") {
		return "", 0, missingCodecError(name)
	}

	spec, ok := compress.Get(name)
	if !ok {
		return "", 0, fmt.Errorf("unknown compression codec %q (supported: %s)",
			name, strings.Join(compress.Supported(), ", "))
	}
	if !levelGiven {
		level = spec.DefaultLevel
	}
	if level < spec.MinLevel || level > spec.MaxLevel {
		return "", 0, fmt.Errorf("codec %s takes -level %d..%d, got %d",
			name, spec.MinLevel, spec.MaxLevel, level)
	}
	return name, level, nil
}

// missingCodecError reports a -compress that was handed something other than
// a codec name: the next flag, or nothing at all.
//
// The flag package takes the following argument as the value whatever it
// looks like, so "-compress --level 9" parses as -compress holding "--level"
// and leaves the 9 behind as a stray argument. Depending on where the parse
// stops, that one mistake reaches the caller either as a flag shaped codec or
// as an unexpected extra argument, and reporting the stray argument would
// send the user looking in the wrong place. A -compress given an empty
// value is the same missing name. All spellings are one mistake.
func missingCodecError(got string) error {
	return fmt.Errorf("-compress needs a codec name, got %q: -compress %s",
		got, compress.CodecDeflate)
}

// parseKey decodes a 64 character hex AES-256 key. A 0x prefix is tolerated
// on input even though the tool never writes one.
func parseKey(s string) ([]byte, error) {
	trimmed := strings.TrimPrefix(strings.TrimPrefix(s, "0x"), "0X")

	key, err := hex.DecodeString(trimmed)
	if err != nil {
		return nil, fmt.Errorf("key is not valid hex: %w", err)
	}
	if len(key) != crypt.KeySize {
		return nil, fmt.Errorf("key must be %d hex characters for AES-256, got %d",
			crypt.KeySize*2, len(trimmed))
	}
	return key, nil
}

// sha256Hex returns the lowercase hex SHA-256 of b.
func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// resolvePaths works out the identifier base and where the file is written.
//
// The base name comes from -name when given, otherwise from the output file
// name when that names a file, otherwise from the input file name. The
// language extension is stripped before sanitizing, so "logo.png" to C
// becomes the identifier logo_png in the file logo_png.h.
func resolvePaths(file, output, name, ext string) (id, outPath string, err error) {
	base := name
	if base == "" {
		base = filepath.Base(file)
		if output != "" && !isDirTarget(output) {
			base = filepath.Base(output)
		}
	}
	if suffix := "." + ext; strings.HasSuffix(strings.ToLower(base), suffix) {
		base = base[:len(base)-len(suffix)]
	}

	id = emit.Sanitize(base)
	if id == "" {
		return "", "", fmt.Errorf("cannot build an identifier from %q: pass -name with an ASCII name", base)
	}
	if name == "" && hasNonASCII(base) {
		printWarning("%q has non-ASCII characters, identifier is %q. Pass -name to choose another.", base, id)
	}

	if output == "" || isDirTarget(output) {
		outPath = filepath.Join(output, id+"."+ext)
	} else {
		outPath = output
	}

	if dir := filepath.Dir(outPath); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", "", fmt.Errorf("creating %s: %w", dir, err)
		}
	}
	return id, outPath, nil
}

// pipeline holds the validated transformations applied to the input file on
// its way into the array.
//
// The order is fixed and not configurable, because the two steps are not
// independent choices: compression has to run first. A cipher turns its input
// into something statistically indistinguishable from noise, so a compressor
// fed ciphertext spends time and returns a few bytes more than it was given,
// while compressing first and then encrypting costs nothing extra.
type pipeline struct {
	// codec is the compression codec to run, empty for none.
	codec string

	// level is the level for codec. It is meaningless when codec is empty.
	level int

	// cipher is the AES mode, crypt.ModeCTR or crypt.ModeCBC, empty for none.
	cipher string

	// key is the decoded AES-256 key, nil when cipher is empty.
	key []byte

	// ivMode is 0 to keep the IV in the comment only, 1 to lead the array
	// with it.
	ivMode int
}

// buildPayload runs the pipeline over plain and returns the bytes that go into
// the array together with the metadata the comment and the digest constants
// are built from.
//
// m.Sha256Hex is the lowercase hex SHA-256 of plain exactly as it was read
// from disk, and it is carried through unchanged: it always describes the
// input file, never what the pipeline made of it.
func buildPayload(plain []byte, p pipeline, m emit.Meta) ([]byte, emit.Meta, error) {
	payload := plain

	if p.codec != "" {
		compressed, err := compress.Apply(plain, p.codec, p.level)
		if err != nil {
			return nil, m, err
		}

		// Compression is not a promise that the result is smaller. Input that
		// is already compressed, PNG and JPEG and zip, comes out a few bytes
		// longer because the format still has to describe the blocks. The
		// request is not second guessed: the user named a codec, so the array
		// holds the codec output. The warning is the whole signal, and the fix
		// is not to pass -compress.
		if len(compressed) >= len(plain) {
			printWarning("%s gained nothing: %d -> %d bytes (already compressed input?)",
				p.codec, len(plain), len(compressed))
		}

		payload = compressed
		m.Codec = p.codec
		// Whether this is a bare stream or a self describing frame is the
		// codec's to say, not the generator's: DEFLATE also ships wrapped as
		// zlib and gzip, so a consumer has to be told there is no wrapper,
		// while Zstandard has no bare variant to rule out.
		m.CodecWire = compress.Wire(p.codec)
		m.Level = p.level
		m.RawLen = len(plain)
		m.CodecSumHex = sha256Hex(compressed)
	}

	if p.cipher == "" {
		return payload, m, nil
	}

	iv, err := crypt.NewIV()
	if err != nil {
		return nil, m, err
	}
	ciphertext, err := crypt.Encrypt(payload, p.key, iv, p.cipher)
	if err != nil {
		return nil, m, err
	}

	m.Cipher = p.cipher
	m.IVHex = hex.EncodeToString(iv)
	m.KeyHex = hex.EncodeToString(p.key)
	m.IVInArray = p.ivMode == 1

	// The array digest covers the bytes the array literally holds, the IV
	// included when it leads the array. Hashing the whole thing rather than
	// the ciphertext alone is deliberate: it is what lets a consumer check the
	// embedded data without owning the key, and it saves them from having to
	// slice the IV off first, which is its own chance to get it wrong.
	arr := ciphertext
	if m.IVInArray {
		arr = make([]byte, 0, len(iv)+len(ciphertext))
		arr = append(arr, iv...)
		arr = append(arr, ciphertext...)
	}
	m.ArraySumHex = sha256Hex(arr)
	return arr, m, nil
}

func writeSource(path string, gen emit.Generator, data []byte, m emit.Meta) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("creating %s: %w", path, err)
	}
	if err := gen.Generate(f, data, m); err != nil {
		f.Close()
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

// isDirTarget reports whether -o points at a directory rather than a file,
// either because it exists as one or because it is spelled with a trailing
// separator.
func isDirTarget(p string) bool {
	if strings.HasSuffix(p, "/") || strings.HasSuffix(p, `\`) {
		return true
	}
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}

func hasNonASCII(s string) bool {
	for _, r := range s {
		if r > 127 {
			return true
		}
	}
	return false
}

func isIdent(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r == '_',
			r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z':
		case i > 0 && r >= '0' && r <= '9':
		default:
			return false
		}
	}
	return true
}

// Package emit renders a byte slice as a source file for one target language.
//
// Adding a language means adding a file that implements Generator and
// registering it from an init function. Nothing else in the tool needs to
// change.
package emit

import "io"

// Language keys. These are also the command line flag names.
const (
	LangC      = "c"
	LangCpp    = "cpp"
	LangGo     = "go"
	LangRust   = "rust"
	LangPython = "python"
)

// Meta carries everything a generator needs besides the raw bytes.
//
// The fields are filled in the order the pipeline runs, so a generator can
// read the transformations off the struct without being told the flags: Codec
// means compression happened, Cipher means encryption happened, and each one
// brings its own digest constant with it.
type Meta struct {
	// Name is the sanitized lower_snake identifier base, for example
	// "logo_png". Each generator applies its own casing convention on top.
	Name string

	// Codec is the compression codec that ran before encryption, and is
	// empty when the payload reaches the array as it was read. It is the
	// public name of the codec, deflate or zstd, and it is written into the
	// generated comment and into the stage digest's identifier.
	Codec string

	// CodecWire is how Codec's output is shaped, "raw" or "frame", and is
	// only set when Codec is. It belongs to the codec rather than to the
	// generator because only some formats have a choice to report: DEFLATE
	// also ships as zlib and gzip, so a consumer has to be told there is no
	// wrapper here, while a Zstandard frame is the only shape Zstandard
	// takes. Leaving it to the generator to print one word for every codec
	// would have it claiming "raw" for a format that has no bare variant.
	CodecWire string

	// Level is the level Codec was run at. It is only meaningful when Codec
	// is set, and it is recorded so a consumer can reproduce the encoding.
	Level int

	// RawLen is the size of the input file in bytes, before compression. It
	// is only set when Codec is set: raw DEFLATE carries no length of its
	// own, unlike gzip's ISIZE, and a consumer that has to size a buffer
	// before inflating has nothing else to go on. Without compression the
	// original size is already expressed by the array.
	RawLen int

	// Cipher is empty when the payload is not encrypted, otherwise it is
	// crypt.ModeCTR or crypt.ModeCBC.
	Cipher string

	// IVHex and KeyHex are lowercase hex and are only set when Cipher is not
	// empty. They are written into the generated source comment.
	IVHex  string
	KeyHex string

	// IVInArray reports whether the first 16 bytes of the payload are the IV.
	// When false the IV appears only in the comment.
	IVInArray bool

	// Sha256Hex is the lowercase hex SHA-256 of the input file as it was read
	// from disk, before compression and before encryption. It is always set,
	// so the generated source can be checked against the original file after
	// the array has been written back out.
	Sha256Hex string

	// CodecSumHex is the lowercase hex SHA-256 of the payload as it left the
	// codec, before encryption. It is only set when Codec is set: the
	// intermediate exists only then.
	CodecSumHex string

	// ArraySumHex is the lowercase hex SHA-256 of the bytes the array holds,
	// IV included when IVInArray is set. It is only set when the payload is
	// encrypted, because that is when the array stops being something the
	// other digests already describe: without a cipher, a compressed array is
	// covered by CodecSumHex and a plain one by Sha256Hex.
	//
	// It is the one digest a consumer can check without owning the key.
	ArraySumHex string

	// Pkg is the Go package name or the Rust module name. Empty means the
	// language default.
	Pkg string

	// Base64 asks for the payload to be written as base64 text instead of a
	// byte array or a hex string. The bytes are untouched: only how the array
	// is spelled changes, which is why the encoding is applied last, after
	// compression and after encryption, and why every length and digest keeps
	// the meaning it had. The source file shrinks to roughly a quarter for the
	// languages that would have written 0xNN elements, at the price of one
	// decode in the consumer.
	Base64 bool
}

// Generator renders data as a source file for one target language.
type Generator interface {
	// Ext returns the file extension without the leading dot.
	Ext() string

	// Generate writes the complete source file to w. It never buffers the
	// whole output in memory, so it is safe for large payloads.
	Generate(w io.Writer, data []byte, m Meta) error
}

var registry = map[string]Generator{}

// Register makes a generator available under a language key. It is meant to
// be called from init functions.
func Register(lang string, g Generator) {
	registry[lang] = g
}

// Get looks up a generator by its language key.
func Get(lang string) (Generator, bool) {
	g, ok := registry[lang]
	return g, ok
}

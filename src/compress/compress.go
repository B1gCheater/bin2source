// Package compress implements the codecs bin2source can apply to the bytes it
// embeds, before any encryption.
//
// Compression always runs before encryption and never after. A cipher turns
// its input into something statistically indistinguishable from noise, so a
// compressor fed ciphertext spends time and returns a slightly larger result
// than it was given. The generated source files therefore carry the codec
// output, and never the input as it was read.
package compress

import (
	"bytes"
	"compress/flate"
	"fmt"
	"strings"

	"github.com/klauspost/compress/zstd"
)

// Codec names. These strings are written verbatim into the generated source
// comment and into the identifier of the stage digest, so they are the public
// name of each codec and not just an internal key.
const (
	CodecDeflate = "deflate"
	CodecZstd    = "zstd"
)

// Spec describes one codec: the name it answers to and the levels it accepts.
type Spec struct {
	// Name is the value -compress takes.
	Name string

	// Wire is how the codec's output is described in the generated comment,
	// and it is one codec's business rather than the generator's, because not
	// every format has a choice to describe.
	//
	// "raw" marks a bare stream that a wrapper also exists for: DEFLATE also
	// comes as zlib and gzip, and a consumer who guesses wrong gets a decode
	// error, so the comment has to say which one this is.
	// "frame" marks a format with exactly one on the wire shape, where there
	// is nothing to disambiguate and the word is there to say so.
	Wire string

	// MinLevel and MaxLevel bound the value -level accepts for this codec.
	MinLevel, MaxLevel int

	// DefaultLevel is used when -compress is given without -level.
	DefaultLevel int
}

// Wire format names for Spec.Wire.
const (
	// WireRaw is a bare stream, with a wrapper format it could be mistaken
	// for.
	WireRaw = "raw"

	// WireFrame is a self describing container that has no bare variant.
	WireFrame = "frame"
)

// registry lists every codec the tool offers, in the order the usage text and
// the error messages should present them. Adding a codec means adding a Spec
// here, a case in Apply, and nothing else.
var registry = []Spec{
	{Name: CodecDeflate, Wire: WireRaw, MinLevel: 1, MaxLevel: 9, DefaultLevel: 9},
	{Name: CodecZstd, Wire: WireFrame, MinLevel: 1, MaxLevel: 22, DefaultLevel: 19},
}

// Supported returns the codec names -compress accepts.
func Supported() []string {
	names := make([]string, len(registry))
	for i, s := range registry {
		names[i] = s.Name
	}
	return names
}

// Get looks up a codec by the name given on the command line.
func Get(name string) (Spec, bool) {
	for _, s := range registry {
		if s.Name == name {
			return s, true
		}
	}
	return Spec{}, false
}

// Wire returns the word the generated comment should use to describe a
// codec's output, "raw" or "frame", and an empty string for an unknown codec.
//
// It exists so the caller can fill in that one word without repeating Get's
// ok handling for a lookup it has already validated.
func Wire(name string) string {
	if spec, ok := Get(name); ok {
		return spec.Wire
	}
	return ""
}

// Apply compresses plain with the named codec at level.
//
// It validates both arguments, so the command line layer does not have to
// repeat the codec's rules before calling in.
func Apply(plain []byte, name string, level int) ([]byte, error) {
	spec, ok := Get(name)
	if !ok {
		return nil, fmt.Errorf("unknown codec %q (supported: %s)",
			name, strings.Join(Supported(), ", "))
	}
	if level < spec.MinLevel || level > spec.MaxLevel {
		return nil, fmt.Errorf("codec %s takes level %d..%d, got %d",
			name, spec.MinLevel, spec.MaxLevel, level)
	}

	switch name {
	case CodecDeflate:
		return deflate(plain, level)
	case CodecZstd:
		return zstdEncode(plain, level)
	}
	// Get and this switch are kept in step by a test that walks Supported, so
	// reaching here means a codec was registered without an implementation.
	return nil, fmt.Errorf("codec %q is registered but not implemented", name)
}

// deflate compresses plain into a raw DEFLATE stream, RFC 1951.
//
// The raw stream, not the zlib or gzip wrapper, because this output is going
// into a source file rather than onto a network:
//
//   - it has no header and no trailer, so nothing is spent on framing;
//   - gzip would add a CRC32 that only repeats the SHA-256 the generator
//     already records, plus mtime and file name fields that make the same
//     input produce different bytes on different runs;
//   - every target language can read it, through inflateInit2(-15) in zlib,
//     flate.NewReader in Go, flate2 in Rust and zlib.decompressobj(-15) in
//     Python.
//
// The trade is that raw DEFLATE carries no uncompressed length, unlike gzip's
// ISIZE, which is why the generator also records raw_len.
func deflate(plain []byte, level int) ([]byte, error) {
	// Half the input is a reasonable guess at the result for the payloads
	// this tool sees, and it saves the buffer a chain of doublings on
	// multi-megabyte input. Being wrong only costs one reallocation.
	var buf bytes.Buffer
	buf.Grow(len(plain) / 2)

	w, err := flate.NewWriter(&buf, level)
	if err != nil {
		return nil, fmt.Errorf("deflate level %d: %w", level, err)
	}
	if _, err := w.Write(plain); err != nil {
		return nil, fmt.Errorf("compressing: %w", err)
	}
	// Close and not Flush: the final block header and the end of stream
	// marker are only written on Close, and without them the output cannot be
	// decoded at all.
	if err := w.Close(); err != nil {
		return nil, fmt.Errorf("compressing: %w", err)
	}
	return buf.Bytes(), nil
}

// zstdEncode compresses plain into a Zstandard frame, RFC 8478.
//
// Unlike deflate there is no raw variant to choose here: Zstandard has one
// on the wire format, the frame, and everything that can read zstd reads
// that. The frame costs 9 bytes on empty input versus deflate's 5: a magic
// number, a header that declares the content size, and a block header.
//
// The one-shot EncodeAll API is used rather than a Writer, and
// WithSingleSegment is forced on, because those two together are what make
// every frame declare the size of its content. That field is optional in the
// format, but not in practice: the reference encoder records the size
// whenever it knows it, and one-shot decoders lean on that. python-zstandard
// is the sharpest example, its decompress() refuses a frame without the
// field outright ("could not determine content size in frame header"), and
// it is the API the generated files' documentation tells consumers to use.
// The Writer path only declares the size for inputs within its window and
// above one kilobyte, so an empty asset and a multi megabyte one would both
// silently need the streaming API instead. The declared window becomes the
// content size, which for an asset decoded in one piece is the memory a
// decoder needs either way.
//
// The level is passed through as the Zstandard level it names, 1..22. Two
// things are worth knowing about how those levels behave, both measured
// rather than assumed:
//
//   - The reference encoder keeps raising its search effort and its window
//     across 1..22, and library implementations are free to map runs of
//     those levels onto one internal setting. klauspost/compress does that
//     above 15: on a 2 MB executable, levels 15, 19, 20, 21 and 22 all
//     produce byte identical output. The range is still exposed as 1..22 so
//     that -level means what the zstd documentation says it means, and so a
//     future version of the library that separates those levels needs no
//     change here.
//   - A higher level searches harder and usually returns fewer bytes. It
//     changes nothing about what a decoder has to hold: the frame names its
//     content size and decoders size themselves from it.
func zstdEncode(plain []byte, level int) ([]byte, error) {
	w, err := zstd.NewWriter(nil,
		zstd.WithEncoderLevel(zstd.EncoderLevelFromZstd(level)),
		zstd.WithSingleSegment(true))
	if err != nil {
		return nil, fmt.Errorf("zstd level %d: %w", level, err)
	}
	defer w.Close()

	// EncodeAll writes the whole frame, header to checksum, in one call; the
	// Writer interface would stream the same bytes but without the content
	// size for the sizes above. An empty input still produces a complete
	// frame, so the array never comes back empty-handed.
	return w.EncodeAll(plain, nil), nil
}

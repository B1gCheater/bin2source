package compress

import (
	"bytes"
	"compress/flate"
	"compress/zlib"
	"fmt"
	"io"
	"sort"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
)

// inflate decodes a raw DEFLATE stream. It is the reference decoder for these
// tests: every payload the package produces has to survive it.
func inflate(t *testing.T, data []byte) []byte {
	t.Helper()
	out, err := io.ReadAll(flate.NewReader(bytes.NewReader(data)))
	if err != nil {
		t.Fatalf("flate.NewReader could not decode the output: %v", err)
	}
	return out
}

// zstdDecode decodes a Zstandard frame. Decoding with a second implementation
// is what makes the output trustworthy: a codec agreeing with itself proves
// nothing about whether anyone else can read the bytes.
func zstdDecode(t *testing.T, data []byte) []byte {
	t.Helper()
	d, err := zstd.NewReader(nil)
	if err != nil {
		t.Fatalf("zstd.NewReader: %v", err)
	}
	defer d.Close()
	out, err := d.DecodeAll(data, nil)
	if err != nil {
		t.Fatalf("zstd could not decode the output: %v", err)
	}
	return out
}

// decode runs the reference decoder belonging to the named codec.
//
// Tests that walk every registered codec go through this rather than calling
// one decoder directly, so a codec cannot be added to the registry without a
// decoder to check its output against.
func decode(t *testing.T, codec string, data []byte) []byte {
	t.Helper()
	switch codec {
	case CodecDeflate:
		return inflate(t, data)
	case CodecZstd:
		return zstdDecode(t, data)
	}
	t.Fatalf("no reference decoder is registered for codec %q", codec)
	return nil
}

// sampleLevels picks the levels worth exercising for a codec: both ends of
// its range, its default, and a midpoint.
func sampleLevels(spec Spec) []int {
	var out []int
	seen := map[int]bool{}
	add := func(n int) {
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	add(spec.MinLevel)
	add(spec.MaxLevel)
	add(spec.DefaultLevel)
	add((spec.MinLevel + spec.MaxLevel) / 2)
	sort.Ints(out)
	return out
}

func TestRoundTrip(t *testing.T) {
	inputs := map[string][]byte{
		"empty":        {},
		"one byte":     {'A'},
		"short text":   []byte("hello hello hello"),
		"very long":    bytes.Repeat([]byte("abcdefgh"), 5000),
		"all one byte": bytes.Repeat([]byte{0}, 10000),
		"binary": func() []byte {
			b := make([]byte, 4096)
			for i := range b {
				b[i] = byte(i)
			}
			return b
		}(),
	}

	for name, in := range inputs {
		for _, spec := range registry {
			for _, level := range sampleLevels(spec) {
				t.Run(fmt.Sprintf("%s/%s/%d", name, spec.Name, level), func(t *testing.T) {
					out, err := Apply(in, spec.Name, level)
					if err != nil {
						t.Fatal(err)
					}
					if got := decode(t, spec.Name, out); !bytes.Equal(got, in) {
						t.Errorf("round trip changed the data: %d bytes in, %d out",
							len(in), len(got))
					}
				})
			}
		}
	}
}

// The output has to be a raw DEFLATE stream, not a zlib one. Both decoders are
// standard, they differ only in the two byte header, and picking the wrong one
// is the kind of defect that only shows up in someone else's program.
func TestOutputIsRawDeflateNotZlib(t *testing.T) {
	out, err := Apply([]byte("hello hello hello"), CodecDeflate, 6)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := zlib.NewReader(bytes.NewReader(out)); err == nil {
		t.Error("the output decodes as zlib, so it carries a zlib wrapper")
	}
	if got := inflate(t, out); string(got) != "hello hello hello" {
		t.Errorf("raw deflate gave %q", got)
	}
}

// Zstandard has no raw variant to choose between: the frame is the format.
// The frame opens with a four byte magic, and every decoder in every language
// looks for it first, so its presence is what makes the array readable by
// libzstd, python-zstandard and the rest.
func TestZstdOutputIsAFrame(t *testing.T) {
	out, err := Apply([]byte("hello hello hello"), CodecZstd, 9)
	if err != nil {
		t.Fatal(err)
	}
	if want := []byte{0x28, 0xb5, 0x2f, 0xfd}; !bytes.HasPrefix(out, want) {
		t.Errorf("output starts with % x, want the zstd magic % x", out[:4], want)
	}
	if got := zstdDecode(t, out); string(got) != "hello hello hello" {
		t.Errorf("zstd frame gave %q", got)
	}
}

// Every frame has to declare the size of its content.
//
// A frame without that field is legal Zstandard and the Go decoder reads it
// happily, but one-shot decoders do not all accept it: python-zstandard's
// decompress() refuses anything without the field ("could not determine
// content size in frame header"), and that is the recipe the generated
// files' documentation hands to consumers. The reference encoder records the
// size whenever it knows it, and this tool always knows it.
//
// The sizes below include the ranges the library's writer path would omit
// the field for: the empty input, anything under 256 bytes, and anything
// over one block (128 KB) -- and the last one is where the megabyte scale
// assets this codec exists for live.
func TestZstdFrameDeclaresItsContentSize(t *testing.T) {
	spec, ok := Get(CodecZstd)
	if !ok {
		t.Fatal("zstd is not registered")
	}

	for _, n := range []int{0, 1, 5, 255, 256, 1024, 4096, 131072, 200000, 1048577} {
		in := make([]byte, n)
		for i := range in {
			in[i] = byte(i*7 + i/13)
		}
		out, err := Apply(in, CodecZstd, spec.DefaultLevel)
		if err != nil {
			t.Fatalf("n=%d: %v", n, err)
		}

		var h zstd.Header
		if err := h.Decode(out); err != nil {
			t.Fatalf("n=%d: the output does not open with a frame header: %v", n, err)
		}
		if !h.HasFCS {
			t.Errorf("n=%d: the frame does not declare its content size", n)
		}
		if h.FrameContentSize != uint64(n) {
			t.Errorf("n=%d: the frame declares %d bytes", n, h.FrameContentSize)
		}
	}
}

// Deflate: on this toolchain even nothing at all costs two bytes, a final
// empty block in fixed Huffman form. An encoder that chooses a stored block
// spends five instead, and the choice is the encoder's to make, so there is
// no implementation independent constant to pin here; the test checks the
// contract instead: it decodes as a raw stream, it does not decode as zlib,
// and the overhead stays in single digits.
//
// This is also why the generator does not refuse to compress a payload that
// did not shrink. Small inputs legitimately grow, and the caller is told on
// stderr rather than silently overruled.
func TestEmptyInputCostsAFewBytes(t *testing.T) {
	out, err := Apply(nil, CodecDeflate, 9)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) > 8 {
		t.Errorf("empty input cost %d bytes, more than the format needs", len(out))
	}
	if _, err := zlib.NewReader(bytes.NewReader(out)); err == nil {
		t.Error("the empty input's output is zlib wrapped")
	}
	if got := inflate(t, out); len(got) != 0 {
		t.Errorf("empty input inflated to %d bytes", len(got))
	}
}

// The two codecs do not cost the same to say nothing with, and that is worth
// pinning: a frame carries a magic number, a header that declares the content
// size and a block header, which deflate's bare block header does not. Nine
// bytes is the floor anyone embedding an empty asset in zstd pays, about
// thirteen for small non-empty ones.
func TestZstdFrameOverheadIsLargerThanDeflate(t *testing.T) {
	deflated, err := Apply(nil, CodecDeflate, 9)
	if err != nil {
		t.Fatal(err)
	}
	zstded, err := Apply(nil, CodecZstd, 19)
	if err != nil {
		t.Fatal(err)
	}
	if len(zstded) <= len(deflated) {
		t.Errorf("empty input costs %d bytes in zstd but %d in deflate; a frame should cost more",
			len(zstded), len(deflated))
	}
	if len(zstded) > 32 {
		t.Errorf("empty input costs %d bytes in zstd, more than a frame needs", len(zstded))
	}
}

// Compression is not a promise of a smaller result, and the package must not
// pretend otherwise. Random bytes come out a few bytes longer, which is the
// behaviour the command line layer warns about.
func TestIncompressibleInputGrowsSlightly(t *testing.T) {
	// A fixed, xorshift generated sequence: deterministic across runs, and
	// with no structure for the codec to find.
	b := make([]byte, 4096)
	x := uint64(0x9e3779b97f4a7c15)
	for i := range b {
		x ^= x << 13
		x ^= x >> 7
		x ^= x << 17
		b[i] = byte(x)
	}

	for _, spec := range registry {
		t.Run(spec.Name, func(t *testing.T) {
			out, err := Apply(b, spec.Name, spec.MaxLevel)
			if err != nil {
				t.Fatal(err)
			}
			if len(out) <= len(b) {
				t.Logf("note: this input did compress, %d -> %d", len(b), len(out))
			}
			if len(out) > len(b)+64 {
				t.Errorf("incompressible input grew from %d to %d bytes", len(b), len(out))
			}
			if !bytes.Equal(decode(t, spec.Name, out), b) {
				t.Error("the decoded bytes differ from the input")
			}
		})
	}
}

// What a level buys: more effort looking for matches, so usually fewer bytes.
//
// "Usually" is doing real work in that sentence. Neither format promises
// monotonic shrinkage, and Go's DEFLATE does not always deliver it: on an
// 8,800 byte input, Go 1.27 emits 84 bytes at levels 1 through 3 and 85 at
// levels 4 through 9, because the levels switch matching strategies rather
// than turning one dial. The same test passed on Go 1.25, which drew the
// line elsewhere. A test asserting monotonic shrinkage would therefore be
// pinning one release's heuristics and reporting a phantom bug on the next.
//
// Two claims do hold, and they are worth testing:
//
//   - The level reaches the codec at all. A bug that dropped it and always
//     ran one setting would still round trip, so nothing else catches it.
//   - No level blows the result up. A level that cost many times the CPU to
//     produce substantially more bytes would point at the wrong number being
//     passed down, which is the part this package owns.
func TestLevelsArePassedThroughAndStayBounded(t *testing.T) {
	in := bytes.Repeat([]byte("the quick brown fox jumps over the lazy dog\n"), 200)

	for _, spec := range registry {
		t.Run(spec.Name, func(t *testing.T) {
			sizes := make(map[int]int)
			cheapest := -1
			for level := spec.MinLevel; level <= spec.MaxLevel; level++ {
				out, err := Apply(in, spec.Name, level)
				if err != nil {
					t.Fatal(err)
				}
				sizes[level] = len(out)
				if level == spec.MinLevel {
					cheapest = len(out)
				}
			}

			distinct := make(map[int]bool)
			for _, n := range sizes {
				distinct[n] = true
			}
			if len(distinct) < 2 {
				t.Errorf("every level %d..%d gave %d bytes: the level is not reaching the codec",
					spec.MinLevel, spec.MaxLevel, cheapest)
			}

			// Deliberately loose. spewing bytes is the failure this is here
			// to catch, not losing a handful to a different heuristic.
			limit := cheapest + cheapest/4 + 16
			for level := spec.MinLevel; level <= spec.MaxLevel; level++ {
				if n := sizes[level]; n > limit {
					t.Errorf("level %d gave %d bytes where level %d needed %d",
						level, n, spec.MinLevel, cheapest)
				}
			}
		})
	}
}

// Unlike encryption, which salts every run with a fresh IV, compression has no
// random input: the same request has to produce the same bytes.
func TestApplyIsDeterministic(t *testing.T) {
	in := bytes.Repeat([]byte("repeat me"), 500)

	for _, spec := range registry {
		t.Run(spec.Name, func(t *testing.T) {
			first, err := Apply(in, spec.Name, spec.DefaultLevel)
			if err != nil {
				t.Fatal(err)
			}
			second, err := Apply(in, spec.Name, spec.DefaultLevel)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(first, second) {
				t.Error("two identical calls produced different output")
			}
		})
	}
}

// A codec name is either registered or refused. The names here are all
// plausible things a user might type, including xz and brotli which are the
// formats most often asked for after these two.
func TestUnknownCodecIsRefused(t *testing.T) {
	for _, name := range []string{"xz", "lzma", "brotli", "gzip", "deflate ", "", "DEFLATE", "Zstd"} {
		t.Run(name, func(t *testing.T) {
			if _, err := Apply([]byte("x"), name, 9); err == nil {
				t.Fatalf("codec %q was accepted", name)
			}
		})
	}
}

// Each codec owns its own level range, and 9 is deflate's ceiling rather than
// a shared maximum: zstd goes to 22 and has to be allowed to.
func TestLevelRangeIsEnforced(t *testing.T) {
	cases := []struct {
		codec string
		bad   []int
		good  []int
	}{
		{CodecDeflate, []int{-2, -1, 0, 10, 100}, []int{1, 5, 9}},
		{CodecZstd, []int{-2, -1, 0, 23, 100}, []int{1, 19, 22}},
	}
	for _, tc := range cases {
		t.Run(tc.codec, func(t *testing.T) {
			for _, level := range tc.bad {
				if _, err := Apply([]byte("x"), tc.codec, level); err == nil {
					t.Errorf("level %d was accepted", level)
				}
			}
			for _, level := range tc.good {
				if _, err := Apply([]byte("x"), tc.codec, level); err != nil {
					t.Errorf("level %d was refused: %v", level, err)
				}
			}
		})
	}
}

// Every registered codec must answer to its own name, at its own default
// level, through Apply. Without this, adding a Spec without a matching case in
// Apply would only fail the first time a user asked for that codec.
func TestEveryRegisteredCodecIsImplemented(t *testing.T) {
	if len(Supported()) == 0 {
		t.Fatal("no codec is registered")
	}
	for _, name := range Supported() {
		t.Run(name, func(t *testing.T) {
			spec, ok := Get(name)
			if !ok {
				t.Fatalf("Supported lists %q but Get does not find it", name)
			}
			if spec.Name != name {
				t.Errorf("Spec.Name is %q, want %q", spec.Name, name)
			}
			if spec.MinLevel > spec.DefaultLevel || spec.DefaultLevel > spec.MaxLevel {
				t.Errorf("default level %d is outside %d..%d",
					spec.DefaultLevel, spec.MinLevel, spec.MaxLevel)
			}
			out, err := Apply([]byte("hello hello"), name, spec.DefaultLevel)
			if err != nil {
				t.Fatalf("Apply: %v", err)
			}
			if got := decode(t, name, out); string(got) != "hello hello" {
				t.Errorf("round trip gave %q", got)
			}
		})
	}
}

func TestGetIsExact(t *testing.T) {
	if _, ok := Get("Deflate"); ok {
		t.Error("codec lookup ignored case, so -compress Deflate would work and confuse the comment")
	}
	for _, name := range []string{CodecDeflate, CodecZstd} {
		if _, ok := Get(name); !ok {
			t.Errorf("%q is not registered", name)
		}
		if !strings.Contains(strings.Join(Supported(), ","), name) {
			t.Errorf("Supported does not mention %q", name)
		}
	}
}

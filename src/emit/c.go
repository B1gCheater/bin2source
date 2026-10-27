package emit

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

func init() { Register(LangC, cGenerator{}) }

// cGenerator emits a self contained C header holding the array.
//
// The array is static, which gives it internal linkage: including this header
// from several translation units compiles cleanly and yields one private copy
// per unit. That trades a little binary size for not having to add a matching
// .c file to the build.
type cGenerator struct{}

func (cGenerator) Ext() string { return "h" }

func (cGenerator) Generate(w io.Writer, data []byte, m Meta) error {
	name := LowerSnake(m.Name)
	guard := strings.ToUpper(name) + "_H"

	bw := bufio.NewWriterSize(w, 1<<16)
	writeBlockComment(bw, metaLines(m, func(suffix string) string {
		return joinedIdent(name, "_", suffix, func(s string) string { return s })
	}))
	fmt.Fprintf(bw, "#ifndef %s\n#define %s\n\n", guard, guard)
	if m.Base64 {
		// Text instead of the byte array: about a quarter of the characters, at
		// the price of one decode in the consumer. The length below is still
		// the size of what the text decodes to, not the size of the text.
		fmt.Fprintf(bw, "static const char %s_b64[] = (\n", name)
		writeBase64Chunks(bw, data, "\t", "\t", "")
		fmt.Fprintf(bw, ");\n\n")
	} else {
		fmt.Fprintf(bw, "static const unsigned char %s[] = {\n", name)
		writeHexArray(bw, cArrayBody(data), "\t")
		fmt.Fprintf(bw, "};\n\n")
	}

	// A literal size, not sizeof: the requested behaviour for C and C++, and
	// with base64 it is the only place the decoded size appears.
	fmt.Fprintf(bw, "static const unsigned int %s = %d;\n\n", name+"_len", len(data))

	if m.Codec != "" {
		// Raw DEFLATE carries no length of its own, so this is the only place
		// a consumer can learn how much room the inflated data needs. Without
		// it a C caller has to grow a buffer on failure and retry.
		writeIntConst(bw, "static const unsigned int "+name+"_raw_len", m.RawLen, "", ";")
	}

	// One digest per stage of the pipeline, so a consumer that fails to
	// recover the original bytes can tell which step went wrong.
	writeStageDigests(bw, m, func(suffix string) string {
		return "static const char " + name + "_" + suffix
	}, "[]", "", "\t", " ", ";", true)

	fmt.Fprintf(bw, "#endif /* %s */\n", guard)
	return bw.Flush()
}

// cArrayBody works around empty input. C rejected an empty initialiser until
// C23, so a lone zero byte is emitted instead. It is never read: the length
// constant is still zero, and every consumer iterates 0 bytes.
func cArrayBody(data []byte) []byte {
	if len(data) == 0 {
		return []byte{0}
	}
	return data
}

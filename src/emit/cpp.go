package emit

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

func init() { Register(LangCpp, cppGenerator{}) }

// cppGenerator emits a self contained C++ header holding the array.
//
// The shape matches the C generator on purpose, so a .h and a .hpp for the
// same payload read the same way. The only differences are the file
// extension, the include guard suffix and the use of constexpr for the
// length.
type cppGenerator struct{}

func (cppGenerator) Ext() string { return "hpp" }

func (cppGenerator) Generate(w io.Writer, data []byte, m Meta) error {
	name := LowerSnake(m.Name)
	guard := strings.ToUpper(name) + "_HPP"

	bw := bufio.NewWriterSize(w, 1<<16)
	writeBlockComment(bw, metaLines(m, func(suffix string) string {
		return joinedIdent(name, "_", suffix, func(s string) string { return s })
	}))
	fmt.Fprintf(bw, "#ifndef %s\n#define %s\n\n", guard, guard)
	if m.Base64 {
		// As in the C output: base64 text four characters per three bytes
		// against the array's six per one, and the length constant below is
		// the decoded size.
		fmt.Fprintf(bw, "static constexpr const char %s_b64[] = (\n", name)
		writeBase64Chunks(bw, data, "\t", "\t", "")
		fmt.Fprintf(bw, ");\n\n")
	} else {
		fmt.Fprintf(bw, "static const unsigned char %s[] = {\n", name)
		writeHexArray(bw, cArrayBody(data), "\t")
		fmt.Fprintf(bw, "};\n\n")
	}

	// static constexpr keeps internal linkage, so the header stays safe to
	// include from many translation units. The value is a literal, not
	// sizeof, to match the C output.
	fmt.Fprintf(bw, "static constexpr unsigned int %s = %d;\n\n", name+"_len", len(data))

	if m.Codec != "" {
		// As in the C output: raw DEFLATE does not record the size the
		// inflated data will need.
		writeIntConst(bw, "static constexpr unsigned int "+name+"_raw_len", m.RawLen, "", ";")
	}

	// One digest per stage of the pipeline, as in the C output.
	writeStageDigests(bw, m, func(suffix string) string {
		return "static constexpr const char " + name + "_" + suffix
	}, "[]", "", "\t", " ", ";", true)

	fmt.Fprintf(bw, "#endif /* %s */\n", guard)
	return bw.Flush()
}

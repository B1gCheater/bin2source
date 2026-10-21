package emit

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

func init() { Register(LangPython, pythonGenerator{}) }

// pythonGenerator emits a Python module holding the payload.
//
// Hex rather than a bytes([...]) literal: a ten megabyte resource would be
// ten million list elements rebuilt on every single import, while
// bytes.fromhex parses the same data in one C level pass. Adjacent string
// literals are concatenated by the parser, so the value still wraps nicely.
type pythonGenerator struct{}

func (pythonGenerator) Ext() string { return "py" }

func (pythonGenerator) Generate(w io.Writer, data []byte, m Meta) error {
	name := UpperSnake(m.Name)

	bw := bufio.NewWriterSize(w, 1<<16)
	writeHashComment(bw, metaLines(m, func(suffix string) string {
		return joinedIdent(name, "_", suffix, strings.ToUpper)
	}))
	fmt.Fprint(bw, "\n")

	if m.Base64 {
		// The same shape as the hex string below, with a denser encoding:
		// four characters per three bytes instead of two per one. The length
		// is written down because there is no bytes object to ask any more.
		fmt.Fprintf(bw, "%s_B64 = (\n", name)
		writeBase64Chunks(bw, data, "    ", "    ", "")
		fmt.Fprint(bw, ")\n\n")
		fmt.Fprintf(bw, "%s_LEN = %d\n\n", name, len(data))
	} else {
		fmt.Fprintf(bw, "%s = bytes.fromhex(\n", name)
		writeHexString(bw, data, "    ")
		fmt.Fprint(bw, ")\n\n")
		fmt.Fprintf(bw, "%s_LEN = len(%s)\n\n", name, name)
	}

	if m.Codec != "" {
		// Raw DEFLATE does not record the size the inflated data will need.
		writeIntConst(bw, name+"_RAW_LEN", m.RawLen, "", "")
	}

	// One digest per stage of the pipeline, each a 64 character lowercase hex
	// string, so it compares directly with hashlib output. Four spaces, not a
	// tab: the array above is indented with spaces too.
	writeStageDigests(bw, m, func(suffix string) string {
		return name + "_" + strings.ToUpper(suffix)
	}, "", "", "    ", " ", "", true)
	return bw.Flush()
}

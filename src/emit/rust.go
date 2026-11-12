package emit

import (
	"bufio"
	"encoding/base64"
	"fmt"
	"io"
	"strings"
)

func init() { Register(LangRust, rustGenerator{}) }

// rustGenerator emits a Rust source file holding the payload.
//
// Rust puts the length in the array's type, so unlike every other target the
// element count appears twice: once as [u8; N] and once through .len(). The
// type is unavoidable, and .len() is the const-folded form for the length
// constant.
type rustGenerator struct{}

func (rustGenerator) Ext() string { return "rs" }

func (rustGenerator) Generate(w io.Writer, data []byte, m Meta) error {
	name := UpperSnake(m.Name)

	indent := ""
	bw := bufio.NewWriterSize(w, 1<<16)

	if m.Pkg != "" {
		fmt.Fprintf(bw, "pub mod %s {\n", m.Pkg)
		indent = "\t"
	}

	writeSlashComment(bw, metaLines(m, func(suffix string) string {
		return joinedIdent(name, "_", suffix, strings.ToUpper)
	}), indent)
	if m.Cipher != "" || m.Base64 {
		fmt.Fprint(bw, "\n")
	}

	if m.Base64 {
		// One literal, continued with a backslash: Rust does not concatenate
		// adjacent literals, and concat! would want one argument per chunk.
		// The decoded size is written down because there is no array left to
		// measure.
		fmt.Fprintf(bw, "%spub const %s: &str = ", indent, name+"_B64")
		writeRustBase64(bw, data, indent+"\t")
		fmt.Fprintf(bw, ";\n\n")
		fmt.Fprintf(bw, "%spub const %s: usize = %d;\n\n", indent, name+"_LEN", len(data))
	} else {
		fmt.Fprintf(bw, "%spub const %s: [u8; %d] = [\n", indent, name, len(data))
		writeHexArray(bw, data, indent+"\t")
		fmt.Fprintf(bw, "%s];\n\n", indent)
		fmt.Fprintf(bw, "%spub const %s: usize = %s.len();\n\n", indent, name+"_LEN", name)
	}

	if m.Codec != "" {
		writeIntConst(bw, "pub const "+name+"_RAW_LEN: usize", m.RawLen, indent, ";")
	}

	// Rust insists on a type for a const, so each digest is a single line with
	// the type spelled out. Wrapping one in parentheses would be valid but
	// trips the unused_parens lint.
	writeStageDigests(bw, m, func(suffix string) string {
		return "pub const " + name + "_" + strings.ToUpper(suffix)
	}, ": &str", indent, "", "", ";", false)
	if m.Pkg != "" {
		fmt.Fprint(bw, "}\n")
	}
	return bw.Flush()
}

// writeRustBase64 writes data as base64 inside one string literal, broken
// across lines with the string continuation escape.
//
// Rust has no adjacent literal concatenation and concat! would need one
// argument per chunk at a size where that is thousands of them, so the
// backslash is what keeps this to a single literal: the escape swallows the
// newline and the indentation after it, leaving the string holding nothing
// but the base64 characters.
func writeRustBase64(w *bufio.Writer, data []byte, indent string) {
	text := base64.StdEncoding.EncodeToString(data)
	w.WriteByte('"')
	for i := 0; i < len(text); i += base64CharsPerLine {
		end := min(i+base64CharsPerLine, len(text))
		if i > 0 {
			w.WriteString("\\\n")
			w.WriteString(indent)
		}
		w.WriteString(text[i:end])
	}
	w.WriteByte('"')
}

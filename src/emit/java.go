package emit

import (
	"bufio"
	"encoding/base64"
	"fmt"
	"io"
	"strings"
)

func init() { Register(LangJava, javaGenerator{}) }

// javaGenerator emits a self-contained Java source file holding the payload.
type javaGenerator struct{}

func (javaGenerator) Ext() string { return "java" }

func (javaGenerator) Generate(w io.Writer, data []byte, m Meta) error {
	const maxArrayBytes = 8192
	if !m.Base64 && len(data) > maxArrayBytes {
		return fmt.Errorf("input file is too large (%d bytes) for a Java byte[] array literal (JVM 64KB method limit prevents array literals > %d bytes); use -b64 instead", len(data), maxArrayBytes)
	}

	name := UpperSnake(m.Name)
	className := Pascal(m.Name)

	bw := bufio.NewWriterSize(w, 1<<16)
	writeBlockComment(bw, metaLines(m, func(suffix string) string {
		return joinedIdent(name, "_", suffix, strings.ToUpper)
	}))

	if m.Pkg != "" {
		fmt.Fprintf(bw, "package %s;\n\n", m.Pkg)
	}

	fmt.Fprintf(bw, "public final class %s {\n", className)
	fmt.Fprintf(bw, "\tprivate %s() {}\n\n", className)

	if m.Base64 {
		writeJavaBase64(bw, data, name)
	} else {
		if len(data) == 0 {
			fmt.Fprintf(bw, "\tpublic static final byte[] %s = new byte[0];\n\n", name)
		} else {
			fmt.Fprintf(bw, "\tpublic static final byte[] %s = new byte[] {\n", name)
			writeJavaByteArray(bw, data, "\t\t")
			fmt.Fprintf(bw, "\t};\n\n")
		}
		fmt.Fprintf(bw, "\tpublic static final int %s_LEN = %d;\n\n", name, len(data))
	}

	if m.Codec != "" {
		writeIntConst(bw, "public static final int "+name+"_RAW_LEN", m.RawLen, "\t", ";")
	}

	writeStageDigests(bw, m, func(suffix string) string {
		return "public static final String " + name + "_" + strings.ToUpper(suffix)
	}, "", "\t", "", "", ";", false)

	fmt.Fprint(bw, "}\n")
	return bw.Flush()
}

// writeJavaByteArray writes data as (byte) 0xNN elements, 16 per line.
func writeJavaByteArray(w *bufio.Writer, data []byte, indent string) {
	const perLine = 16
	for i := 0; i < len(data); i += perLine {
		end := min(i+perLine, len(data))
		w.WriteString(indent)
		for j := i; j < end; j++ {
			if j > i {
				w.WriteByte(' ')
			}
			b := data[j]
			w.WriteString("(byte) 0x")
			w.WriteByte(hexDigits[b>>4])
			w.WriteByte(hexDigits[b&0x0f])
			w.WriteByte(',')
		}
		w.WriteByte('\n')
	}
}

// writeJavaBase64 writes the base64 payload.
//
// When the base64 text fits within a single Java string literal constant (<= 65,512 chars,
// roughly 48 KB payload), it is emitted as a single String constant wrapped in parentheses.
// For larger payloads, it is emitted as a String[] array of chunks, each chunk <= 65,512 chars,
// avoiding javac's 65,534 character string constant limit and the 64KB method limit.
func writeJavaBase64(w *bufio.Writer, data []byte, name string) {
	text := base64.StdEncoding.EncodeToString(data)
	if text == "" {
		fmt.Fprintf(w, "\tpublic static final String %s_B64 = \"\";\n\n", name)
		fmt.Fprintf(w, "\tpublic static final int %s_LEN = 0;\n\n", name)
		return
	}

	const maxChunkChars = 862 * base64CharsPerLine

	if len(text) <= maxChunkChars {
		fmt.Fprintf(w, "\tpublic static final String %s_B64 = (\n", name)
		writeBase64Chunks(w, data, "\t\t", "\t\t", " +")
		fmt.Fprintf(w, "\t);\n\n")
	} else {
		fmt.Fprintf(w, "\tpublic static final String[] %s_B64 = new String[] {\n", name)
		for i := 0; i < len(text); i += maxChunkChars {
			end := min(i+maxChunkChars, len(text))
			chunk := text[i:end]
			w.WriteString("\t\t(\n")
			writeJavaB64String(w, chunk, "\t\t\t", " +")
			if end < len(text) {
				w.WriteString("\t\t),\n")
			} else {
				w.WriteString("\t\t)\n")
			}
		}
		fmt.Fprintf(w, "\t};\n\n")
	}
	fmt.Fprintf(w, "\tpublic static final int %s_LEN = %d;\n\n", name, len(data))
}

func writeJavaB64String(w *bufio.Writer, text string, indent, sep string) {
	for i := 0; i < len(text); i += base64CharsPerLine {
		end := min(i+base64CharsPerLine, len(text))
		w.WriteString(indent)
		w.WriteByte('"')
		w.WriteString(text[i:end])
		w.WriteByte('"')
		if end < len(text) {
			w.WriteString(sep)
		}
		w.WriteByte('\n')
	}
}

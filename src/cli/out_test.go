package cli

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/fatih/color"
)

// The message stream is a package variable precisely so a test can point it at
// a buffer. Without that, checking colour would mean checking a terminal.
func capture(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	old := stderr
	stderr = &buf
	defer func() { stderr = old }()
	fn()
	return buf.String()
}

// allColours is every colour the package can write in, so a test can switch
// them on or off together and put them back afterwards.
func allColours() []*color.Color {
	return []*color.Color{colourError, colourWarning, colourSuccess, colourPlain}
}

func setColour(t *testing.T, on bool) {
	t.Helper()
	for _, c := range allColours() {
		if on {
			c.EnableColor()
		} else {
			c.DisableColor()
		}
	}
	t.Cleanup(func() {
		for _, c := range allColours() {
			c.DisableColor()
		}
	})
}

// Each kind of message gets its own colour, which is the whole point: an error
// and a success are both just text until something sorts them for the eye.
func TestEachMessageKindHasItsOwnColour(t *testing.T) {
	setColour(t, true)

	cases := []struct {
		name string
		fn   func()
		// The SGR parameter for the bright variant of each colour.
		want string
	}{
		{"error", func() { printError(errors.New("boom")) }, "\x1b[91m"},
		{"warning", func() { printWarning("careful") }, "\x1b[93m"},
		{"success", func() { printSuccess("wrote it") }, "\x1b[92m"},
		{"usage", printUsage, "\x1b[94m"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := capture(t, tc.fn)
			if !strings.Contains(got, tc.want) {
				t.Errorf("output does not open with %q: %q", tc.want, got)
			}
			// Whatever is opened has to be closed, or the colour leaks into
			// everything printed afterwards.
			if !strings.HasSuffix(got, "\x1b[0m") {
				t.Errorf("output does not reset the colour: %q", got)
			}
		})
	}
}

// With colour off the text has to be byte for byte what it was before colour
// existed. Anything still writing escapes into a pipe would show up as garbage
// in a log file, or in the output of another tool that captured this one.
func TestPlainOutputHasNoEscapeSequences(t *testing.T) {
	setColour(t, false)

	cases := []struct {
		name string
		fn   func()
		want string
	}{
		{"error", func() { printError(errors.New("boom")) }, "bin2source: boom\n"},
		{"warning", func() { printWarning("careful") }, "bin2source: warning: careful\n"},
		{"success", func() { printSuccess("wrote it") }, "bin2source: wrote it\n"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := capture(t, tc.fn); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}

	if got := capture(t, printUsage); got != usage {
		t.Errorf("the help text changed on its way out:\n%q", got)
	}
}

// NO_COLOR is the convention for opting out, and it has to hold even with a
// terminal attached, which is the case the library's own check gets wrong for
// a tool that writes to stderr.
func TestNoColorIsHonoured(t *testing.T) {
	// Unset, so the "not requested" branch is exercised rather than assumed.
	if err := os.Unsetenv("NO_COLOR"); err != nil {
		t.Fatal(err)
	}
	if noColorRequested() {
		t.Error("NO_COLOR is unset but colour was reported as unwanted")
	}

	for _, value := range []string{"1", "true", "0"} {
		t.Setenv("NO_COLOR", value)
		if !noColorRequested() {
			t.Errorf("NO_COLOR=%q was not taken as a request for plain output", value)
		}
	}

	// An empty value is conventionally the same as unset.
	t.Setenv("NO_COLOR", "")
	if noColorRequested() {
		t.Error("NO_COLOR set to empty should mean unset")
	}
}

// Colour must not disturb the words: the tool's name still prefixes every line
// and a warning still says it is one, so output redirected to a file a month
// ago stays readable.
func TestMessagesKeepTheirShapeWithColourOn(t *testing.T) {
	setColour(t, true)

	cases := []struct {
		name string
		fn   func()
		want string
	}{
		{"error", func() { printError(errors.New("boom")) }, "bin2source: boom"},
		{"warning", func() { printWarning("careful") }, "bin2source: warning: careful"},
		{"success", func() { printSuccess("wrote it") }, "bin2source: wrote it"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := capture(t, tc.fn); !strings.Contains(got, tc.want) {
				t.Errorf("message lost its shape: %q", got)
			}
		})
	}
}

// The help text no longer points at a file the reader may not have. The tool
// ships as a single executable more often than as a checkout.
func TestUsageDoesNotReferenceTheDocsDirectory(t *testing.T) {
	setColour(t, false)

	got := capture(t, printUsage)
	for _, unwanted := range []string{"doc/output-format.md", "SHA-256 for each stage"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("the help text mentions %q, which assumes the reader has the repository", unwanted)
		}
	}
	if !strings.Contains(got, "-h, --help") {
		t.Error("the help text lost its Other section")
	}
}

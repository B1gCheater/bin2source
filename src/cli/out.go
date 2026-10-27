package cli

import (
	"io"
	"os"

	"github.com/fatih/color"
	"github.com/mattn/go-colorable"
	"github.com/mattn/go-isatty"
)

// Everything the tool says goes to stderr, so that the generated file can be
// piped to stdout one day without the two mixing. Colour follows it there.
//
// The stream is wrapped with go-colorable rather than used directly because of
// Windows: a console there has to be switched into virtual terminal mode
// before it will do anything with an escape sequence, and go-colorable does
// that, falling back to the console API on terminals old enough to need it.
// On Linux and macOS it hands the stream back untouched.
var stderr io.Writer = colorable.NewColorableStderr()

// One colour per kind of message, so the eye can sort a run into things that
// worked and things that did not without reading a word of it.
var (
	colourError   = color.New(color.FgHiRed)
	colourWarning = color.New(color.FgHiYellow)
	colourSuccess = color.New(color.FgHiGreen)
	colourPlain   = color.New(color.FgHiBlue)
)

func init() {
	// fatih/color decides whether to emit colour from os.Stdout, which is the
	// wrong stream for a tool that only ever writes to stderr. Redirecting
	// the generated file is the common case, "bin2source -f logo.png -c >
	// logo.h": stdout is a file, stderr is still the terminal, and the
	// library's check would switch colour off even though a terminal is
	// looking straight at the output. Judge by the stream being written to.
	//
	// This is also where NO_COLOR is honoured, which is the convention for
	// opting out: set it to anything non-empty and colour stays away, whether
	// or not a terminal is attached.
	on := !noColorRequested() && isatty.IsTerminal(os.Stderr.Fd())

	for _, c := range []*color.Color{colourError, colourWarning, colourSuccess, colourPlain} {
		if on {
			c.EnableColor()
		} else {
			c.DisableColor()
		}
	}
}

// noColorRequested reports whether the environment has asked for plain output.
// Any non-empty NO_COLOR counts, per the convention at no-color.org.
func noColorRequested() bool {
	v, ok := os.LookupEnv("NO_COLOR")
	return ok && v != ""
}

// printError reports a failure. It is the last thing the tool says before
// exiting non-zero. ReportError is the same thing under the name callers
// outside this package want, so main can print whatever Run returned without
// knowing how failures are dressed.
func printError(err error) {
	colourError.Fprintf(stderr, "bin2source: %v\n", err)
}

// ReportError writes err the way printError does, for main.
func ReportError(err error) {
	printError(err)
}

// printWarning writes a message about something worth knowing that did not
// stop the run. Warnings get their own colour rather than borrowing the
// error's: they are not failures, and painting them red would train people to
// stop reading red.
func printWarning(format string, args ...any) {
	colourWarning.Fprintf(stderr, "bin2source: warning: "+format+"\n", args...)
}

// printSuccess reports a completed run.
func printSuccess(format string, args ...any) {
	colourSuccess.Fprintf(stderr, "bin2source: "+format+"\n", args...)
}

// printUsage writes the help text. It goes out in the plain colour: it is
// reference material rather than a report on what just happened, and colouring
// it like a success would suggest something had.
func printUsage() {
	colourPlain.Fprint(stderr, usage)
}

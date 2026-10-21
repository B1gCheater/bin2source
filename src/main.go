// Command bin2source embeds an arbitrary file as a byte array in the source
// syntax of C, C++, Go, Rust or Python, optionally encrypted with AES-256.
package main

import (
	"os"

	"bin2source/src/cli"
)

func main() {
	if err := cli.Run(os.Args[1:]); err != nil {
		cli.ReportError(err)
		os.Exit(1)
	}
}

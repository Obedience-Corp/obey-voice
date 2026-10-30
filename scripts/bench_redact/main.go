// bench_redact filters one benchmark JSON document on stdin for sharing.
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/Obedience-Corp/obey-voice/internal/benchprivacy"
)

func main() {
	data, err := io.ReadAll(os.Stdin)
	if err == nil {
		data, err = benchprivacy.JSON(data)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if _, err := os.Stdout.Write(append(data, '\n')); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

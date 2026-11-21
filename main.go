package main

import (
	"fmt"
	"os"

	"github.com/hypernewbie/psi/internal/app"
	"github.com/hypernewbie/psi/internal/terminal"
)

var version = "0.5.0"

func main() {
	if err := app.Run(os.Args[1:], version); err != nil {
		fmt.Fprintf(os.Stderr, "psi: %s\n", terminal.Sanitize(err.Error()))
		os.Exit(1)
	}
}

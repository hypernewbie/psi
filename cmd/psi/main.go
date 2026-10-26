package main

import (
	"fmt"
	"os"

	"psi/internal/app"
)

var version = "0.1.0-dev"

func main() {
	if err := app.Run(os.Args[1:], version); err != nil {
		fmt.Fprintf(os.Stderr, "psi: %v\n", err)
		os.Exit(1)
	}
}

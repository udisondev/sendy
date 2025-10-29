package main

import (
	"os"

	"github.com/udisondev/sendy/cmd/sendy/cmd"
)

func main() {
	if err := cmd.Execute(); err != nil {
		os.Exit(1)
	}
}

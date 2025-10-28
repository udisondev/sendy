package main

import (
	"os"

	"sendy/cmd/sendy/cmd"
)

func main() {
	if err := cmd.Execute(); err != nil {
		os.Exit(1)
	}
}

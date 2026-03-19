package main

import (
	"os"

	"geoprism/internal/cli"
)

func main() {
	cli.Main(os.Args[1:])
}

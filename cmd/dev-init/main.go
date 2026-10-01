package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/radityama/portway/internal/devsetup"
)

func main() {
	dir := flag.String("dir", ".tmp/dev", "private development credentials directory")
	force := flag.Bool("force", false, "rotate existing local development credentials")
	flag.Parse()
	if err := devsetup.Ensure(*dir, *force); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := devsetup.EnsurePublic(*dir, *force); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("Portway development TLS and credential files are ready.")
}

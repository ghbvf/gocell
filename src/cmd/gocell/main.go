package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/ghbvf/gocell/internal/meta"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	switch os.Args[1] {
	case "validate-meta":
		if err := runValidateMeta(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	default:
		usage()
		os.Exit(2)
	}
}

func runValidateMeta(args []string) error {
	fs := flag.NewFlagSet("validate-meta", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	root := fs.String("root", ".", "metadata root directory")
	if err := fs.Parse(args); err != nil {
		return err
	}

	result, err := meta.ValidateRepository(*root)
	if err != nil {
		return err
	}

	result.Print(os.Stdout)
	if result.HasErrors() {
		return fmt.Errorf("validate-meta failed")
	}

	return nil
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage:")
	fmt.Fprintln(os.Stderr, "  gocell validate-meta [-root .]")
}

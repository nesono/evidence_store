// campaign-package builds deterministic archives and validates them offline.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/nesono/evidence-store/internal/campaign"
)

func main() { os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr)) }

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "example" {
		return example(args[1:], stdout, stderr)
	}
	if len(args) != 2 || (args[0] != "build" && args[0] != "validate") {
		_, _ = fmt.Fprintln(stderr, "Usage: campaign-package build <directory> | validate <archive.tar.gz|-> | example --repository URL --branch BRANCH --commit HASH [--title TITLE] [--output FILE]")
		return 2
	}
	var err error
	if args[0] == "build" {
		err = build(args[1], stdout, stderr)
	} else {
		err = validate(args[1], stdin, stdout)
	}
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err) // Best-effort diagnostic; exit status reports failure.
		return 1
	}
	return 0
}

func build(directory string, stdout, stderr io.Writer) error {
	files, err := campaign.LoadPackageDirectory(directory)
	if err != nil {
		return err
	}
	data, p, err := campaign.BuildPackage(files)
	if err != nil {
		return err
	}
	n, err := stdout.Write(data)
	if err != nil {
		return fmt.Errorf("write archive: %w", err)
	}
	if n != len(data) {
		return fmt.Errorf("write archive: %w", io.ErrShortWrite)
	}
	_, _ = fmt.Fprintln(stderr, p.Digest) // Archive output is primary; digest is informational.
	return nil
}

func example(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("example", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var product campaign.ProductRevision
	flags.StringVar(&product.Repository, "repository", "", "Product repository HTTPS URL")
	flags.StringVar(&product.Branch, "branch", "", "Product build branch")
	flags.StringVar(&product.Commit, "commit", "", "Full product Git commit hash")
	output := flags.String("output", "", "Archive destination (default stdout)")
	title := flags.String("title", "Vehicle feature tour", "Campaign title")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		_, _ = fmt.Fprintln(stderr, "example takes flags only")
		return 2
	}
	files, err := campaign.ExamplePackage(product, *title)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	data, p, err := campaign.BuildPackage(files)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	if *output != "" {
		err = os.WriteFile(*output, data, 0644)
	} else {
		var n int
		n, err = stdout.Write(data)
		if err == nil && n != len(data) {
			err = io.ErrShortWrite
		}
	}
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	_, _ = fmt.Fprintf(stderr, "%s · %d scenarios · product commit %s\n", p.Digest, len(p.Scenarios), product.Commit)
	return 0
}

func validate(name string, stdin io.Reader, stdout io.Writer) error {
	input := stdin
	if name != "-" {
		f, err := os.Open(name)
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }() // Read-only input.
		input = f
	}
	p, err := campaign.ReadPackage(input)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(p)
}

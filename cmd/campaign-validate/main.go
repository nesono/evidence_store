// campaign-validate checks one procedure Markdown document and writes its
// ordered, typed representation as JSON. It does not import or execute tests.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/nesono/evidence-store/internal/campaign"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		if _, err := fmt.Fprintln(stdout, "Usage: campaign-validate <procedure.md|->"); err != nil {
			_, _ = fmt.Fprintf(stderr, "write help: %v\n", err)
			return 1
		}
		return 0
	}
	// Diagnostics are best-effort: a stderr write failure cannot be reported
	// through another channel, and each error path already returns nonzero.
	if len(args) != 1 {
		_, _ = fmt.Fprintln(stderr, "Usage: campaign-validate <procedure.md|->")
		return 2
	}
	input := stdin
	if args[0] != "-" {
		file, err := os.Open(args[0])
		if err != nil {
			_, _ = fmt.Fprintln(stderr, err)
			return 1
		}
		defer func() { _ = file.Close() }() // Read-only input; there is no pending write to flush.
		input = file
	}
	data, err := io.ReadAll(io.LimitReader(input, campaign.MaxProcedureBytes+1))
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "%s: %v\n", args[0], err)
		return 1
	}
	procedure, err := campaign.ParseProcedure(string(data))
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "%s: %v\n", args[0], err)
		return 1
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(procedure); err != nil {
		_, _ = fmt.Fprintf(stderr, "write JSON: %v\n", err)
		return 1
	}
	return 0
}

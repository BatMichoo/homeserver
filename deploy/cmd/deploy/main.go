// Command deploy is the home server's deployer.
//
//	deploy [run]                       poll and deploy (see the README)
//	deploy lint --project-dir <dir>    check `docker compose config --format json` on stdin
package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/BatMichoo/homeserver/deploy/internal/compose"
)

func main() {
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "lint" {
		os.Exit(lint(args[1:], os.Stdin, os.Stdout))
	}
	fmt.Fprintln(os.Stderr, "usage: deploy [run] | deploy lint --project-dir <dir>")
	os.Exit(2)
}

// lint reports compose services the deployer can't handle well.
func lint(args []string, in io.Reader, out io.Writer) int {
	fs := flag.NewFlagSet("lint", flag.ContinueOnError)
	dir := fs.String("project-dir", ".", "compose project directory")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	data, err := io.ReadAll(in)
	if err != nil {
		fmt.Fprintln(out, err)
		return 2
	}
	p, err := compose.Parse(data, *dir)
	if err != nil {
		fmt.Fprintln(out, err)
		return 2
	}
	problems := compose.Lint(p)
	for _, pr := range problems {
		fmt.Fprintln(out, pr)
	}
	if len(problems) > 0 {
		return 1
	}
	return 0
}

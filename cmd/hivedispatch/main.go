// Command hivedispatch is the HiveDispatch worker CLI.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/thomasmeadows/hivedispatch/internal/config"
)

// version is overridden at build time via -ldflags "-X main.version=...".
var version = "dev"

const usage = `usage: hivedispatch <command> [flags]

commands:
  version              print the version
  check [-config P]    validate the worker config
  init  [-config P]    set up tracker fields (not implemented yet)
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "version":
		fmt.Fprintf(stdout, "hivedispatch %s\n", version)
		return 0
	case "check":
		return runCheck(args[1:], stdout, stderr)
	case "init":
		fmt.Fprintln(stderr, "init: not implemented yet")
		return 2
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n%s", args[0], usage)
		return 2
	}
}

func runCheck(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", config.DefaultPath(), "path to worker config")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "config ok: agent %s, %d repo(s), jira %s\n", cfg.AgentID, len(cfg.Repos), cfg.Jira.BaseURL)
	return 0
}

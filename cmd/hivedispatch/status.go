package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"sort"
	"text/tabwriter"

	"github.com/thomasmeadows/hivedispatch/internal/config"
)

// runStatus lists every run record from the state stores. It reads local
// state only: no Jira, no token discovery, no executor.
func runStatus(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", config.DefaultPath(), "path to worker config")
	asJSON := fs.Bool("json", false, "print the run records as JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	ctx := context.Background()
	_, store, err := openStores(ctx, cfg)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	runs, err := store.List(ctx)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].UpdatedAt.After(runs[j].UpdatedAt) })
	if *asJSON {
		if runs == nil {
			fmt.Fprintln(stdout, "[]")
			return 0
		}
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(runs); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}
	if len(runs) == 0 {
		fmt.Fprintln(stdout, "no runs recorded")
		return 0
	}
	tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "TICKET\tPHASE\tSTATUS\tATTEMPTS\tAGENT\tUPDATED\tPR")
	for _, r := range runs {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\t%s\t%s\n", r.Ticket, r.Phase, r.LastStatus, r.Attempts, r.Agent, r.UpdatedAt.Local().Format("2006-01-02 15:04"), r.PRURL)
	}
	if err := tw.Flush(); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

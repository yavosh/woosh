package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"text/tabwriter"
	"time"
)

func cmdLs(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("ls", flag.ExitOnError)
	wait := fs.Duration("wait", browseFor, "how long to listen for announces")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), "usage: woosh ls [--wait D]\n\n"+
			"lists the offers waiting on this network. An announce carries no file\n"+
			"name and no size, so you need the code to see what an offer holds.\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	return runLs(ctx, *wait, os.Stdout)
}

func runLs(ctx context.Context, wait time.Duration, out *os.File) error {
	offers, err := collectOffers(ctx, wait)
	if err != nil {
		return err
	}
	if len(offers) == 0 {
		fmt.Fprintln(out, "no offers on this network")
		return nil
	}
	sort.Slice(offers, func(i, j int) bool { return offers[i].From < offers[j].From })

	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tFROM\tADDRESS")
	for _, o := range offers {
		fmt.Fprintf(tw, "%s\t%s\t%s\n", o.ID, o.From, o.addr())
	}
	return tw.Flush()
}

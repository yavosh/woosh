// Command woosh copies text snippets and files between machines on a local network.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// version is set at build time with -ldflags "-X main.version=..."
var version = "dev"

const usage = `woosh - copy text snippets and files across the local network

usage:
  woosh init [--name NAME]      create or show this device's identity
  woosh send [flags] [path...]  offer files, or a snippet from stdin
  woosh recv [flags] [code]     take an offer
  woosh ls                      list offers on the network
  woosh version                 print the version

run "woosh <command> -h" for the flags of a command.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var err error
	switch os.Args[1] {
	case "init":
		err = cmdInit(os.Args[2:])
	case "send":
		err = runTransfer(ctx, func() error { return cmdSend(ctx, os.Args[2:]) })
	case "recv":
		err = runTransfer(ctx, func() error { return cmdRecv(ctx, os.Args[2:]) })
	case "ls":
		err = cmdLs(ctx, os.Args[2:])
	case "version":
		fmt.Println(version)
	case "-h", "--help", "help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "woosh: unknown command %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}

	if err != nil {
		if errors.Is(err, context.Canceled) {
			os.Exit(130)
		}
		fmt.Fprintf(os.Stderr, "woosh: %s\n", cleanMessage(err.Error()))
		os.Exit(1)
	}
}

// runTransfer gives cancellation a second to clean up before main exits.
// Inherited stdin or stdout can block in a syscall that Close cannot interrupt.
func runTransfer(ctx context.Context, command func() error) error {
	done := make(chan error, 1)
	go func() { done <- command() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		timer := time.NewTimer(time.Second)
		defer timer.Stop()
		select {
		case <-done:
		case <-timer.C:
		}
		return ctx.Err()
	}
}

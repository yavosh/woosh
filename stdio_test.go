package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"slices"
	"testing"
	"time"
)

// TestTransferProcess runs only in a child with shell-style inherited streams.
func TestTransferProcess(t *testing.T) {
	separator := slices.Index(os.Args, "--")
	if separator == -1 {
		return
	}
	args := os.Args[separator+1:]
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	key, code := make([]byte, keyLen), "1-alpha-beta"
	started := make(chan struct{}, 1)
	go func() {
		<-started
		fmt.Fprintln(os.Stderr, "blocked")
	}()
	err := runTransfer(ctx, func() error {
		if args[0] == "send" {
			return runSend(ctx, sendOpts{
				key: key, code: code, stdin: &startedReader{os.Stdin, started},
				out: io.Discard, silent: true,
				onReady: func(a net.Addr) {
					_, port, _ := net.SplitHostPort(a.String())
					fmt.Fprintln(os.Stderr, net.JoinHostPort("127.0.0.1", port))
				},
			})
		}
		return runRecv(ctx, recvOpts{
			key: key, code: code, addr: args[1], toStdout: true,
			stdout: &startedWriter{os.Stdout, started}, msg: io.Discard, silent: true,
		})
	})
	if errors.Is(err, context.Canceled) {
		os.Exit(130)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

func TestInheritedStdioCancellation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("subprocess interrupt fixture needs Unix signals")
	}
	for _, mode := range []string{"send", "recv"} {
		t.Run(mode, func(t *testing.T) {
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			defer w.Close()
			args := []string{"-test.run=^TestTransferProcess$", "--", mode}
			if mode == "recv" {
				addr, _, cancel := startTestSender(t, sendOpts{
					key: make([]byte, keyLen), code: "1-alpha-beta",
					stdin: bytes.NewReader(make([]byte, 16<<20)),
				})
				defer cancel()
				args = append(args, addr)
			}
			cmd := exec.Command(os.Args[0], args...)
			// The child needs no user environment or key file.
			cmd.Env = []string{"GOMAXPROCS=2", "GORACE=atexit_sleep_ms=0"}
			if mode == "send" {
				cmd.Stdin = r
			} else {
				cmd.Stdout = w
			}
			stderr, err := cmd.StderrPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer cmd.Process.Kill() // Only this disposable test child.
			lines := make(chan string, 2)
			go func() {
				scanner := bufio.NewScanner(stderr)
				for scanner.Scan() {
					lines <- scanner.Text()
				}
				close(lines)
			}()
			nextLine := func() string {
				t.Helper()
				select {
				case line, ok := <-lines:
					if !ok {
						t.Fatal("child exited before transfer started")
					}
					return line
				case <-time.After(3 * time.Second):
					t.Fatal("child never started the transfer")
					return ""
				}
			}
			if mode == "send" {
				connectTestReceiver(t, make([]byte, keyLen), "1-alpha-beta", nextLine())
			}
			if got := nextLine(); got != "blocked" {
				t.Fatalf("unexpected child status %q", got)
			}
			if err := cmd.Process.Signal(os.Interrupt); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case err := <-done:
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.ExitCode() != 130 {
					t.Fatalf("child cancellation exit is %v, want 130", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("inherited stream prevented cancellation")
			}
		})
	}
}

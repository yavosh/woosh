package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
)

// confirmPeer uses the controlling terminal, leaving payload stdin and stdout untouched.
func confirmPeer(ctx context.Context, peer peerIdentity) (bool, error) {
	inPath, outPath := "/dev/tty", "/dev/tty"
	if runtime.GOOS == "windows" {
		inPath, outPath = "CONIN$", "CONOUT$"
	}
	in, err := os.Open(inPath)
	if err != nil {
		return false, errors.New("unknown peer; run this transfer in a terminal to confirm its public key")
	}
	defer in.Close()
	out, err := os.OpenFile(outPath, os.O_WRONLY, 0)
	if err != nil {
		return false, err
	}
	defer out.Close()
	stop := context.AfterFunc(ctx, func() { in.Close(); out.Close() })
	defer stop()
	return promptPeer(ctx, in, out, peer)
}

func promptPeer(ctx context.Context, in io.Reader, out io.Writer, peer peerIdentity) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if _, err := fmt.Fprintf(out, "\nUnknown device %q at %s.\nED25519 fingerprint: %s\nCompare this fingerprint with \"woosh init\" on that device.\nTrust this device and save its public key? (yes/no): ", peer.Name, cleanText(peer.Addr), fingerprint(peer.Key)); err != nil {
		return false, err
	}
	// Bound input without reading any bytes from the transfer's stdin.
	r := bufio.NewReader(io.LimitReader(in, 128))
	type answer struct {
		line string
		err  error
	}
	done := make(chan answer, 1)
	go func() {
		line, err := r.ReadString('\n')
		done <- answer{line, err}
	}()
	select {
	case <-ctx.Done():
		return false, ctx.Err()
	case got := <-done:
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if got.err != nil {
			return false, errors.New("peer confirmation needs a complete yes or no response")
		}
		return strings.TrimSpace(got.line) == "yes", nil
	}
}

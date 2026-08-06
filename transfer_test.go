package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// transfer runs a send and a recv against each other over loopback, with the
// multicast beacon switched off. It returns the receiver's error.
func transfer(t *testing.T, send sendOpts, recv recvOpts) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	if send.out == nil {
		send.out = io.Discard
	}
	if recv.msg == nil {
		recv.msg = io.Discard
	}
	send.silent, recv.silent = true, true
	send.announce = false
	if send.timeout == 0 {
		send.timeout = 15 * time.Second
	}

	ready := make(chan string, 1)
	send.onReady = func(a net.Addr) {
		ready <- net.JoinHostPort("127.0.0.1", portOf(t, a))
	}

	sendErr := make(chan error, 1)
	go func() { sendErr <- runSend(ctx, send) }()

	select {
	case addr := <-ready:
		recv.addr = addr
	case err := <-sendErr:
		t.Fatalf("sender stopped before it was ready: %v", err)
	}

	err := runRecv(ctx, recv)
	if serr := <-sendErr; serr != nil && err == nil {
		t.Errorf("sender: %v", serr)
	}
	return err
}

func portOf(t *testing.T, a net.Addr) string {
	t.Helper()
	_, port, err := net.SplitHostPort(a.String())
	if err != nil {
		t.Fatal(err)
	}
	return port
}

func TestTransferSnippetToStdout(t *testing.T) {
	key := testKey(t)
	var got bytes.Buffer

	err := transfer(t,
		sendOpts{key: key, code: "1-alpha-beta", stdin: strings.NewReader("hello woosh\n"), name: "snippet.txt"},
		recvOpts{key: key, code: "1-alpha-beta", stdout: &got, toStdout: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got.String() != "hello woosh\n" {
		t.Errorf("got %q", got.String())
	}
}

func TestTransferFile(t *testing.T) {
	key := testKey(t)
	src := t.TempDir()
	body := bytes.Repeat([]byte("woosh"), 200_000) // spans several chunks
	writeFile(t, filepath.Join(src, "report.pdf"), string(body), 0o600)

	dest := t.TempDir()
	err := transfer(t,
		sendOpts{key: key, code: "2-gamma-delta", paths: []string{filepath.Join(src, "report.pdf")}},
		recvOpts{key: key, code: "2-gamma-delta", out: dest},
	)
	if err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(filepath.Join(dest, "report.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, body) {
		t.Error("the file arrived changed")
	}
	if info, err := os.Stat(filepath.Join(dest, "report.pdf")); err == nil {
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("mode is %o, want 600", perm)
		}
	}
	if leftovers := partialFiles(t, dest); len(leftovers) > 0 {
		t.Errorf("partial files left behind: %v", leftovers)
	}
}

func TestTransferDirectory(t *testing.T) {
	key := testKey(t)
	src := t.TempDir()
	writeFile(t, filepath.Join(src, "notes", "a.txt"), "alpha", 0o644)
	writeFile(t, filepath.Join(src, "notes", "deep", "b.txt"), "bravo", 0o644)

	dest := t.TempDir()
	err := transfer(t,
		sendOpts{key: key, code: "3-epsilon-zeta", paths: []string{filepath.Join(src, "notes")}},
		recvOpts{key: key, code: "3-epsilon-zeta", out: dest},
	)
	if err != nil {
		t.Fatal(err)
	}

	for name, want := range map[string]string{"notes/a.txt": "alpha", "notes/deep/b.txt": "bravo"} {
		got, err := os.ReadFile(filepath.Join(dest, filepath.FromSlash(name)))
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if string(got) != want {
			t.Errorf("%s: got %q, want %q", name, got, want)
		}
	}
	if leftovers := partialFiles(t, dest); len(leftovers) > 0 {
		t.Errorf("staging left behind: %v", leftovers)
	}
}

func TestTransferFailsWithWrongKey(t *testing.T) {
	err := transfer(t,
		sendOpts{key: testKey(t), code: "4-eta-theta", stdin: strings.NewReader("secret")},
		recvOpts{key: testKey(t), code: "4-eta-theta", stdout: io.Discard, toStdout: true},
	)
	if err == nil {
		t.Fatal("the transfer went through with a different key")
	}
}

func TestTransferFailsWithWrongCode(t *testing.T) {
	key := testKey(t)
	err := transfer(t,
		sendOpts{key: key, code: "5-iota-kappa", stdin: strings.NewReader("secret")},
		recvOpts{key: key, code: "5-iota-lambda", stdout: io.Discard, toStdout: true},
	)
	if !errors.Is(err, errBadKey) {
		t.Fatalf("error is %v, want errBadKey", err)
	}
}

func TestRecvRefusesToOverwrite(t *testing.T) {
	key := testKey(t)
	src := t.TempDir()
	writeFile(t, filepath.Join(src, "a.txt"), "new", 0o644)

	dest := t.TempDir()
	writeFile(t, filepath.Join(dest, "a.txt"), "old", 0o644)

	err := transfer(t,
		sendOpts{key: key, code: "6-mu-nu", paths: []string{filepath.Join(src, "a.txt")}},
		recvOpts{key: key, code: "6-mu-nu", out: dest},
	)
	if err == nil {
		t.Fatal("recv replaced a file without --force")
	}
	got, _ := os.ReadFile(filepath.Join(dest, "a.txt"))
	if string(got) != "old" {
		t.Errorf("the file on disk became %q", got)
	}
}

func TestRecvOverwritesWithForce(t *testing.T) {
	key := testKey(t)
	src := t.TempDir()
	writeFile(t, filepath.Join(src, "a.txt"), "new", 0o644)

	dest := t.TempDir()
	writeFile(t, filepath.Join(dest, "a.txt"), "old", 0o644)

	err := transfer(t,
		sendOpts{key: key, code: "7-xi-omicron", paths: []string{filepath.Join(src, "a.txt")}},
		recvOpts{key: key, code: "7-xi-omicron", out: dest, force: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(dest, "a.txt"))
	if string(got) != "new" {
		t.Errorf("the file on disk is %q, want %q", got, "new")
	}
}

func TestRecvHonoursMaxSize(t *testing.T) {
	key := testKey(t)
	src := t.TempDir()
	writeFile(t, filepath.Join(src, "big.bin"), strings.Repeat("x", 200_000), 0o644)

	dest := t.TempDir()
	err := transfer(t,
		sendOpts{key: key, code: "8-pi-rho", paths: []string{filepath.Join(src, "big.bin")}},
		recvOpts{key: key, code: "8-pi-rho", out: dest, limit: 1000},
	)
	if err == nil {
		t.Fatal("recv took a payload above --max-size")
	}
	if _, err := os.Stat(filepath.Join(dest, "big.bin")); err == nil {
		t.Error("a rejected payload was left in place")
	}
	if leftovers := partialFiles(t, dest); len(leftovers) > 0 {
		t.Errorf("partial files left behind: %v", leftovers)
	}
}

// TestSendSurvivesABadPeer is the reason a failed attempt does not end an
// offer: the port is on the open network, so anyone can reach it, and a
// stranger with the wrong key must not be able to end somebody else's send.
func TestSendSurvivesABadPeer(t *testing.T) {
	good, bad := testKey(t), testKey(t)
	src := t.TempDir()
	writeFile(t, filepath.Join(src, "a.txt"), "alpha", 0o644)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	ready := make(chan string, 1)
	sendErr := make(chan error, 1)
	go func() {
		sendErr <- runSend(ctx, sendOpts{
			key: good, code: "1-alpha-alpha", paths: []string{filepath.Join(src, "a.txt")},
			out: io.Discard, silent: true, timeout: 15 * time.Second,
			onReady: func(a net.Addr) { ready <- net.JoinHostPort("127.0.0.1", portOf(t, a)) },
		})
	}()

	var addr string
	select {
	case addr = <-ready:
	case err := <-sendErr:
		t.Fatalf("sender stopped before it was ready: %v", err)
	}

	stranger := runRecv(ctx, recvOpts{key: bad, addr: addr, code: "1-alpha-alpha",
		stdout: io.Discard, toStdout: true, msg: io.Discard, silent: true})
	if stranger == nil {
		t.Fatal("a receiver with the wrong key was served")
	}

	dest := t.TempDir()
	if err := runRecv(ctx, recvOpts{key: good, addr: addr, code: "1-alpha-alpha",
		out: dest, msg: io.Discard, silent: true}); err != nil {
		t.Fatalf("the offer did not survive the stranger: %v", err)
	}
	if err := <-sendErr; err != nil {
		t.Errorf("sender: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(dest, "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "alpha" {
		t.Errorf("got %q, want %q", got, "alpha")
	}
}

func TestSendKeepRejectsStdin(t *testing.T) {
	err := runSend(context.Background(), sendOpts{
		key: testKey(t), keep: true, stdin: strings.NewReader("x"), out: io.Discard,
	})
	if err == nil {
		t.Error("--keep was accepted with no path")
	}
}

func TestSendTimesOut(t *testing.T) {
	err := runSend(context.Background(), sendOpts{
		key: testKey(t), code: "9-sigma-tau", stdin: strings.NewReader("x"),
		out: io.Discard, timeout: 150 * time.Millisecond, silent: true,
	})
	if err == nil {
		t.Fatal("send waited past its timeout")
	}
}

func partialFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".partial") || strings.HasPrefix(e.Name(), ".woosh-partial-") {
			out = append(out, e.Name())
		}
	}
	return out
}

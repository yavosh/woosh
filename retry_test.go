package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReceiverCanRetryDeclinedOfferWithForce(t *testing.T) {
	sender, receiver := testDevice(t, "sender"), testDevice(t, "receiver")
	approve := func(context.Context, peerIdentity) (bool, error) { return true, nil }
	senderAuth := storedAuth(t, sender, peerTrust{dir: t.TempDir(), confirm: approve})
	receiverAuth := storedAuth(t, receiver, peerTrust{dir: t.TempDir(), confirm: approve})
	src, dest := t.TempDir(), t.TempDir()
	body := bytes.Repeat([]byte("payload"), 6000)
	source := filepath.Join(src, "coverage.out")
	writeFile(t, source, string(body), 0o600)
	target := filepath.Join(dest, "coverage.out")
	writeFile(t, target, "existing data", 0o600)
	var messages bytes.Buffer
	addr, done, cancel := startTestSender(t, sendOpts{auth: senderAuth, code: "3-aspen-coral", paths: []string{source}, out: &messages})
	defer cancel()
	ctx, stop := context.WithTimeout(t.Context(), 3*time.Second)
	defer stop()
	opts := recvOpts{auth: receiverAuth, code: "3-aspen-coral", addr: addr, out: dest, msg: io.Discard, silent: true}
	if err := runRecv(ctx, opts); err == nil || !strings.Contains(err.Error(), "already there") {
		t.Fatalf("first receiver: %v", err)
	}
	requireContent(t, target, "existing data")
	opts.force = true
	if err := runRecv(ctx, opts); err != nil {
		t.Fatalf("forced retry: %v", err)
	}
	if err := waitSender(t, done); err != nil {
		t.Fatalf("sender: %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, body) {
		t.Fatal("forced retry did not receive the payload")
	}
	if !strings.Contains(messages.String(), "use --force") {
		t.Fatal("sender truncated the retry instruction")
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatal("retry exceeded its deadline")
	}
}

func TestRetryAfterStalledKnownPeer(t *testing.T) {
	sender, receiver := testDevice(t, "sender"), testDevice(t, "receiver")
	approve := func(context.Context, peerIdentity) (bool, error) { return true, nil }
	senderStore := peerTrust{dir: t.TempDir(), confirm: approve}
	receiverStore := peerTrust{dir: t.TempDir(), confirm: approve}
	if err := senderStore.verify(t.Context(), peerIdentity{Name: receiver.Name, Key: receiver.publicKey()}); err != nil {
		t.Fatal(err)
	}
	if err := receiverStore.verify(t.Context(), peerIdentity{Name: sender.Name, Key: sender.publicKey()}); err != nil {
		t.Fatal(err)
	}
	senderAuth, receiverAuth := storedAuth(t, sender, senderStore), storedAuth(t, receiver, receiverStore)
	src, dest := t.TempDir(), t.TempDir()
	source := filepath.Join(src, "coverage.out")
	writeFile(t, source, "new payload", 0o600)
	writeFile(t, filepath.Join(dest, "coverage.out"), "existing data", 0o600)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	ready := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		done <- runSend(ctx, sendOpts{auth: senderAuth, code: "3-aspen-coral", paths: []string{source}, out: io.Discard, silent: true,
			onReady: func(a net.Addr) { ready <- net.JoinHostPort("127.0.0.1", portOf(t, a)) },
		})
	}()
	var addr string
	select {
	case addr = <-ready:
	case err := <-done:
		t.Fatal(err)
	case <-ctx.Done():
		t.Fatal("sender did not become ready")
	}
	opts := recvOpts{auth: receiverAuth, code: "3-aspen-coral", addr: addr, out: dest, msg: io.Discard, silent: true}
	if err := runRecv(ctx, opts); err == nil || !strings.Contains(err.Error(), "already there") {
		t.Fatalf("first receiver: %v", err)
	}

	raw, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	raw.SetDeadline(time.Now().Add(10 * time.Second))
	stalled := tls.Client(raw, receiverAuth.clientConfig())
	hello := map[string]any{"auth": authVersion, "offer": offerID(opts.code), "known": true}
	if err := writeJSONLine(stalled, hello); err != nil {
		t.Fatal(err)
	}
	r := bufio.NewReader(stalled)
	var greeting authHello
	if err := readJSONLine(r, &greeting); err != nil {
		t.Fatal(err)
	}
	var approval reply
	if err := readJSONLine(r, &approval); err != nil || !approval.Accept {
		t.Fatalf("sender approval: %v", err)
	}
	// The known receiver stalls before sending its approval. A retry must still complete promptly.
	retryCtx, stop := context.WithTimeout(ctx, 8*time.Second)
	defer stop()
	opts.force = true
	if err := runRecv(retryCtx, opts); err != nil {
		t.Fatalf("a stalled known connection blocked the forced retry: %v", err)
	}
	if err := waitSender(t, done); err != nil {
		t.Fatal(err)
	}
	requireContent(t, filepath.Join(dest, "coverage.out"), "new payload")
}

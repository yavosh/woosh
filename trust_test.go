package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func testDevice(t *testing.T, name string) deviceIdentity {
	t.Helper()
	return deviceIdentity{Name: name, Seed: testKey(t)}
}

func storedAuth(t *testing.T, d deviceIdentity, store peerTrust) peerAuth {
	t.Helper()
	auth, err := newPeerAuth(d, store.verify)
	if err != nil {
		t.Fatal(err)
	}
	auth.offerID = "abc123"
	return auth
}

func TestMutualFirstUseAndRememberedTrust(t *testing.T) {
	sender, receiver := testDevice(t, "sender"), testDevice(t, "receiver")
	senderCalls, receiverCalls := 0, 0
	senderTrust := peerTrust{dir: filepath.Join(t.TempDir(), "known_peers"), confirm: func(_ context.Context, p peerIdentity) (bool, error) {
		senderCalls++
		if p.Name != receiver.Name || !bytes.Equal(p.Key, receiver.publicKey()) {
			return false, errors.New("unexpected receiver")
		}
		return true, nil
	}}
	receiverTrust := peerTrust{dir: filepath.Join(t.TempDir(), "known_peers"), confirm: func(_ context.Context, p peerIdentity) (bool, error) {
		receiverCalls++
		if p.Name != sender.Name || !bytes.Equal(p.Key, sender.publicKey()) {
			return false, errors.New("unexpected sender")
		}
		return true, nil
	}}
	for range 2 {
		got, serverErr, clientErr := authPair(t, storedAuth(t, sender, senderTrust), storedAuth(t, receiver, receiverTrust))
		if serverErr != nil || clientErr != nil || got != "payload follows the proof" {
			t.Fatalf("mutual trust: server=%v client=%v", serverErr, clientErr)
		}
	}
	if senderCalls != 1 || receiverCalls != 1 {
		t.Fatalf("confirmation count: sender=%d receiver=%d", senderCalls, receiverCalls)
	}
	for _, store := range []peerTrust{senderTrust, receiverTrust} {
		entries, err := os.ReadDir(store.dir)
		if err != nil || len(entries) != 1 {
			t.Fatalf("known-peer persistence: %v", err)
		}
		if runtime.GOOS != "windows" && dirPerm(t, filepath.Join(store.dir, entries[0].Name())) != 0o600 {
			t.Fatal("known-peer file is not private")
		}
	}
}

func TestEitherDeviceCanDeclineTrust(t *testing.T) {
	for _, rejecting := range []string{"sender", "receiver"} {
		t.Run(rejecting, func(t *testing.T) {
			sender, receiver := testDevice(t, "sender"), testDevice(t, "receiver")
			stores := map[string]peerTrust{}
			for _, name := range []string{"sender", "receiver"} {
				stores[name] = peerTrust{dir: filepath.Join(t.TempDir(), "known_peers"), confirm: func(context.Context, peerIdentity) (bool, error) { return name != rejecting, nil }}
			}
			got, serverErr, clientErr := authPair(t, storedAuth(t, sender, stores["sender"]), storedAuth(t, receiver, stores["receiver"]))
			if serverErr == nil || clientErr == nil || got != "" {
				t.Fatalf("declined trust released a payload: server=%v client=%v", serverErr, clientErr)
			}
			dir := stores[rejecting].dir
			entries, err := os.ReadDir(dir)
			if !errors.Is(err, os.ErrNotExist) && (err != nil || len(entries) != 0) {
				t.Fatal("declined trust was saved")
			}
		})
	}
}

func TestChangedKeyDoesNotPromptOrOverwrite(t *testing.T) {
	first := testDevice(t, "peer")
	peer := peerIdentity{Name: first.Name, Key: first.publicKey()}
	calls := 0
	store := peerTrust{dir: t.TempDir(), confirm: func(context.Context, peerIdentity) (bool, error) { calls++; return true, nil }}
	if err := store.verify(t.Context(), peer); err != nil {
		t.Fatal(err)
	}
	changed := peerIdentity{Name: peer.Name, Key: testDevice(t, peer.Name).publicKey()}
	if err := store.verify(t.Context(), changed); !errors.Is(err, errPeerKeyChanged) {
		t.Fatalf("changed key: %v", err)
	}
	if calls != 1 {
		t.Fatal("asked to replace a known public key")
	}
	if known, err := store.check(peer); err != nil || !known {
		t.Fatal("changed key replaced saved trust")
	}
}

func TestUnknownPeerFailsWithoutTerminalConfirmation(t *testing.T) {
	peer := peerIdentity{Name: "peer", Key: testDevice(t, "peer").publicKey()}
	store := peerTrust{dir: t.TempDir()}
	if err := store.verify(t.Context(), peer); err == nil || !strings.Contains(err.Error(), "terminal") {
		t.Fatalf("headless first use: %v", err)
	}
	entries, err := os.ReadDir(store.dir)
	if err != nil || len(entries) != 0 {
		t.Fatal("headless first use saved trust")
	}
}

func TestTrustCancellationDoesNotPersist(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	peer := peerIdentity{Name: "peer", Key: testDevice(t, "peer").publicKey()}
	store := peerTrust{dir: t.TempDir(), confirm: func(context.Context, peerIdentity) (bool, error) { cancel(); return true, nil }}
	if err := store.verify(ctx, peer); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled approval: %v", err)
	}
	entries, err := os.ReadDir(store.dir)
	if err != nil || len(entries) != 0 {
		t.Fatal("canceled approval saved trust")
	}
}

func TestConcurrentApprovalsPreserveAllPeers(t *testing.T) {
	store := peerTrust{dir: t.TempDir(), confirm: func(context.Context, peerIdentity) (bool, error) { return true, nil }}
	peers := []peerIdentity{}
	for _, name := range []string{"first", "second", "third"} {
		peers = append(peers, peerIdentity{Name: name, Key: testDevice(t, name).publicKey()})
	}
	var wg sync.WaitGroup
	failures := make(chan error, 12)
	for range 4 {
		for _, peer := range peers {
			wg.Go(func() { failures <- store.verify(t.Context(), peer) })
		}
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Error(err)
		}
	}
	for _, peer := range peers {
		if known, err := store.check(peer); err != nil || !known {
			t.Fatal("concurrent approval lost a trusted peer")
		}
	}
	entries, err := os.ReadDir(store.dir)
	if err != nil || len(entries) != len(peers) {
		t.Fatal("approval left staging files")
	}
}

func TestConflictingConcurrentApprovalCannotReplaceKey(t *testing.T) {
	peer := peerIdentity{Name: "peer", Key: testDevice(t, "peer").publicKey()}
	conflicting := peerIdentity{Name: "peer", Key: testDevice(t, "peer").publicKey()}
	store := peerTrust{dir: t.TempDir()}
	store.confirm = func(ctx context.Context, _ peerIdentity) (bool, error) {
		winner := store
		winner.confirm = func(context.Context, peerIdentity) (bool, error) { return true, nil }
		return true, winner.verify(ctx, conflicting)
	}
	if err := store.verify(t.Context(), peer); !errors.Is(err, errPeerKeyChanged) {
		t.Fatalf("concurrent replacement: %v", err)
	}
	if known, err := store.check(conflicting); err != nil || !known {
		t.Fatal("concurrent winner was replaced")
	}
}

func TestKnownTrustRejectsSymlinksAndMalformedFiles(t *testing.T) {
	peer := peerIdentity{Name: "peer", Key: testDevice(t, "peer").publicKey()}
	store := peerTrust{dir: t.TempDir(), confirm: func(context.Context, peerIdentity) (bool, error) {
		t.Fatal("prompted for an invalid trust file")
		return true, nil
	}}
	path := store.path(peer.Name)
	writeFile(t, path, "malformed", 0o600)
	if err := store.verify(t.Context(), peer); err == nil {
		t.Fatal("accepted a malformed trust file")
	}
	requireContent(t, path, "malformed")
	if runtime.GOOS == "windows" {
		return
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(t.TempDir(), "unrelated")
	writeFile(t, victim, "synthetic placeholder", 0o600)
	if err := os.Symlink(victim, path); err != nil {
		t.Fatal(err)
	}
	if err := store.verify(t.Context(), peer); err == nil {
		t.Fatal("followed a known-peer symlink")
	}
	requireContent(t, victim, "synthetic placeholder")
}

func TestPromptRequiresExplicitYes(t *testing.T) {
	peer := peerIdentity{Name: "peer", Key: testDevice(t, "peer").publicKey(), Addr: "127.0.0.1:1234"}
	for _, answer := range []string{"yes\n", "no\n", "y\n", "\n", "yes", "yes please\n", strings.Repeat("y", 200) + "\n"} {
		var messages bytes.Buffer
		accepted, _ := promptPeer(t.Context(), strings.NewReader(answer), &messages, peer)
		if accepted != (answer == "yes\n") {
			t.Errorf("unexpected acceptance for %q", answer)
		}
		if !strings.Contains(messages.String(), fingerprint(peer.Key)) || !strings.Contains(messages.String(), "yes/no") {
			t.Fatal("prompt omitted the fingerprint or confirmation")
		}
	}
}

func TestPromptCancellationWithoutInput(t *testing.T) {
	r, w := io.Pipe()
	defer r.Close()
	defer w.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	peer := peerIdentity{Name: "peer", Key: testDevice(t, "peer").publicKey()}
	if accepted, err := promptPeer(ctx, r, io.Discard, peer); accepted || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("prompt cancellation: %v", err)
	}
}

func TestTransferConfirmsBothDevicesBeforeReadingStdin(t *testing.T) {
	sender, receiver := testDevice(t, "sender"), testDevice(t, "receiver")
	senderConfirmed, receiverConfirmed := make(chan struct{}), make(chan struct{})
	senderStore := peerTrust{dir: t.TempDir(), confirm: func(context.Context, peerIdentity) (bool, error) { close(senderConfirmed); return true, nil }}
	receiverStore := peerTrust{dir: t.TempDir(), confirm: func(context.Context, peerIdentity) (bool, error) { close(receiverConfirmed); return true, nil }}
	// A finite reader checks the first read and then serves the rest without restarting.
	first := true
	body := strings.NewReader("payload\n")
	input := readFunc(func(p []byte) (int, error) {
		if first {
			first = false
			select {
			case <-senderConfirmed:
			default:
				return 0, errors.New("read stdin before sender confirmation")
			}
			select {
			case <-receiverConfirmed:
			default:
				return 0, errors.New("read stdin before receiver confirmation")
			}
		}
		return body.Read(p)
	})
	var got bytes.Buffer
	err := transfer(t, sendOpts{auth: storedAuth(t, sender, senderStore), code: "1-alpha-beta", stdin: input}, recvOpts{auth: storedAuth(t, receiver, receiverStore), code: "1-alpha-beta", stdout: &got, toStdout: true})
	if err != nil {
		t.Fatal(err)
	}
	if got.String() != "payload\n" {
		t.Fatal("trust confirmation changed the payload")
	}
}

type readFunc func([]byte) (int, error)

func (f readFunc) Read(p []byte) (int, error) { return f(p) }

func TestTLSRejectsCertificateWithoutItsPrivateKey(t *testing.T) {
	identity := testDevice(t, "peer")
	expected := identity.publicKey()
	honest, err := newPeerAuth(identity, func(_ context.Context, peer peerIdentity) error {
		if !bytes.Equal(peer.Key, expected) {
			return errPeerKeyChanged
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	impersonator := honest
	impersonator.cert.PrivateKey = ed25519.NewKeyFromSeed(testKey(t))
	got, serverErr, clientErr := authPair(t, honest, impersonator)
	if serverErr == nil || clientErr == nil || got != "" {
		t.Fatalf("accepted a stolen public certificate: server=%v client=%v", serverErr, clientErr)
	}
}

func TestAuthVersionMismatchDoesNotPrompt(t *testing.T) {
	identity := testDevice(t, "peer")
	auth, err := newPeerAuth(identity, func(context.Context, peerIdentity) error {
		t.Error("prompted for an incompatible authentication version")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	done := make(chan error, 1)
	go func() {
		raw, err := ln.Accept()
		if err != nil {
			done <- err
			return
		}
		defer raw.Close()
		raw.SetDeadline(time.Now().Add(time.Second))
		_, err = auth.accept(tls.Server(raw, auth.serverConfig()))
		done <- err
	}()
	raw, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	raw.SetDeadline(time.Now().Add(time.Second))
	conn := tls.Client(raw, auth.clientConfig())
	if err := conn.Handshake(); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONLine(conn, authHello{V: authVersion + 1}); err != nil {
		t.Fatal(err)
	}
	var hello authHello
	if err := readJSONLine(bufio.NewReader(conn), &hello); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err == nil || !strings.Contains(err.Error(), "version mismatch") {
		t.Fatalf("unsupported version: %v", err)
	}
}

func TestRemoteDeclineCancelsOpenConfirmation(t *testing.T) {
	entered := make(chan struct{})
	sender, receiver := testDevice(t, "sender"), testDevice(t, "receiver")
	sa, err := newPeerAuth(sender, func(ctx context.Context, _ peerIdentity) error {
		close(entered)
		<-ctx.Done()
		return ctx.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	ca, err := newPeerAuth(receiver, func(context.Context, peerIdentity) error {
		<-entered
		return errPeerDeclined
	})
	if err != nil {
		t.Fatal(err)
	}
	got, serverErr, clientErr := authPair(t, sa, ca)
	if got != "" || serverErr == nil || !errors.Is(clientErr, errPeerDeclined) {
		t.Fatalf("remote decline: server=%v client=%v", serverErr, clientErr)
	}
	if errors.Is(serverErr, context.Canceled) {
		t.Fatal("remote rejection was reported as a local interrupt")
	}
}

package main

import (
	"bytes"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

// authPair runs accept and connect against each other over loopback TCP and
// returns the error each side saw. When both sides succeed it also passes a
// short message from server to client, to prove no buffered byte was lost.
func authPair(t *testing.T, serverKey, clientKey []byte, serverID, clientID string) (serverErr, clientErr error, got string) {
	t.Helper()

	sa, err := newPeerAuth(serverKey, serverID)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := newPeerAuth(clientKey, clientID)
	if err != nil {
		t.Fatal(err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	const msg = "payload follows the proof"
	done := make(chan struct{})

	go func() {
		defer close(done)
		raw, err := ln.Accept()
		if err != nil {
			serverErr = err
			return
		}
		defer raw.Close()
		raw.SetDeadline(time.Now().Add(10 * time.Second))

		conn, err := sa.accept(tls.Server(raw, sa.serverConfig()))
		if err != nil {
			serverErr = err
			return
		}
		_, serverErr = io.WriteString(conn, msg)
	}()

	raw, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	raw.SetDeadline(time.Now().Add(10 * time.Second))

	conn, clientErr := ca.connect(tls.Client(raw, ca.clientConfig()))
	if clientErr == nil {
		buf := make([]byte, len(msg))
		if _, err := io.ReadFull(conn.r, buf); err != nil {
			clientErr = err
		}
		got = string(buf)
	}

	<-done
	return serverErr, clientErr, got
}

func TestAuthSucceedsWithSameKey(t *testing.T) {
	key := testKey(t)
	serverErr, clientErr, got := authPair(t, key, key, "abc123", "abc123")
	if serverErr != nil {
		t.Fatalf("server: %v", serverErr)
	}
	if clientErr != nil {
		t.Fatalf("client: %v", clientErr)
	}
	if got != "payload follows the proof" {
		t.Errorf("stream after auth gave %q", got)
	}
}

func TestAuthFailsWithDifferentKey(t *testing.T) {
	serverErr, clientErr, _ := authPair(t, testKey(t), testKey(t), "abc123", "abc123")
	if !errors.Is(serverErr, errBadKey) {
		t.Errorf("server error is %v, want errBadKey", serverErr)
	}
	if clientErr == nil {
		t.Error("the client accepted a peer with a different key")
	}
}

func TestAuthFailsWithDifferentOfferID(t *testing.T) {
	key := testKey(t)
	serverErr, clientErr, _ := authPair(t, key, key, "abc123", "def456")
	if !errors.Is(serverErr, errBadKey) {
		t.Errorf("server error is %v, want errBadKey", serverErr)
	}
	if clientErr == nil {
		t.Error("the client accepted a proof for another offer")
	}
}

func TestTranscriptBindsBothCertificates(t *testing.T) {
	sc, cc := []byte("server-cert"), []byte("client-cert")
	base := transcript(sc, cc, "abc123")

	if bytes.Equal(base, transcript([]byte("other"), cc, "abc123")) {
		t.Error("the transcript ignores the server certificate")
	}
	if bytes.Equal(base, transcript(sc, []byte("other"), "abc123")) {
		t.Error("the transcript ignores the client certificate")
	}
	if bytes.Equal(base, transcript(cc, sc, "abc123")) {
		t.Error("the transcript does not separate the two roles")
	}
	if bytes.Equal(base, transcript(sc, cc, "def456")) {
		t.Error("the transcript ignores the offer id")
	}
}

func TestProofSeparatesRoles(t *testing.T) {
	key, tr := testKey(t), []byte("transcript")
	nc, ns := []byte("nonce-c"), []byte("nonce-s")

	if bytes.Equal(proof(key, labelClient, tr, nc, ns), proof(key, labelServer, tr, nc, ns)) {
		t.Error("the client and server proofs are the same, one could be replayed as the other")
	}
	if bytes.Equal(proof(key, labelClient, tr, nc, ns), proof(key, labelClient, tr, ns, nc)) {
		t.Error("the proof does not separate the two nonces")
	}
	if bytes.Equal(proof(key, labelClient, tr, nc, ns), proof(testKey(t), labelClient, tr, nc, ns)) {
		t.Error("the proof does not depend on the key")
	}
}

func TestEphemeralCertsDiffer(t *testing.T) {
	a, err := newEphemeralCert()
	if err != nil {
		t.Fatal(err)
	}
	b, err := newEphemeralCert()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(a.Certificate[0], b.Certificate[0]) {
		t.Error("two processes would use the same certificate")
	}
}

func testKey(t *testing.T) []byte {
	t.Helper()
	k, err := newKey()
	if err != nil {
		t.Fatal(err)
	}
	return k
}

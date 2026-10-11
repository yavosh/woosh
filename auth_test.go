package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

const testSeedLen = ed25519.SeedSize

// testKey returns synthetic seed material. No fixture reads the user's configuration.
func testKey(t *testing.T) []byte {
	t.Helper()
	seed := make([]byte, testSeedLen)
	if _, err := rand.Read(seed); err != nil {
		t.Fatal(err)
	}
	return seed
}

func testAuth(t *testing.T, seed []byte) peerAuth {
	t.Helper()
	identity := deviceIdentity{Name: "test", Seed: seed}
	expected := identity.publicKey()
	auth, err := newPeerAuth(identity, func(_ context.Context, peer peerIdentity) error {
		if !bytes.Equal(peer.Key, expected) {
			return errPeerKeyChanged
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return auth
}

func newTestPeerAuth(t *testing.T, seed []byte, id string) (peerAuth, error) {
	auth := testAuth(t, seed)
	auth.offerID = id
	return auth, nil
}

// authPair runs mutual trust over loopback and checks the buffered payload boundary.
func authPair(t *testing.T, sa, ca peerAuth) (got string, serverErr, clientErr error) {
	t.Helper()
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
	return got, serverErr, clientErr
}

func TestAuthSucceedsWithPinnedIdentity(t *testing.T) {
	seed := testKey(t)
	sa, _ := newTestPeerAuth(t, seed, "abc123")
	ca, _ := newTestPeerAuth(t, seed, "abc123")
	got, serverErr, clientErr := authPair(t, sa, ca)
	if serverErr != nil || clientErr != nil {
		t.Fatalf("authentication: server=%v client=%v", serverErr, clientErr)
	}
	if got != "payload follows the proof" {
		t.Errorf("stream after auth gave %q", got)
	}
}

func TestAuthFailsWithChangedIdentity(t *testing.T) {
	sa := testAuth(t, testKey(t))
	ca := testAuth(t, testKey(t))
	_, serverErr, clientErr := authPair(t, sa, ca)
	if !errors.Is(serverErr, errPeerKeyChanged) || !errors.Is(clientErr, errPeerKeyChanged) {
		t.Fatalf("changed keys: server=%v client=%v", serverErr, clientErr)
	}
}

func TestAuthFailsWithDifferentOfferID(t *testing.T) {
	seed := testKey(t)
	sa, _ := newTestPeerAuth(t, seed, "abc123")
	ca, _ := newTestPeerAuth(t, seed, "def456")
	calls := 0
	sa.verify = func(context.Context, peerIdentity) error { calls++; return nil }
	_, serverErr, clientErr := authPair(t, sa, ca)
	if !errors.Is(serverErr, errWrongOffer) || !errors.Is(clientErr, errWrongOffer) {
		t.Fatalf("wrong offer: server=%v client=%v", serverErr, clientErr)
	}
	if calls != 0 {
		t.Fatal("asked for trust before checking the offer")
	}
}

func TestRenewedCertificateRetainsPublicKey(t *testing.T) {
	identity := deviceIdentity{Name: "device", Seed: testKey(t)}
	a, err := newIdentityCert(identity)
	if err != nil {
		t.Fatal(err)
	}
	b, err := newIdentityCert(identity)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(a.Certificate[0], b.Certificate[0]) {
		t.Fatal("certificates did not renew")
	}
	for _, cert := range []tls.Certificate{a, b} {
		leaf, err := x509.ParseCertificate(cert.Certificate[0])
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(leaf.PublicKey.(ed25519.PublicKey), identity.publicKey()) || leaf.Subject.CommonName != identity.Name {
			t.Fatal("certificate changed the identity")
		}
	}
}

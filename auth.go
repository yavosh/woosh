package main

import (
	"bufio"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
)

const (
	labelClient = "woosh-client-v1"
	labelServer = "woosh-server-v1"
	nonceLen    = 32
)

// errBadKey means the peer could not prove it holds the same network key.
var errBadKey = errors.New("the peer does not hold this network key")

// authConn is a TLS connection whose peer has proven it holds the network key.
// Read through r, not through Conn, so no buffered byte is lost.
type authConn struct {
	*tls.Conn
	r *bufio.Reader
}

// peerAuth holds what both sides need to prove themselves to each other.
type peerAuth struct {
	key     []byte
	offerID string
	cert    tls.Certificate
}

func newPeerAuth(key []byte, offerID string) (peerAuth, error) {
	cert, err := newEphemeralCert()
	if err != nil {
		return peerAuth{}, err
	}
	return peerAuth{key: key, offerID: offerID, cert: cert}, nil
}

// serverConfig and clientConfig deliberately skip certificate verification.
// The certificates are throwaway and carry no trusted identity. accept and
// connect supply the identity, with a proof that is bound to these exact
// certificates.
func (a peerAuth) serverConfig() *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{a.cert},
		MinVersion:   tls.VersionTLS13,
		ClientAuth:   tls.RequireAnyClientCert,
	}
}

func (a peerAuth) clientConfig() *tls.Config {
	return &tls.Config{
		Certificates:       []tls.Certificate{a.cert},
		MinVersion:         tls.VersionTLS13,
		InsecureSkipVerify: true,
	}
}

// accept completes the handshake on the listening side and checks the client.
// It answers with its own proof only after the client has proven itself.
func (a peerAuth) accept(conn *tls.Conn) (*authConn, error) {
	peerCert, err := handshake(conn)
	if err != nil {
		return nil, err
	}
	tr := transcript(a.cert.Certificate[0], peerCert, a.offerID)
	r := bufio.NewReader(conn)

	nc := make([]byte, nonceLen)
	if _, err := io.ReadFull(r, nc); err != nil {
		return nil, err
	}
	ns, err := newNonce()
	if err != nil {
		return nil, err
	}
	if _, err := conn.Write(ns); err != nil {
		return nil, err
	}

	got := make([]byte, sha256.Size)
	if _, err := io.ReadFull(r, got); err != nil {
		return nil, err
	}
	if !hmac.Equal(got, proof(a.key, labelClient, tr, nc, ns)) {
		return nil, errBadKey
	}
	if _, err := conn.Write(proof(a.key, labelServer, tr, nc, ns)); err != nil {
		return nil, err
	}
	return &authConn{Conn: conn, r: r}, nil
}

// connect completes the handshake on the dialling side and checks the server.
func (a peerAuth) connect(conn *tls.Conn) (*authConn, error) {
	peerCert, err := handshake(conn)
	if err != nil {
		return nil, err
	}
	tr := transcript(peerCert, a.cert.Certificate[0], a.offerID)
	r := bufio.NewReader(conn)

	nc, err := newNonce()
	if err != nil {
		return nil, err
	}
	if _, err := conn.Write(nc); err != nil {
		return nil, err
	}
	ns := make([]byte, nonceLen)
	if _, err := io.ReadFull(r, ns); err != nil {
		return nil, err
	}

	if _, err := conn.Write(proof(a.key, labelClient, tr, nc, ns)); err != nil {
		return nil, err
	}
	got := make([]byte, sha256.Size)
	if _, err := io.ReadFull(r, got); err != nil {
		// The server closes the connection when our proof is wrong, which is
		// the usual reason this read fails.
		return nil, errBadKey
	}
	if !hmac.Equal(got, proof(a.key, labelServer, tr, nc, ns)) {
		return nil, errBadKey
	}
	return &authConn{Conn: conn, r: r}, nil
}

// handshake runs the TLS handshake and returns the peer's certificate bytes.
func handshake(conn *tls.Conn) ([]byte, error) {
	if err := conn.Handshake(); err != nil {
		return nil, fmt.Errorf("tls handshake: %w", err)
	}
	certs := conn.ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return nil, errors.New("the peer sent no certificate")
	}
	return certs[0].Raw, nil
}

// transcript binds a proof to this TLS session and to this offer. A machine in
// the middle terminates TLS with its own certificates, so its transcript
// differs and its proof does not verify.
func transcript(serverCert, clientCert []byte, offerID string) []byte {
	s := sha256.Sum256(serverCert)
	c := sha256.Sum256(clientCert)
	out := make([]byte, 0, len(s)+len(c)+len(offerID))
	out = append(out, s[:]...)
	out = append(out, c[:]...)
	return append(out, offerID...)
}

func proof(key []byte, label string, tr, nc, ns []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(label)) // labelClient and labelServer are the same length
	m.Write(tr)
	m.Write(nc)
	m.Write(ns)
	return m.Sum(nil)
}

func newNonce() ([]byte, error) {
	n := make([]byte, nonceLen)
	_, err := rand.Read(n)
	return n, err
}

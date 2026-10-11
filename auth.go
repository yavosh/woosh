package main

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"errors"
	"fmt"
	"time"
)

const (
	authVersion  = 1
	trustTimeout = 5 * time.Minute
)

var errWrongOffer = errors.New("the peer selected a different offer")

// Read through r, not through Conn, so no buffered byte is lost.
type authConn struct {
	*tls.Conn
	r    *bufio.Reader
	peer peerIdentity
}

type peerAuth struct {
	cert             tls.Certificate
	name             string
	localFingerprint string
	offerID          string
	ctx              context.Context
	verify           func(context.Context, peerIdentity) error
}

func newPeerAuth(d deviceIdentity, verify func(context.Context, peerIdentity) error) (peerAuth, error) {
	if !validDeviceName(d.Name) || len(d.Seed) != ed25519.SeedSize || verify == nil {
		return peerAuth{}, errors.New("invalid local identity or trust configuration")
	}
	cert, err := newIdentityCert(d)
	if err != nil {
		return peerAuth{}, err
	}
	return peerAuth{cert: cert, name: d.Name, localFingerprint: fingerprint(d.publicKey()), ctx: context.Background(), verify: verify}, nil
}

func loadPeerAuth(ctx context.Context) (peerAuth, error) {
	d, err := loadIdentity("")
	if err != nil {
		return peerAuth{}, err
	}
	dir, err := configPath("known_peers")
	if err != nil {
		return peerAuth{}, err
	}
	store := peerTrust{dir: dir, confirm: confirmPeer}
	auth, err := newPeerAuth(d, store.verify)
	auth.ctx = ctx
	return auth, err
}

// TLS proves possession of both private keys. finish pins the public keys before application data flows.
func (a peerAuth) serverConfig() *tls.Config {
	return &tls.Config{Certificates: []tls.Certificate{a.cert}, MinVersion: tls.VersionTLS13, ClientAuth: tls.RequireAnyClientCert}
}

func (a peerAuth) clientConfig() *tls.Config {
	return &tls.Config{
		Certificates:       []tls.Certificate{a.cert},
		MinVersion:         tls.VersionTLS13,
		InsecureSkipVerify: true, // finish verifies the peer's pinned public key.
	}
}

type authHello struct {
	V  int    `json:"auth"`
	ID string `json:"offer"`
}

func (a peerAuth) accept(conn *tls.Conn) (*authConn, error)  { return a.finish(conn, false) }
func (a peerAuth) connect(conn *tls.Conn) (*authConn, error) { return a.finish(conn, true) }

func (a peerAuth) finish(conn *tls.Conn, client bool) (*authConn, error) {
	if err := conn.Handshake(); err != nil {
		return nil, fmt.Errorf("tls handshake: %w (both machines need a version supporting device trust)", err)
	}
	certs := conn.ConnectionState().PeerCertificates
	if len(certs) != 1 {
		return nil, errors.New("the peer must send one identity certificate")
	}
	cert := certs[0]
	key, ok := cert.PublicKey.(ed25519.PublicKey)
	if !ok || !validDeviceName(cert.Subject.CommonName) {
		return nil, errors.New("the peer sent an invalid device identity; upgrade both machines")
	}
	if err := cert.CheckSignature(cert.SignatureAlgorithm, cert.RawTBSCertificate, cert.Signature); err != nil {
		return nil, errors.New("the peer sent an invalid identity certificate signature")
	}
	peer := peerIdentity{Name: cert.Subject.CommonName, Key: key, Addr: conn.RemoteAddr().String()}
	r := bufio.NewReader(conn)
	hello := authHello{V: authVersion, ID: a.offerID}
	var got authHello
	// The client writes first. Sending the offer ID on this TLS session binds it to these identities.
	if client {
		if err := writeJSONLine(conn, hello); err != nil {
			return nil, err
		}
	}
	if err := readJSONLine(r, &got); err != nil {
		return nil, err
	}
	if !client {
		if err := writeJSONLine(conn, hello); err != nil {
			return nil, err
		}
	}
	if got.V != authVersion {
		return nil, errors.New("peer authentication version mismatch; upgrade both machines")
	}
	if got.ID != a.offerID {
		return nil, errWrongOffer
	}
	if err := conn.SetDeadline(time.Now().Add(trustTimeout)); err != nil {
		return nil, err
	}
	ctx, timeout := context.WithTimeout(a.ctx, trustTimeout)
	defer timeout()
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	approved := make(chan error, 1)
	go func() {
		var approval reply
		err := readJSONLine(r, &approval)
		if err != nil {
			err = fmt.Errorf("peer trust was not confirmed: %w", err)
		} else if !approval.Accept {
			err = errors.New("the other device did not trust this device")
		}
		approved <- err
		if err != nil {
			cancel(err) // A disconnected or declining peer must not leave a prompt open.
		}
	}()
	err := a.verify(ctx, peer)
	if err != nil {
		writeJSONLine(conn, reply{Accept: false})
		if ctx.Err() != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
			return nil, context.Cause(ctx)
		}
		return nil, err
	}
	if err := writeJSONLine(conn, reply{Accept: true}); err != nil {
		return nil, err
	}
	select {
	case err := <-approved:
		if err != nil {
			return nil, err
		}
	case <-ctx.Done():
		return nil, context.Cause(ctx)
	}
	return &authConn{Conn: conn, r: r, peer: peer}, nil
}

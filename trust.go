package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

var (
	errPeerKeyChanged = errors.New("the device's public key has changed")
	errPeerDeclined   = errors.New("peer trust was declined")
)

type peerIdentity struct {
	Name string            `json:"name"`
	Key  ed25519.PublicKey `json:"key"`
	Addr string            `json:"-"`
}

type peerConfirmation func(context.Context, peerIdentity) (bool, error)
type peerTrust struct {
	dir     string
	confirm peerConfirmation
}

func (s peerTrust) path(name string) string {
	sum := sha256.Sum256([]byte(name))
	return filepath.Join(s.dir, hex.EncodeToString(sum[:])+".json")
}

// check returns true only for an exact match. An existing name never accepts a replacement key.
func (s peerTrust) check(peer peerIdentity) (bool, error) {
	b, err := readPrivateFile(s.path(peer.Name))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var known peerIdentity
	if err := json.Unmarshal(b, &known); err != nil || known.Name != peer.Name || len(known.Key) != ed25519.PublicKeySize {
		return false, errors.New("invalid known-peer file; refusing to replace it")
	}
	if !bytes.Equal(known.Key, peer.Key) {
		return false, fmt.Errorf("%w for %q; received %s; saved trust is at %s", errPeerKeyChanged, peer.Name, fingerprint(peer.Key), shortPath(s.path(peer.Name)))
	}
	return true, nil
}

func (s peerTrust) verify(ctx context.Context, peer peerIdentity) error {
	if !validDeviceName(peer.Name) || len(peer.Key) != ed25519.PublicKeySize {
		return errors.New("invalid peer identity")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	known, err := s.check(peer)
	if err != nil || known {
		return err
	}
	if s.confirm == nil {
		return errors.New("unknown peer; run this transfer in a terminal to confirm its public key")
	}
	accepted, err := s.confirm(ctx, peer)
	if err != nil {
		return err
	}
	if !accepted {
		return errPeerDeclined
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	b, err := json.Marshal(peer)
	if err != nil {
		return err
	}
	if err := createPrivateFile(s.path(peer.Name), b); err != nil {
		if errors.Is(err, os.ErrExist) {
			known, err = s.check(peer)
			if err == nil && !known {
				return errors.New("saved peer trust disappeared; retry the transfer")
			}
		}
		return err
	}
	return nil
}

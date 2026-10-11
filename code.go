package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// offerIDLen is the length in hex characters of the identifier in an announce packet.
const offerIDLen = 12

// newCode makes a transfer code such as "7-canyon-otter".
func newCode() (string, error) {
	var b [3]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	// The code selects an offer, it is not a secret. Saved peer identities give the
	// security, so a small modulo bias on the digit does no harm.
	return fmt.Sprintf("%d-%s-%s", b[0]%10, words[b[1]], words[b[2]]), nil
}

// normalizeCode puts a code that a user typed into its canonical form.
func normalizeCode(code string) (string, error) {
	code = strings.ToLower(strings.TrimSpace(code))
	if code == "" {
		return "", errors.New("empty code")
	}
	return code, nil
}

// offerID derives the public identifier that a sender puts in its announce
// packets. Anything on the network can read an announce, so it carries this
// hash and not the code itself.
func offerID(code string) string {
	sum := sha256.Sum256([]byte("woosh-offer-v1|" + code))
	return hex.EncodeToString(sum[:])[:offerIDLen]
}

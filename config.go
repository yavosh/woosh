package main

import (
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// keyLen is the size of the shared network key in bytes.
const keyLen = 32

// keyPrefix marks an encoded key so a user can recognise it in a paste buffer.
const keyPrefix = "WSH-"

// keyEnv overrides the key file. It exists for tests and for throwaway networks.
const keyEnv = "WOOSH_KEY"

var keyEnc = base32.StdEncoding.WithPadding(base32.NoPadding)

var errNoKey = errors.New("no network key: run \"woosh init\" first")

// keyPath returns the location of the network key file.
func keyPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "woosh", "key"), nil
}

// newKey makes a random network key.
func newKey() ([]byte, error) {
	k := make([]byte, keyLen)
	if _, err := rand.Read(k); err != nil {
		return nil, err
	}
	return k, nil
}

// encodeKey writes a key as dash-separated groups, for example WSH-A3F9-C1D2-...
func encodeKey(k []byte) string {
	s := keyEnc.EncodeToString(k)
	var b strings.Builder
	b.WriteString(keyPrefix)
	for i := 0; i < len(s); i += 4 {
		if i > 0 {
			b.WriteByte('-')
		}
		b.WriteString(s[i:min(i+4, len(s))])
	}
	return b.String()
}

// decodeKey reads a key written by encodeKey. It ignores case, dashes and spaces.
func decodeKey(s string) ([]byte, error) {
	s = strings.Map(func(r rune) rune {
		if r == '-' || r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			return -1
		}
		return r
	}, s)
	s = strings.ToUpper(s)
	s = strings.TrimPrefix(s, strings.TrimSuffix(keyPrefix, "-"))

	k, err := keyEnc.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("bad network key: %w", err)
	}
	if len(k) != keyLen {
		return nil, fmt.Errorf("bad network key: got %d bytes, want %d", len(k), keyLen)
	}
	return k, nil
}

// saveKey writes the key to the config file. Only the owner can read it.
func saveKey(k []byte) (string, error) {
	path, err := keyPath()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(encodeKey(k)+"\n"), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// loadKey reads the network key from WOOSH_KEY, or from the config file.
func loadKey() ([]byte, error) {
	if s := os.Getenv(keyEnv); s != "" {
		return decodeKey(s)
	}
	path, err := keyPath()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, errNoKey
	}
	if err != nil {
		return nil, err
	}
	return decodeKey(strings.TrimSpace(string(b)))
}

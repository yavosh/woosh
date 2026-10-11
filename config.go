package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

type deviceIdentity struct {
	Name string `json:"name"`
	Seed []byte `json:"seed"`
}

func configPath(name string) (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "woosh", name), nil
}

// shortPath hides the home directory in messages.
func shortPath(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	rest, ok := strings.CutPrefix(path, home+string(filepath.Separator))
	if !ok {
		return path
	}
	return "~" + string(filepath.Separator) + rest
}

func validDeviceName(name string) bool {
	if len(name) == 0 || len(name) > 255 {
		return false
	}
	for _, c := range name {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '-' || c == '_') {
			return false
		}
	}
	return name != "." && name != ".."
}

func (d deviceIdentity) publicKey() ed25519.PublicKey {
	return ed25519.NewKeyFromSeed(d.Seed).Public().(ed25519.PublicKey)
}

func fingerprint(key ed25519.PublicKey) string {
	sum := sha256.Sum256(key)
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:])
}

// loadIdentity creates an identity only when none exists. A concurrent creator wins safely.
func loadIdentity(name string) (deviceIdentity, error) {
	path, err := configPath("identity")
	if err != nil {
		return deviceIdentity{}, err
	}
	d, err := readIdentity(path)
	if err == nil {
		if name != "" && name != d.Name {
			return deviceIdentity{}, fmt.Errorf("this device is already named %q", d.Name)
		}
		return d, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return deviceIdentity{}, err
	}
	requestedName := name
	if name == "" {
		name, err = os.Hostname()
		if err != nil {
			return deviceIdentity{}, err
		}
	}
	if !validDeviceName(name) {
		return deviceIdentity{}, errors.New("device names need 1–255 letters, digits, dots, hyphens, or underscores; use woosh init --name NAME")
	}
	seed := make([]byte, ed25519.SeedSize)
	if _, err := rand.Read(seed); err != nil {
		return deviceIdentity{}, err
	}
	d = deviceIdentity{Name: name, Seed: seed}
	b, err := json.Marshal(d)
	if err != nil {
		return deviceIdentity{}, err
	}
	if err := createPrivateFile(path, b); err != nil {
		if errors.Is(err, os.ErrExist) {
			return loadIdentity(requestedName)
		}
		return deviceIdentity{}, err
	}
	return d, nil
}

func readIdentity(path string) (deviceIdentity, error) {
	b, err := readPrivateFile(path)
	if err != nil {
		return deviceIdentity{}, err
	}
	var d deviceIdentity
	if err := json.Unmarshal(b, &d); err != nil || !validDeviceName(d.Name) || len(d.Seed) != ed25519.SeedSize {
		return deviceIdentity{}, errors.New("invalid device identity; refusing to replace it")
	}
	return d, nil
}

// Private files must be regular files. Unix files must have no group or other permissions.
func readPrivateFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s must be a regular file", shortPath(path))
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%s must have private permissions (chmod 600)", shortPath(path))
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(info, opened) || !opened.Mode().IsRegular() || runtime.GOOS != "windows" && opened.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("configuration file changed while opening it")
	}
	const maxConfigSize = 4096
	b, err := io.ReadAll(io.LimitReader(f, maxConfigSize+1))
	if err == nil && len(b) > maxConfigSize {
		return nil, errors.New("configuration file is too large")
	}
	return b, err
}

// createPrivateFile publishes a complete, private file without replacing any existing path.
func createPrivateFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".woosh-config-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Link(f.Name(), path)
}

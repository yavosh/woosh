package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestKeyRoundTrip(t *testing.T) {
	k, err := newKey()
	if err != nil {
		t.Fatal(err)
	}
	if len(k) != keyLen {
		t.Fatalf("newKey gave %d bytes, want %d", len(k), keyLen)
	}

	got, err := decodeKey(encodeKey(k))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, k) {
		t.Fatalf("round trip changed the key")
	}
}

func TestDecodeKeyIsForgiving(t *testing.T) {
	k, err := newKey()
	if err != nil {
		t.Fatal(err)
	}
	enc := encodeKey(k)

	variants := map[string]string{
		"canonical": enc,
		"lowercase": strings.ToLower(enc),
		"no dashes": strings.ReplaceAll(enc, "-", ""),
		"spaced":    strings.ReplaceAll(enc, "-", " "),
		"padded":    "  " + enc + "\n",
	}
	for name, v := range variants {
		got, err := decodeKey(v)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if !bytes.Equal(got, k) {
			t.Errorf("%s: decoded to a different key", name)
		}
	}
}

func TestDecodeKeyRejectsBadInput(t *testing.T) {
	for _, s := range []string{"", "WSH-", "not a key!", encodeKey(make([]byte, 8))} {
		if _, err := decodeKey(s); err == nil {
			t.Errorf("decodeKey(%q) accepted a bad key", s)
		}
	}
}

func TestSaveKeyPermissions(t *testing.T) {
	// os.UserConfigDir reads XDG_CONFIG_HOME on unix and $HOME/Library on macOS.
	// Set both so the test never touches the real config directory.
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv(keyEnv, "")

	path, err := keyPath()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(path, dir) {
		t.Fatalf("keyPath is %s, outside the temp home %s", path, dir)
	}

	k, err := newKey()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := saveKey(k); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("key file mode is %o, want 600", perm)
	}
	if perm := dirPerm(t, filepath.Dir(path)); perm != 0o700 {
		t.Errorf("key directory mode is %o, want 700", perm)
	}

	got, err := loadKey()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, k) {
		t.Error("loadKey gave a different key")
	}
}

func TestLoadKeyFromEnv(t *testing.T) {
	k, err := newKey()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(keyEnv, encodeKey(k))

	got, err := loadKey()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, k) {
		t.Error("WOOSH_KEY was not used")
	}
}

func dirPerm(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}

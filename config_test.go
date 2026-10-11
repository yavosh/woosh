package main

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// TestShortPathHidesTheHome covers what init prints. The path comes from the
// operating system and holds the user name.
func TestShortPathHidesTheHome(t *testing.T) {
	home := tempHome(t)

	under := filepath.Join(home, "somewhere", "woosh", "key")
	want := filepath.Join("~", "somewhere", "woosh", "key")
	if got := shortPath(under); got != want {
		t.Errorf("shortPath(%q) = %q, want %q", under, got, want)
	}

	// A path outside the home directory stays as it is.
	outside := filepath.Join(string(filepath.Separator), "etc", "woosh", "key")
	if got := shortPath(outside); got != outside {
		t.Errorf("shortPath(%q) = %q, want it unchanged", outside, got)
	}
}

func TestIdentityPermissions(t *testing.T) {
	home := tempHome(t)
	identity, err := loadIdentity("device")
	if err != nil {
		t.Fatal(err)
	}
	path, err := configPath("identity")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(path, home) {
		t.Fatal("identity escaped the isolated home")
	}
	if runtime.GOOS != "windows" {
		if dirPerm(t, path) != 0o600 || dirPerm(t, filepath.Dir(path)) != 0o700 {
			t.Fatal("identity permissions are not private")
		}
	}
	again, err := readIdentity(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(identity.Seed, again.Seed) {
		t.Fatal("identity changed on disk")
	}
}

func TestIdentityConcurrentCreation(t *testing.T) {
	tempHome(t)
	const count = 12
	identities := make(chan deviceIdentity, count)
	failures := make(chan error, count)
	var wg sync.WaitGroup
	for range count {
		wg.Go(func() {
			identity, err := loadIdentity("device")
			if err != nil {
				failures <- err
				return
			}
			identities <- identity
		})
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}
	close(identities)
	var first []byte
	for identity := range identities {
		if first == nil {
			first = identity.Seed
		}
		if !bytes.Equal(first, identity.Seed) {
			t.Fatal("concurrent creation replaced the device key")
		}
	}
}

func TestIdentityRejectsMalformedFiles(t *testing.T) {
	for _, value := range []string{"broken", `{"name":"device","seed":"AA=="}`, `{"name":"../escape","seed":"AA=="}`} {
		t.Run(value, func(t *testing.T) {
			tempHome(t)
			path, err := configPath("identity")
			if err != nil {
				t.Fatal(err)
			}
			writeFile(t, path, value, 0o600)
			if _, err := loadIdentity(""); err == nil {
				t.Fatal("malformed identity was replaced")
			}
			requireContent(t, path, value)
		})
	}
}

func TestIdentityLeavesLegacyKeyUntouched(t *testing.T) {
	tempHome(t)
	path, err := configPath("key")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, "synthetic legacy key", 0o600)
	if _, err := loadIdentity("new-device"); err != nil {
		t.Fatal(err)
	}
	requireContent(t, path, "synthetic legacy key")
}

func TestDeviceNames(t *testing.T) {
	for _, name := range []string{"", ".", "..", "bad/name", "bad\\name", "bad\nname", "bad name", strings.Repeat("a", 256)} {
		if validDeviceName(name) {
			t.Errorf("accepted invalid name %q", name)
		}
	}
	if !validDeviceName("Device_1.local") {
		t.Fatal("rejected a valid device name")
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

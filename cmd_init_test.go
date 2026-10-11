package main

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
)

// tempHome points the config directory at a temporary place, so a test never
// touches real device identities.
func tempHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("APPDATA", dir)
	return dir
}

func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w

	out := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		out <- string(b)
	}()

	runErr := fn()
	w.Close()
	os.Stdout = old
	return <-out, runErr
}

func TestInitCreatesAndReusesIdentity(t *testing.T) {
	tempHome(t)
	firstOutput, err := captureStdout(t, func() error { return cmdInit([]string{"--name", "laptop"}) })
	if err != nil {
		t.Fatal(err)
	}
	first, err := loadIdentity("")
	if err != nil {
		t.Fatal(err)
	}
	againOutput, err := captureStdout(t, func() error { return cmdInit(nil) })
	if err != nil {
		t.Fatal(err)
	}
	again, err := loadIdentity("")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.Seed, again.Seed) || firstOutput != againOutput {
		t.Fatal("init replaced an existing identity")
	}
	if !strings.Contains(firstOutput, fingerprint(first.publicKey())) {
		t.Fatal("init did not print the public fingerprint")
	}
	if strings.Contains(firstOutput, "seed") {
		t.Fatal("init printed private identity data")
	}
	if err := cmdInit([]string{"--name", "other"}); err == nil {
		t.Fatal("init silently renamed an existing identity")
	}
}

func TestInitRejectsInvalidArguments(t *testing.T) {
	tempHome(t)
	for _, args := range [][]string{{"--name", "bad/name"}, {"extra"}, {"--key", "obsolete"}, {"--raw"}, {"--passphrase"}, {"--force"}} {
		if err := cmdInit(args); err == nil {
			t.Errorf("accepted invalid arguments %v", args)
		}
	}
	path, err := configPath("identity")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("invalid arguments created an identity")
	}
}

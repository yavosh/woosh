package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// tempHome points the config directory at a temporary place, so a test never
// touches the real network key.
func tempHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv(keyEnv, "")
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

func TestInitWritesABase32Key(t *testing.T) {
	tempHome(t)

	printed, err := captureStdout(t, func() error { return cmdInit(nil) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(printed, keyPrefix) {
		t.Errorf("the base32 key was not shown:\n%s", printed)
	}

	key, err := loadKey()
	if err != nil {
		t.Fatal(err)
	}
	if len(key) != keyLen {
		t.Errorf("stored key is %d bytes, want %d", len(key), keyLen)
	}
}

// TestInitWordsReachTheSameKey is the point of the feature: read the words off
// one machine, type them on another, and both end up on the same key.
func TestInitWordsReachTheSameKey(t *testing.T) {
	tempHome(t)

	printed, err := captureStdout(t, func() error { return cmdInit([]string{"--words"}) })
	if err != nil {
		t.Fatal(err)
	}

	m := regexp.MustCompile(`--key "([a-z ]+)"`).FindStringSubmatch(printed)
	if m == nil {
		t.Fatalf("no word list in the output:\n%s", printed)
	}
	words := strings.Fields(m[1])
	if len(words) != mnemonicWords {
		t.Fatalf("showed %d words, want %d", len(words), mnemonicWords)
	}

	first, err := loadKey()
	if err != nil {
		t.Fatal(err)
	}

	// A second machine, typing what the first one showed.
	tempHome(t)
	if _, err := captureStdout(t, func() error { return cmdInit([]string{"--key", m[1]}) }); err != nil {
		t.Fatal(err)
	}
	second, err := loadKey()
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(first, second) {
		t.Error("the two machines did not reach the same key")
	}
}

func TestInitAdoptsABase32Key(t *testing.T) {
	tempHome(t)

	want, err := newKey()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := captureStdout(t, func() error { return cmdInit([]string{"--key", encodeKey(want)}) }); err != nil {
		t.Fatal(err)
	}

	got, err := loadKey()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Error("the adopted key is not the one that was given")
	}
}

func TestInitRejectsTwoModes(t *testing.T) {
	tempHome(t)

	pairs := [][]string{
		{"--words", "--passphrase"},
		{"--words", "--key", "abandon ability able about above absent absorb abstract"},
		{"--passphrase", "--key", "abandon ability able about above absent absorb abstract"},
	}
	for _, args := range pairs {
		if err := cmdInit(args); err == nil {
			t.Errorf("%v: two modes were accepted", args)
		}
	}
}

func TestInitRefusesToReplaceWithoutForce(t *testing.T) {
	home := tempHome(t)

	if _, err := captureStdout(t, func() error { return cmdInit(nil) }); err != nil {
		t.Fatal(err)
	}
	first, err := loadKey()
	if err != nil {
		t.Fatal(err)
	}

	if err := cmdInit(nil); err == nil {
		t.Error("init replaced an existing key without --force")
	}
	again, err := loadKey()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, again) {
		t.Fatal("the key changed even though init reported an error")
	}

	if _, err := captureStdout(t, func() error { return cmdInit([]string{"--force"}) }); err != nil {
		t.Fatal(err)
	}
	forced, err := loadKey()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(first, forced) {
		t.Error("--force did not make a new key")
	}

	// Sanity check that the test really worked inside the temporary home.
	if _, err := os.Stat(filepath.Join(home)); err != nil {
		t.Fatal(err)
	}
}

func TestInitRejectsABadWordList(t *testing.T) {
	tempHome(t)

	if err := cmdInit([]string{"--key", "abandon ability notarealword about above absent absorb abstract"}); err == nil {
		t.Error("a misspelled word list was accepted")
	}
	if _, err := loadKey(); err == nil {
		t.Error("a key was written even though init failed")
	}
}

func TestReadPassphraseFromAPipe(t *testing.T) {
	// Not a terminal, so readPassphrase takes one line and asks nothing.
	path := filepath.Join(t.TempDir(), "phrase")
	if err := os.WriteFile(path, []byte("correct horse battery staple\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	var prompts bytes.Buffer
	got, err := readPassphrase(f, &prompts)
	if err != nil {
		t.Fatal(err)
	}
	if got != "correct horse battery staple" {
		t.Errorf("got %q", got)
	}
	if prompts.Len() != 0 {
		t.Errorf("it prompted a script: %q", prompts.String())
	}
}

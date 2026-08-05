package main

import (
	"bytes"
	"regexp"
	"strings"
	"testing"
)

func TestWordListIsIntact(t *testing.T) {
	if len(words39) != 1<<mnemonicBits {
		t.Fatalf("the word list holds %d words, want %d", len(words39), 1<<mnemonicBits)
	}
	if len(index39) != len(words39) {
		t.Errorf("the word list has duplicates: %d words, %d distinct", len(words39), len(index39))
	}

	shape := regexp.MustCompile(`^[a-z]{3,8}$`)
	prefixes := make(map[string]string, len(words39))
	for i, w := range words39 {
		if !shape.MatchString(w) {
			t.Errorf("word %d %q is not 3-8 lowercase letters", i, w)
		}
		// The four letter prefixes are unique in this list. That property is
		// what makes a mistyped word easy to catch, so guard it.
		p := w
		if len(p) > 4 {
			p = p[:4]
		}
		if other, dup := prefixes[p]; dup {
			t.Errorf("%q and %q share the prefix %q", other, w, p)
		}
		prefixes[p] = w
	}
}

func TestMnemonicRoundTrip(t *testing.T) {
	for range 200 {
		words, err := newMnemonic()
		if err != nil {
			t.Fatal(err)
		}
		if len(words) != mnemonicWords {
			t.Fatalf("got %d words, want %d", len(words), mnemonicWords)
		}

		ent, err := entropyFromWords(words)
		if err != nil {
			t.Fatal(err)
		}
		if len(ent) != mnemonicBytes {
			t.Fatalf("got %d entropy bytes, want %d", len(ent), mnemonicBytes)
		}
		if got := wordsFromEntropy(ent); !equalWords(got, words) {
			t.Fatalf("round trip changed the words:\n got %v\nwant %v", got, words)
		}
	}
}

func TestMnemonicCoversTheWholeRange(t *testing.T) {
	// Every bit pattern must map back to itself, including the extremes.
	for _, ent := range [][]byte{
		bytes.Repeat([]byte{0x00}, mnemonicBytes),
		bytes.Repeat([]byte{0xff}, mnemonicBytes),
		{0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef, 0x00, 0x11, 0x22},
	} {
		words := wordsFromEntropy(ent)
		got, err := entropyFromWords(words)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, ent) {
			t.Errorf("round trip of % x gave % x via %v", ent, got, words)
		}
	}
}

func TestKeyFromWordsIsDeterministic(t *testing.T) {
	words, err := newMnemonic()
	if err != nil {
		t.Fatal(err)
	}

	first, err := keyFromWords(words)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != keyLen {
		t.Fatalf("key is %d bytes, want %d", len(first), keyLen)
	}

	// The same words on another machine must give the same key, whatever the
	// spacing and the case.
	messy := strings.Fields(strings.ToUpper("  " + strings.Join(words, "   ") + "  "))
	again, err := keyFromWords(messy)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, again) {
		t.Error("the same words gave two different keys")
	}

	// One word different must give a different key.
	changed := append([]string(nil), words...)
	changed[0] = words39[(index39[changed[0]]+1)%len(words39)]
	other, err := keyFromWords(changed)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(first, other) {
		t.Error("changing a word did not change the key")
	}
}

func TestKeyFromWordsRejectsBadInput(t *testing.T) {
	good, err := newMnemonic()
	if err != nil {
		t.Fatal(err)
	}

	short := good[:mnemonicWords-1]
	if _, err := keyFromWords(short); err == nil {
		t.Error("a short word list was accepted")
	}
	if _, err := keyFromWords(append(append([]string(nil), good...), "abandon")); err == nil {
		t.Error("a long word list was accepted")
	}

	typo := append([]string(nil), good...)
	typo[3] = "notarealword"
	_, err = keyFromWords(typo)
	if err == nil {
		t.Fatal("a misspelled word was accepted")
	}
	if !strings.Contains(err.Error(), "notarealword") {
		t.Errorf("the error does not name the bad word: %v", err)
	}
}

// testIters keeps the passphrase tests quick. The cost of the real count is
// not what these check, TestKeyFromPassphraseUsesTheRealCost covers that.
const testIters = 2000

func TestDerivePassphrase(t *testing.T) {
	const phrase = "correct horse battery staple"

	first, err := derivePassphrase(phrase, testIters)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != keyLen {
		t.Fatalf("key is %d bytes, want %d", len(first), keyLen)
	}

	// The whole point: the same phrase on another machine gives the same key.
	again, err := derivePassphrase("  "+phrase+"\n", testIters)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, again) {
		t.Error("the same passphrase gave two different keys")
	}

	other, err := derivePassphrase(phrase+"!", testIters)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(first, other) {
		t.Error("a different passphrase gave the same key")
	}

	// A different cost must give a different key, or the count would not be
	// doing anything.
	cheap, err := derivePassphrase(phrase, testIters+1)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(first, cheap) {
		t.Error("the iteration count does not reach the key")
	}
}

func TestDerivePassphraseRejectsShortInput(t *testing.T) {
	for _, s := range []string{"", "short", strings.Repeat("a", passMinLen-1)} {
		if _, err := derivePassphrase(s, testIters); err == nil {
			t.Errorf("passphrase %q was accepted", s)
		}
	}
	if _, err := derivePassphrase(strings.Repeat("a", passMinLen), testIters); err != nil {
		t.Errorf("a passphrase of exactly %d characters was rejected: %v", passMinLen, err)
	}
}

func TestPassItersIsHighEnough(t *testing.T) {
	// The salt is fixed, so the count is the only thing that makes a guess
	// expensive. OWASP asks for 600000 with PBKDF2-HMAC-SHA256.
	if passIters < 600_000 {
		t.Errorf("passIters is %d, below the advised 600000", passIters)
	}
}

// TestKeyFromPassphraseUsesTheRealCost runs the real derivation once. Four
// million rounds under -race take a few seconds, so -short leaves it out.
func TestKeyFromPassphraseUsesTheRealCost(t *testing.T) {
	if testing.Short() {
		t.Skip("the real iteration count is slow, run without -short")
	}

	key, err := keyFromPassphrase("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if len(key) != keyLen {
		t.Errorf("key is %d bytes, want %d", len(key), keyLen)
	}
}

func TestParseKeyInputTakesBothForms(t *testing.T) {
	words, err := newMnemonic()
	if err != nil {
		t.Fatal(err)
	}
	wantWords, err := keyFromWords(words)
	if err != nil {
		t.Fatal(err)
	}

	got, err := parseKeyInput(strings.Join(words, " "))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, wantWords) {
		t.Error("the word list did not come back as the same key")
	}

	raw, err := newKey()
	if err != nil {
		t.Fatal(err)
	}
	got, err = parseKeyInput(encodeKey(raw))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, raw) {
		t.Error("the base32 key did not come back as the same key")
	}

	// A word list with the wrong count must complain about words, not base32.
	_, err = parseKeyInput("abandon ability able")
	if err == nil || !strings.Contains(err.Error(), "word list") {
		t.Errorf("error is %v, want one about the word list", err)
	}
}

func TestFormatMnemonic(t *testing.T) {
	words := []string{"a", "b", "c", "d", "e", "f", "g", "h"}
	got := formatMnemonic(words)
	want := "  a b c d\n  e f g h\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func equalWords(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

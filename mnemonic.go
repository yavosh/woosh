package main

import (
	"crypto/hkdf"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	_ "embed"
	"fmt"
	"strings"
)

// bip39List is the BIP-39 English word list. Every word is unique in its first
// four letters, which is what makes a mistyped word easy to catch.
//
//go:embed bip39.txt
var bip39List string

const (
	// mnemonicWords is how many words carry a key. The list holds 2048 words,
	// so a word is 11 bits and 8 words are 88 bits.
	mnemonicWords = 8
	mnemonicBits  = 11
	mnemonicBytes = mnemonicWords * mnemonicBits / 8 // 88 bits, no remainder

	// passIters stretches a passphrase. The salt is a constant, because two
	// machines have to reach the same key with nothing between them but the
	// phrase. That leaves the iteration count as the only thing that makes a
	// guess expensive, so it is well above the usual advice. It costs about
	// 0.3s on an M series mac, once, at init.
	passIters = 4_000_000

	// passMinLen is the shortest passphrase woosh accepts.
	passMinLen = 12
)

var (
	words39 = strings.Fields(bip39List)
	index39 = wordIndex(words39)
)

func wordIndex(ws []string) map[string]int {
	m := make(map[string]int, len(ws))
	for i, w := range ws {
		m[w] = i
	}
	return m
}

// newMnemonic draws 88 random bits and writes them as words.
func newMnemonic() ([]string, error) {
	b := make([]byte, mnemonicBytes)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	return wordsFromEntropy(b), nil
}

// wordsFromEntropy reads the bytes 11 bits at a time, one word per group.
func wordsFromEntropy(b []byte) []string {
	out := make([]string, 0, mnemonicWords)
	var acc, bits uint32
	for _, by := range b {
		acc = acc<<8 | uint32(by)
		bits += 8
		for bits >= mnemonicBits {
			bits -= mnemonicBits
			out = append(out, words39[(acc>>bits)&(1<<mnemonicBits-1)])
		}
	}
	return out
}

// entropyFromWords is the inverse of wordsFromEntropy.
func entropyFromWords(ws []string) ([]byte, error) {
	if len(ws) != mnemonicWords {
		return nil, fmt.Errorf("a woosh word list is %d words, this one is %d", mnemonicWords, len(ws))
	}
	out := make([]byte, 0, mnemonicBytes)
	var acc, bits uint32
	for _, w := range ws {
		i, ok := index39[w]
		if !ok {
			return nil, fmt.Errorf("%q is not a woosh word, check the spelling", w)
		}
		acc = acc<<mnemonicBits | uint32(i)
		bits += mnemonicBits
		for bits >= 8 {
			bits -= 8
			out = append(out, byte(acc>>bits))
		}
	}
	return out, nil
}

// keyFromWords turns a word list into the network key. The words already carry
// 88 random bits, so HKDF is the right tool: there is no weak input to stretch.
func keyFromWords(ws []string) ([]byte, error) {
	ent, err := entropyFromWords(normalizeWords(ws))
	if err != nil {
		return nil, err
	}
	return hkdf.Key(sha256.New, ent, []byte("woosh-words-v1"), "network key", keyLen)
}

// keyFromPassphrase stretches a phrase into the network key. See passIters for
// why the salt is fixed and the count is high.
func keyFromPassphrase(phrase string) ([]byte, error) {
	return derivePassphrase(phrase, passIters)
}

// derivePassphrase is keyFromPassphrase with the cost as a parameter, so tests
// can check the behaviour without paying for four million rounds each time.
func derivePassphrase(phrase string, iters int) ([]byte, error) {
	phrase = strings.TrimSpace(phrase)
	if err := checkPassphrase(phrase); err != nil {
		return nil, err
	}
	return pbkdf2.Key(sha256.New, phrase, []byte("woosh-passphrase-v1"), iters, keyLen)
}

// checkPassphrase reports whether a phrase is long enough to be worth
// stretching. Callers use it before they say the derivation has started.
func checkPassphrase(phrase string) error {
	if n := len([]rune(strings.TrimSpace(phrase))); n < passMinLen {
		return fmt.Errorf("a passphrase needs %d characters or more, this one has %d", passMinLen, n)
	}
	return nil
}

func normalizeWords(ws []string) []string {
	out := make([]string, len(ws))
	for i, w := range ws {
		out[i] = strings.ToLower(strings.TrimSpace(w))
	}
	return out
}

// formatMnemonic lays the words out in rows of four, which is easier to read
// back to somebody than one long line.
func formatMnemonic(ws []string) string {
	var b strings.Builder
	for i := 0; i < len(ws); i += 4 {
		fmt.Fprintf(&b, "  %s\n", strings.Join(ws[i:min(i+4, len(ws))], " "))
	}
	return b.String()
}

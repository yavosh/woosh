package main

import (
	"regexp"
	"testing"
)

func TestWordsList(t *testing.T) {
	seen := make(map[string]int, len(words))
	for i, w := range words {
		if w == "" {
			t.Fatalf("word %d is empty", i)
		}
		if prev, dup := seen[w]; dup {
			t.Errorf("word %q is at both %d and %d", w, prev, i)
		}
		seen[w] = i
		if !regexp.MustCompile(`^[a-z]{3,9}$`).MatchString(w) {
			t.Errorf("word %d %q is not 3-9 lowercase letters", i, w)
		}
	}
}

func TestNewCodeFormat(t *testing.T) {
	re := regexp.MustCompile(`^[0-9]-[a-z]+-[a-z]+$`)
	seen := make(map[string]bool)
	for range 200 {
		code, err := newCode()
		if err != nil {
			t.Fatal(err)
		}
		if !re.MatchString(code) {
			t.Fatalf("code %q does not match %s", code, re)
		}
		seen[code] = true
	}
	if len(seen) < 150 {
		t.Errorf("only %d distinct codes in 200 draws, generation looks biased", len(seen))
	}
}

func TestOfferIDIsStable(t *testing.T) {
	const code = "7-canyon-otter"
	want := offerID(code)
	if len(want) != offerIDLen {
		t.Fatalf("offerID is %d chars, want %d", len(want), offerIDLen)
	}
	if got := offerID(code); got != want {
		t.Errorf("offerID is not deterministic: %s then %s", want, got)
	}
	if got := offerID("7-canyon-ottet"); got == want {
		t.Error("a different code gave the same offer id")
	}
}

func TestNormalizeCode(t *testing.T) {
	cases := map[string]string{
		"7-canyon-otter":     "7-canyon-otter",
		"  7-Canyon-Otter\n": "7-canyon-otter",
		"7-CANYON-OTTER":     "7-canyon-otter",
	}
	for in, want := range cases {
		got, err := normalizeCode(in)
		if err != nil {
			t.Errorf("%q: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("normalizeCode(%q) = %q, want %q", in, got, want)
		}
	}
	if _, err := normalizeCode("   "); err == nil {
		t.Error("normalizeCode accepted an empty code")
	}
}

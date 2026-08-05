package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
)

func cmdInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	adopt := fs.String("key", "", "adopt a key you were given: the word list, or the base32 key")
	useRaw := fs.Bool("raw", false, "make a key and show it as base32")
	usePass := fs.Bool("passphrase", false, "derive the key from a passphrase you type on every machine")
	force := fs.Bool("force", false, "replace an existing key")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `usage: woosh init [--raw | --passphrase | --key KEY] [--force]

  woosh init               a random key, shown as 8 words to type elsewhere
  woosh init --raw         a random key, shown as base32
  woosh init --passphrase  the same passphrase on every machine, nothing to copy
  woosh init --key KEY     adopt a key, as words or as base32

`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}

	chosen := 0
	for _, on := range []bool{*adopt != "", *useRaw, *usePass} {
		if on {
			chosen++
		}
	}
	if chosen > 1 {
		return errors.New("choose one of --raw, --passphrase and --key")
	}

	path, err := keyPath()
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); err == nil && !*force {
		return fmt.Errorf("a network key is already at %s (use --force to replace it)", path)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}

	var key []byte
	var words []string

	switch {
	case *adopt != "":
		key, err = parseKeyInput(*adopt)

	case *usePass:
		key, err = initFromPassphrase()

	case *useRaw:
		key, err = newKey()

	default:
		if words, err = newMnemonic(); err == nil {
			key, err = keyFromWords(words)
		}
	}
	if err != nil {
		return err
	}

	if _, err := saveKey(key); err != nil {
		return err
	}
	fmt.Printf("network key saved to %s\n", path)

	switch {
	case *adopt != "":
		// The user already has the key. Saying it back adds nothing.
	case *usePass:
		fmt.Println("\nrun \"woosh init --passphrase\" on your other machines")
		fmt.Println("and type the same passphrase. There is nothing to copy.")
	case *useRaw:
		fmt.Printf("\n  %s\n\n", encodeKey(key))
		fmt.Println("run \"woosh init --key <that key>\" on your other machines.")
		fmt.Println("keep it secret: it is the only thing that guards a transfer.")
	default:
		fmt.Printf("\n%s\n", formatMnemonic(words))
		fmt.Println("type these on your other machines:")
		fmt.Printf("  woosh init --key \"%s\"\n", strings.Join(words, " "))
		fmt.Println("\nkeep them secret: they are the only thing that guards a transfer.")
	}
	return nil
}

func initFromPassphrase() ([]byte, error) {
	fmt.Fprintln(os.Stderr, "type the same passphrase on every machine in your woosh network.")
	fmt.Fprintf(os.Stderr, "it needs %d characters or more.\n\n", passMinLen)

	phrase, err := readPassphrase(os.Stdin, os.Stderr)
	if err != nil {
		return nil, err
	}
	// Check before announcing the work, so a phrase that is too short fails at
	// once instead of after the message.
	if err := checkPassphrase(phrase); err != nil {
		return nil, err
	}
	fmt.Fprintln(os.Stderr, "deriving the key, this takes a moment...")
	return keyFromPassphrase(phrase)
}

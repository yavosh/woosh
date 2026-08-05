package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
)

func cmdInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	adopt := fs.String("key", "", "adopt this network key instead of making a new one")
	force := fs.Bool("force", false, "replace an existing key")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), "usage: woosh init [--key KEY] [--force]\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
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
	if *adopt != "" {
		key, err = decodeKey(*adopt)
	} else {
		key, err = newKey()
	}
	if err != nil {
		return err
	}

	if _, err := saveKey(key); err != nil {
		return err
	}

	fmt.Printf("network key saved to %s\n", path)
	if *adopt == "" {
		fmt.Printf("\n  %s\n\n", encodeKey(key))
		fmt.Println("run \"woosh init --key <that key>\" on your other machines.")
		fmt.Println("keep it secret: it is the only thing that guards a transfer.")
	}
	return nil
}

package main

import (
	"errors"
	"flag"
	"fmt"
)

func cmdInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	name := fs.String("name", "", "device name, defaults to the hostname when creating an identity")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), "usage: woosh init [--name NAME]\n\ncreates a device identity or shows the existing public fingerprint.\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("init takes no positional arguments")
	}
	d, err := loadIdentity(*name)
	if err != nil {
		return err
	}
	path, err := configPath("identity")
	if err != nil {
		return err
	}
	fmt.Printf("device: %s\nfingerprint: %s\nidentity saved to %s\n", d.Name, fingerprint(d.publicKey()), shortPath(path))
	fmt.Println("confirm each device's fingerprint on first contact; trusted public keys are saved automatically.")
	return nil
}

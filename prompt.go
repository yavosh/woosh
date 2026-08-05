package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
)

// readPassphrase asks for a passphrase twice and checks the two answers agree,
// because a typo here means a key nobody else can reach. When stdin is not a
// terminal it reads one line and asks nothing, which is what a script wants.
func readPassphrase(in *os.File, out io.Writer) (string, error) {
	r := bufio.NewReader(in)

	if !isTerminal(in) {
		return readLine(r)
	}

	restore, err := echoOff(in)
	if err != nil {
		fmt.Fprintln(out, "warning: cannot turn the terminal echo off, the passphrase will show")
	} else {
		defer restore()
	}

	first, err := askLine(r, out, "passphrase: ")
	if err != nil {
		return "", err
	}
	again, err := askLine(r, out, "passphrase again: ")
	if err != nil {
		return "", err
	}
	if first != again {
		return "", errors.New("the two passphrases are different")
	}
	return first, nil
}

func askLine(r *bufio.Reader, out io.Writer, prompt string) (string, error) {
	fmt.Fprint(out, prompt)
	line, err := readLine(r)
	fmt.Fprintln(out) // the newline the user typed did not show
	return line, err
}

func readLine(r *bufio.Reader) (string, error) {
	line, err := r.ReadString('\n')
	if err != nil && line == "" {
		if errors.Is(err, io.EOF) {
			return "", errors.New("no passphrase was given")
		}
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// echoOff stops the terminal from showing what is typed, and gives back a
// function that turns it on again.
//
// This shells out to stty, because the standard library cannot set terminal
// modes and woosh takes no dependencies. There is no stty on windows, so the
// caller warns and carries on with the echo left on.
func echoOff(in *os.File) (func(), error) {
	stty, err := exec.LookPath("stty")
	if err != nil {
		return nil, err
	}
	set := func(mode string) error {
		cmd := exec.Command(stty, mode)
		cmd.Stdin = in
		return cmd.Run()
	}
	if err := set("-echo"); err != nil {
		return nil, err
	}

	// A Ctrl-C at the prompt must not leave the terminal unable to show what
	// the user types next.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	done := make(chan struct{})
	go func() {
		select {
		case <-sig:
			set("echo")
			os.Exit(130)
		case <-done:
		}
	}()

	return func() {
		close(done)
		signal.Stop(sig)
		set("echo")
	}, nil
}

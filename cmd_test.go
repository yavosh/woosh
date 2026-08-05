package main

import (
	"context"
	"strings"
	"testing"
)

// TestRecvArgOrder covers the flag parsing loop in cmdRecv. Go stops parsing
// flags at the first plain argument, so "recv CODE -o dir" needs the extra
// pass that cmdRecv makes.
func TestRecvArgOrder(t *testing.T) {
	t.Setenv(keyEnv, encodeKey(testKey(t)))

	// Every form must reach the network and fail there, not fail on the flags.
	// "no offer" is what a parsed command says when nothing answers.
	forms := [][]string{
		{"-wait", "100ms", "1-alpha-beta"},
		{"1-alpha-beta", "-o", t.TempDir(), "-wait", "100ms"},
		{"-o", t.TempDir(), "1-alpha-beta", "-wait", "100ms"},
		{"-force", "1-alpha-beta", "-max-size", "10", "-wait", "100ms"},
	}
	for _, args := range forms {
		err := cmdRecv(context.Background(), args)
		if err == nil {
			t.Errorf("%v: expected the browse to find nothing", args)
			continue
		}
		if !strings.Contains(err.Error(), "no offer") {
			t.Errorf("%v: %v", args, err)
		}
	}

	if err := cmdRecv(context.Background(), []string{"1-alpha-beta", "2-gamma-delta", "-wait", "100ms"}); err == nil {
		t.Error("two codes were accepted")
	}
}

func TestSendRejectsFlagAfterPath(t *testing.T) {
	t.Setenv(keyEnv, encodeKey(testKey(t)))

	err := cmdSend(context.Background(), []string{"go.mod", "--keep"})
	if err == nil || !strings.Contains(err.Error(), "before the paths") {
		t.Errorf("error is %v, want a hint about flag order", err)
	}
}

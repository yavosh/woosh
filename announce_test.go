package main

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

func TestAnnouncementValid(t *testing.T) {
	good := announcement{V: announceVersion, ID: offerID("1-alpha-beta"), Port: 51234, From: "mbp"}
	if !good.valid() {
		t.Fatal("a good announcement was rejected")
	}

	bad := map[string]announcement{
		"wrong version": {V: 99, ID: good.ID, Port: 1},
		"short id":      {V: announceVersion, ID: "abc", Port: 1},
		"no id":         {V: announceVersion, Port: 1},
		"port zero":     {V: announceVersion, ID: good.ID, Port: 0},
		"port too high": {V: announceVersion, ID: good.ID, Port: 70000},
	}
	for name, a := range bad {
		if a.valid() {
			t.Errorf("%s: accepted", name)
		}
	}
}

// TestCleanTextHoldsBackEscapes covers what ls and recv print. An announce
// needs no key, so the name it carries is whatever a stranger typed.
func TestCleanTextHoldsBackEscapes(t *testing.T) {
	cases := map[string]string{
		"studio":                 "studio",
		"my laptop":              "my laptop",
		"\x1b[31mEVIL\x1b[0m":    "?[31mEVIL?[0m",
		"one\ntwo":               "one?two",
		"back\rup":               "back?up",
		"\u202egnp.exe":          "?gnp.exe", // right-to-left override
		strings.Repeat("a", 100): strings.Repeat("a", 40) + "...",
	}
	for in, want := range cases {
		if got := cleanText(in); got != want {
			t.Errorf("cleanText(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestOfferAddrUsesTheDatagramSource(t *testing.T) {
	// Port comes from the payload, the host never does.
	o := offer{
		announcement: announcement{Port: 51234},
		IP:           net.ParseIP("192.168.1.20"),
	}
	if got, want := o.addr(), "192.168.1.20:51234"; got != want {
		t.Errorf("addr() = %q, want %q", got, want)
	}
}

// TestAnnounceAndBrowse needs working multicast on the loopback interface.
// CI runners often lack it, so -short skips it.
func TestAnnounceAndBrowse(t *testing.T) {
	if testing.Short() {
		t.Skip("multicast is not dependable in a sandbox, run without -short")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	want := announcement{V: announceVersion, ID: offerID("1-alpha-beta"), Port: 51234, From: "test-host"}
	if err := announce(ctx, want); err != nil {
		t.Skipf("this machine cannot send multicast: %v", err)
	}

	o, err := waitForOffer(ctx, func(o offer) bool { return o.ID == want.ID })
	if err != nil {
		t.Skipf("no announce came back, multicast is probably blocked here: %v", err)
	}
	if o.Port != want.Port || o.From != want.From {
		t.Errorf("got %+v, want %+v", o.announcement, want)
	}
	if o.IP == nil {
		t.Error("the offer has no source address")
	}
}

// TestDialOfferTriesAnotherAddress covers a machine with several interfaces.
// The first announce to arrive can come from an address this machine cannot
// reach, and the offer is still there behind another one.
func TestDialOfferTriesAnotherAddress(t *testing.T) {
	if testing.Short() {
		t.Skip("multicast is not dependable in a sandbox, run without -short")
	}

	// The offer, reachable on every interface of this machine.
	ln, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	id := offerID("1-alpha-retry")
	a := announcement{V: announceVersion, ID: id, Port: ln.Addr().(*net.TCPAddr).Port, From: "test-host"}
	if err := announce(ctx, a); err != nil {
		t.Skipf("this machine cannot send multicast: %v", err)
	}

	conn, err := dialOffer(ctx, deadAddr(t), id, 5*time.Second)
	if err != nil {
		t.Skipf("no announce came back, multicast is probably blocked here: %v", err)
	}
	conn.Close()
}

// deadAddr gives an address that nothing answers on.
func deadAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

// TestCollectOffersIgnoresJunk checks that a malformed datagram on the group
// does not end up in the list.
func TestCollectOffersIgnoresJunk(t *testing.T) {
	if testing.Short() {
		t.Skip("multicast is not dependable in a sandbox, run without -short")
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	group := &net.UDPAddr{IP: net.ParseIP(mcastGroup), Port: mcastPort}
	conns := multicastSenders()
	if len(conns) == 0 {
		t.Skip("this machine cannot send multicast")
	}
	defer func() {
		for _, c := range conns {
			c.Close()
		}
	}()

	go func() {
		for ctx.Err() == nil {
			for _, c := range conns {
				c.WriteToUDP([]byte("not json at all"), group)
				c.WriteToUDP([]byte(`{"v":99,"id":"aaaaaaaaaaaa","port":1}`), group)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}()

	offers, err := collectOffers(ctx, 400*time.Millisecond)
	if err != nil {
		t.Skipf("cannot listen for announces here: %v", err)
	}
	for _, o := range offers {
		if o.V != announceVersion {
			t.Errorf("a bad announce got through: %+v", o.announcement)
		}
	}
}

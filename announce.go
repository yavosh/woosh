package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"sync"
	"time"
)

const (
	// mcastGroup is an administratively scoped multicast group. Routers do not
	// forward it, so an announce stays on the local network.
	mcastGroup = "239.255.42.99"
	mcastPort  = 9977

	announceVersion = 1
	announceEvery   = time.Second
	// maxAnnounceLen caps a datagram. An announce is about 90 bytes.
	maxAnnounceLen = 512
)

// announcement is what a waiting sender puts on the network. It holds no file
// name and no size, because anything on the network can read it. The receiver
// learns what is on offer only after both sides prove they hold the key.
type announcement struct {
	V    int    `json:"v"`
	ID   string `json:"id"`
	Port int    `json:"port"`
	From string `json:"from"`
}

// offer is an announcement together with the address it arrived from. The
// address comes from the datagram, never from a field inside it, so a forged
// packet cannot send a receiver to another host.
type offer struct {
	announcement
	IP net.IP
}

func (o offer) addr() string {
	return net.JoinHostPort(o.IP.String(), strconv.Itoa(o.Port))
}

func (a announcement) valid() bool {
	_, err := hex.DecodeString(a.ID)
	return err == nil && a.V == announceVersion && len(a.ID) == offerIDLen && a.Port > 0 && a.Port < 65536
}

// announce sends a until ctx ends. It writes on every interface that can carry
// multicast, because the machine you want may not be on the default route.
func announce(ctx context.Context, a announcement) error {
	group := &net.UDPAddr{IP: net.ParseIP(mcastGroup), Port: mcastPort}

	conns := multicastSenders()
	if len(conns) == 0 {
		return errors.New("no interface on this machine can send multicast")
	}
	go func() {
		<-ctx.Done()
		for _, c := range conns {
			c.Close()
		}
	}()

	msg, err := json.Marshal(a)
	if err != nil {
		return err
	}

	go func() {
		t := time.NewTicker(announceEvery)
		defer t.Stop()
		for {
			for _, c := range conns {
				c.WriteToUDP(msg, group) // a dead interface is not worth reporting
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
	return nil
}

// multicastSenders binds one socket per usable interface. Binding to the
// interface address is how the standard library picks the outgoing interface
// for a multicast datagram.
func multicastSenders() []*net.UDPConn {
	var conns []*net.UDPConn
	for _, ip := range multicastIPv4Addrs() {
		c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: ip})
		if err == nil {
			conns = append(conns, c)
		}
	}
	return conns
}

func multicastIPv4Addrs() []net.IP {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var ips []net.IP
	for _, ifi := range ifaces {
		if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagMulticast == 0 {
			continue
		}
		addrs, err := ifi.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			n, ok := a.(*net.IPNet)
			if ok && n.IP.To4() != nil {
				ips = append(ips, n.IP.To4())
			}
		}
	}
	return ips
}

// browse calls found for every valid announce it sees, until ctx ends.
func browse(ctx context.Context, found func(offer)) error {
	group := &net.UDPAddr{IP: net.ParseIP(mcastGroup), Port: mcastPort}

	conns := multicastListeners(group)
	if len(conns) == 0 {
		return errors.New("cannot listen for announces on this machine")
	}
	go func() {
		<-ctx.Done()
		for _, c := range conns {
			c.Close()
		}
	}()

	var wg sync.WaitGroup
	for _, c := range conns {
		wg.Go(func() { readAnnounces(c, found) })
	}
	wg.Wait()
	return ctx.Err()
}

func readAnnounces(c *net.UDPConn, found func(offer)) {
	buf := make([]byte, maxAnnounceLen)
	for {
		n, src, err := c.ReadFromUDP(buf)
		if err != nil {
			return // the socket closed, which is how browse stops
		}
		var a announcement
		if json.Unmarshal(buf[:n], &a) != nil || !a.valid() {
			continue
		}
		found(offer{announcement: a, IP: src.IP})
	}
}

// multicastListeners joins the group on every usable interface, and adds a
// listener on the default interface so two terminals on one machine work.
func multicastListeners(group *net.UDPAddr) []*net.UDPConn {
	var conns []*net.UDPConn
	if c, err := net.ListenMulticastUDP("udp4", nil, group); err == nil {
		conns = append(conns, c)
	}

	ifaces, err := net.Interfaces()
	if err != nil {
		return conns
	}
	for i := range ifaces {
		ifi := &ifaces[i]
		if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagMulticast == 0 {
			continue
		}
		if c, err := net.ListenMulticastUDP("udp4", ifi, group); err == nil {
			conns = append(conns, c)
		}
	}
	return conns
}

// waitForOffer browses until match accepts an offer, or until ctx ends.
func waitForOffer(ctx context.Context, match func(offer) bool) (offer, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	hit := make(chan offer, 1)
	var once sync.Once
	stopped := make(chan error, 1)
	go func() {
		stopped <- browse(ctx, func(o offer) {
			if match(o) {
				once.Do(func() { hit <- o; cancel() })
			}
		})
	}()
	return awaitOffer(ctx, hit, stopped)
}

func awaitOffer(ctx context.Context, hit <-chan offer, stopped <-chan error) (offer, error) {
	select {
	case o := <-hit:
		return o, nil
	case err := <-stopped:
		// browse gave up. The match ran before it returned, so an offer that
		// arrived is already in hit.
		select {
		case o := <-hit:
			return o, nil
		default:
		}
		// A machine that cannot listen for announces says so, instead of
		// waiting out the timeout and blaming the code. browse gives a nil
		// error only if every socket died on its own, which is still a reason
		// to stop rather than to hand back an empty offer.
		if err == nil {
			err = errors.New("stopped listening for announces")
		}
		return offer{}, err
	case <-ctx.Done():
		select {
		case o := <-hit:
			return o, nil
		default:
		}
		return offer{}, ctx.Err()
	}
}

// collectOffers browses for d and returns what it saw, one entry per offer id.
func collectOffers(ctx context.Context, d time.Duration) ([]offer, error) {
	ctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()

	var mu sync.Mutex
	seen := make(map[string]offer)
	err := browse(ctx, func(o offer) {
		mu.Lock()
		defer mu.Unlock()
		if _, dup := seen[o.ID]; !dup {
			seen[o.ID] = o
		}
	})
	if err != nil && !errors.Is(err, context.DeadlineExceeded) {
		return nil, fmt.Errorf("browsing for offers: %w", err)
	}

	mu.Lock()
	defer mu.Unlock()
	out := make([]offer, 0, len(seen))
	for _, o := range seen {
		out = append(out, o)
	}
	return out, nil
}

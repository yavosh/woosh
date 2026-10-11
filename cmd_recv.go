package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// browseFor is how long recv and ls wait for announces before they give up.
const browseFor = 3 * time.Second

type recvOpts struct {
	code  string
	out   string // -o: a directory, or a file name for a single file
	force bool
	limit int64
	wait  time.Duration // how long to look for the offer

	// addr skips discovery and dials this address. Tests use it.
	addr string

	key      []byte
	stdout   io.Writer
	msg      io.Writer // messages for the user
	toStdout bool
	silent   bool
}

func cmdRecv(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("recv", flag.ExitOnError)
	out := fs.String("o", "", "write here: a directory, or a file name for a single file")
	force := fs.Bool("force", false, "replace a file that is already there")
	stdout := fs.Bool("stdout", false, "always write the payload to stdout")
	limit := fs.Int64("max-size", 0, "refuse a payload above this many bytes, 0 for no limit")
	wait := fs.Duration("wait", browseFor, "how long to look for the offer, so you can start before the sender does")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), "usage: woosh recv [flags] [code]\n\n"+
			"with no code, woosh takes the only offer on the network.\n\n")
		fs.PrintDefaults()
	}
	// Go stops flag parsing at the first plain argument, so "recv CODE -o dir"
	// would leave the flags unparsed. Take the code out and keep parsing, so
	// the code can sit on either side of the flags.
	var code string
	for rest := args; ; {
		if err := fs.Parse(rest); err != nil {
			return err
		}
		if fs.NArg() == 0 {
			break
		}
		if code != "" {
			return errors.New("recv takes one code at most")
		}
		code, rest = fs.Arg(0), fs.Args()[1:]
	}

	key, err := loadKey()
	if err != nil {
		return err
	}

	return runRecv(ctx, recvOpts{
		code:     code,
		out:      *out,
		force:    *force,
		limit:    *limit,
		wait:     *wait,
		key:      key,
		stdout:   os.Stdout,
		msg:      os.Stderr,
		toStdout: *stdout,
		silent:   !isTerminal(os.Stderr),
	})
}

func runRecv(ctx context.Context, opts recvOpts) (err error) {
	id, err := resolveCode(opts.code)
	if err != nil {
		return err
	}

	if opts.wait <= 0 {
		opts.wait = browseFor
	}

	// retryID is the offer to look for again when the address does not answer.
	// It stays empty when the address was given rather than discovered.
	addr, retryID := opts.addr, ""
	if addr == "" {
		o, err := findOffer(ctx, id, opts.wait)
		if err != nil {
			return err
		}
		addr, id, retryID = o.addr(), o.ID, o.ID
		fmt.Fprintf(opts.msg, "found an offer from %s at %s\n", cleanText(o.From), o.IP)
	}

	auth, err := newPeerAuth(opts.key, id)
	if err != nil {
		return err
	}

	raw, err := dialOffer(ctx, addr, retryID, opts.wait)
	if err != nil {
		return err
	}
	defer raw.Close()
	stop := context.AfterFunc(ctx, func() { raw.Close() })
	defer stop()
	defer func() {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
	}()
	raw.SetDeadline(time.Now().Add(setupTimeout))

	conn, err := auth.connect(tls.Client(raw, auth.clientConfig()))
	if err != nil {
		return err
	}
	confirm := func(got receipt) error {
		if err := raw.SetWriteDeadline(time.Now().Add(setupTimeout)); err != nil {
			return err
		}
		return writeJSONLine(conn, got)
	}

	var hdr header
	if err := readJSONLine(conn.r, &hdr); err != nil {
		return err
	}
	if err := hdr.validate(); err != nil {
		writeJSONLine(conn, reply{Reason: err.Error()})
		return err
	}

	dst, err := newSink(hdr, opts)
	if err != nil {
		writeJSONLine(conn, reply{Reason: err.Error()})
		return err
	}
	if err := writeJSONLine(conn, reply{Accept: true}); err != nil {
		dst.abort()
		return err
	}
	if err := raw.SetDeadline(time.Time{}); err != nil {
		dst.abort()
		return err
	}

	fmt.Fprintf(opts.msg, "receiving %s from %s\n", hdr.describe(), cleanText(hdr.From))

	p := newProgress(opts.msg, hdr.Size, opts.silent)
	n, err := recvPayload(io.MultiWriter(dst.w, p), conn.r, opts.limit)
	p.done()
	if err != nil {
		dst.abort()
		confirm(receipt{Reason: err.Error()})
		return err
	}

	where, err := dst.commit()
	if err != nil {
		confirm(receipt{Reason: err.Error()})
		return err
	}
	if err := confirm(receipt{Committed: true}); err != nil {
		return fmt.Errorf("payload committed, but could not confirm delivery: %w", err)
	}
	fmt.Fprintf(opts.msg, "got %s%s\n", humanBytes(n), where)
	return nil
}

// resolveCode turns what the user typed into an offer id. An empty code gives
// an empty id, which tells findOffer to take the only offer on the network.
func resolveCode(code string) (string, error) {
	if code == "" {
		return "", nil
	}
	code, err := normalizeCode(code)
	if err != nil {
		return "", err
	}
	return offerID(code), nil
}

// findOffer looks on the network for the wanted offer. With an empty id it
// takes the only offer it can see, and refuses to guess between several.
func findOffer(ctx context.Context, id string, wait time.Duration) (offer, error) {
	if id != "" {
		ctx, cancel := context.WithTimeout(ctx, wait)
		defer cancel()
		o, err := waitForOffer(ctx, func(o offer) bool { return o.ID == id })
		if errors.Is(err, context.DeadlineExceeded) {
			return offer{}, errors.New("no offer with that code on this network")
		}
		return o, err
	}

	offers, err := collectOffers(ctx, wait)
	if err != nil {
		return offer{}, err
	}
	return chooseOffer(offers)
}

func chooseOffer(offers []offer) (offer, error) {
	switch len(offers) {
	case 0:
		return offer{}, errors.New("no offer on this network")
	case 1:
		return offers[0], nil
	default:
		var b []byte
		for _, o := range offers {
			b = append(b, fmt.Sprintf("\n  %s from %s", cleanText(o.ID), cleanText(o.From))...)
		}
		return offer{}, fmt.Errorf("%d offers are waiting, give the code of the one you want:%s", len(offers), b)
	}
}

// dialOffer opens a connection to an offer. A machine with several interfaces
// announces from each of them, and only some of those addresses can be reached
// from here, so an address that does not answer sends dialOffer back to the
// network for another announce of the same offer, until wait runs out.
//
// Only a dial failure is worth another address. A handshake that fails means
// the wrong key, and the same key is on the other end of every address.
func dialOffer(ctx context.Context, addr, retryID string, wait time.Duration) (net.Conn, error) {
	dialer := net.Dialer{Timeout: setupTimeout}
	raw, err := dialer.DialContext(ctx, "tcp", addr)
	if err == nil || retryID == "" {
		return raw, err
	}

	tried := &addrSet{seen: map[string]bool{addr: true}}
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	for {
		o, findErr := waitForOffer(ctx, func(o offer) bool {
			return o.ID == retryID && tried.unseen(o.addr())
		})
		if findErr != nil {
			// No other address turned up, so report the first failure: it names
			// the address the user was told about.
			return nil, err
		}
		if raw, dialErr := dialer.DialContext(ctx, "tcp", o.addr()); dialErr == nil {
			return raw, nil
		}
	}
}

// addrSet is the set of addresses a receiver has already tried. browse calls
// the match function from one goroutine per interface, so it needs a lock.
type addrSet struct {
	mu   sync.Mutex
	seen map[string]bool
}

// unseen reports whether addr is new, and remembers it.
func (s *addrSet) unseen(addr string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.seen[addr] {
		return false
	}
	s.seen[addr] = true
	return true
}

// sink is where a payload lands. Nothing reaches its final place until commit,
// which runs only after the checksum matches.
type sink struct {
	w      io.Writer
	commit func() (string, error)
	abort  func()
}

func newSink(hdr header, opts recvOpts) (*sink, error) {
	if hdr.Kind == kindTar {
		if opts.toStdout {
			return nil, errors.New("an archive cannot go to stdout, give -o a directory")
		}
		return newTarSink(opts)
	}
	if opts.toStdout || (opts.out == "" && !isTerminal(opts.stdout)) {
		w := opts.stdout
		return &sink{w: w, commit: func() (string, error) { return "", nil }, abort: func() {}}, nil
	}
	return newFileSink(hdr, opts)
}

// newFileSink writes to a unique staging file and publishes on commit, so a broken
// transfer never leaves a file that looks complete.
func newFileSink(hdr header, opts recvOpts) (*sink, error) {
	name, err := safeName(hdr.Name)
	if err != nil {
		return nil, err
	}

	target := filepath.Join(".", name)
	if opts.out != "" {
		if info, err := os.Stat(opts.out); err == nil && info.IsDir() {
			target = filepath.Join(opts.out, name)
		} else {
			target = opts.out
		}
	}
	if _, err := os.Lstat(target); err == nil && !opts.force {
		return nil, fmt.Errorf("%s is already there, use --force to replace it", target)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}

	perm := os.FileMode(hdr.Mode).Perm()
	if perm == 0 {
		perm = 0o644
	}
	f, err := os.CreateTemp(filepath.Dir(target), ".woosh-partial-*")
	if err != nil {
		return nil, err
	}
	tmp := f.Name()
	abort := func() {
		f.Close()
		os.Remove(tmp)
	}

	return &sink{
		w: f,
		commit: func() (string, error) {
			defer abort()
			if err := f.Chmod(perm); err != nil {
				return "", err
			}
			if err := f.Close(); err != nil {
				return "", err
			}
			publish := os.Link // atomically refuses an existing target, including symlinks
			if opts.force {
				publish = os.Rename
			}
			if err := publish(tmp, target); err != nil {
				return "", err
			}
			return fmt.Sprintf(" into %q", target), nil
		},
		abort: abort,
	}, nil
}

// newTarSink unpacks into a hidden directory beside the target and moves the
// entries across on commit.
func newTarSink(opts recvOpts) (*sink, error) {
	dest := opts.out
	if dest == "" {
		dest = "."
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return nil, err
	}
	staging, err := os.MkdirTemp(dest, ".woosh-partial-")
	if err != nil {
		return nil, err
	}

	pr, pw := io.Pipe()
	done := make(chan error, 1)
	go func() {
		err := extractTar(pr, staging)
		pr.CloseWithError(err) // stop the writer if extraction gave up early
		done <- err
	}()

	return &sink{
		w: pw,
		commit: func() (string, error) {
			pw.Close()
			if err := <-done; err != nil {
				os.RemoveAll(staging)
				return "", err
			}
			if err := moveInto(staging, dest, opts.force); err != nil {
				return "", fmt.Errorf("%w; received files remain in %s", err, staging)
			}
			return fmt.Sprintf(" into %q", dest+string(filepath.Separator)), nil
		},
		abort: func() {
			pw.CloseWithError(errors.New("transfer failed"))
			<-done
			os.RemoveAll(staging)
		},
	}, nil
}

// isTerminal reports whether w is a character device, which means a person is
// probably looking at it.
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

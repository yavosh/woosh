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

func runRecv(ctx context.Context, opts recvOpts) error {
	id, err := resolveCode(opts.code)
	if err != nil {
		return err
	}

	if opts.wait <= 0 {
		opts.wait = browseFor
	}

	addr := opts.addr
	if addr == "" {
		o, err := findOffer(ctx, id, opts.wait)
		if err != nil {
			return err
		}
		addr, id = o.addr(), o.ID
		fmt.Fprintf(opts.msg, "found an offer from %s at %s\n", o.From, o.IP)
	}

	auth, err := newPeerAuth(opts.key, id)
	if err != nil {
		return err
	}

	dialer := net.Dialer{Timeout: setupTimeout}
	raw, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	defer raw.Close()
	raw.SetDeadline(time.Now().Add(setupTimeout))

	conn, err := auth.connect(tls.Client(raw, auth.clientConfig()))
	if err != nil {
		return err
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

	fmt.Fprintf(opts.msg, "receiving %s from %s\n", hdr.describe(), hdr.From)

	p := newProgress(opts.msg, hdr.Size, opts.silent)
	n, err := recvPayload(io.MultiWriter(dst.w, p), conn.r, opts.limit)
	p.done()
	if err != nil {
		dst.abort()
		return err
	}

	where, err := dst.commit()
	if err != nil {
		return err
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
	switch len(offers) {
	case 0:
		return offer{}, errors.New("no offer on this network")
	case 1:
		return offers[0], nil
	default:
		var b []byte
		for _, o := range offers {
			b = append(b, fmt.Sprintf("\n  %s from %s", o.ID, o.From)...)
		}
		return offer{}, fmt.Errorf("%d offers are waiting, give the code of the one you want:%s", len(offers), b)
	}
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

// newFileSink writes to "<name>.partial" and renames on commit, so a broken
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
	if _, err := os.Stat(target); err == nil && !opts.force {
		return nil, fmt.Errorf("%s is already there, use --force to replace it", target)
	}

	perm := os.FileMode(hdr.Mode).Perm()
	if perm == 0 {
		perm = 0o644
	}
	tmp := target + ".partial"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return nil, err
	}

	return &sink{
		w: f,
		commit: func() (string, error) {
			if err := f.Close(); err != nil {
				return "", err
			}
			if err := os.Rename(tmp, target); err != nil {
				return "", err
			}
			return " into " + target, nil
		},
		abort: func() {
			f.Close()
			os.Remove(tmp)
		},
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
				os.RemoveAll(staging)
				return "", err
			}
			return " into " + dest + string(filepath.Separator), nil
		},
		abort: func() {
			pw.CloseWithError(errors.New("transfer failed"))
			<-done
			os.RemoveAll(staging)
		},
	}, nil
}

// moveInto moves every top level entry of staging into dest, then removes
// staging.
func moveInto(staging, dest string, force bool) error {
	entries, err := os.ReadDir(staging)
	if err != nil {
		return err
	}
	for _, e := range entries {
		target := filepath.Join(dest, e.Name())
		if _, err := os.Stat(target); err == nil {
			if !force {
				return fmt.Errorf("%s is already there, use --force to replace it", target)
			}
			if err := os.RemoveAll(target); err != nil {
				return err
			}
		}
		if err := os.Rename(filepath.Join(staging, e.Name()), target); err != nil {
			return err
		}
	}
	return os.Remove(staging)
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

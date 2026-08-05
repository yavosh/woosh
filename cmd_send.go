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
	"strings"
	"time"
)

const (
	defaultSnippetName = "snippet.txt"

	// setupTimeout bounds the handshake and the header exchange. The payload
	// that follows runs without a deadline.
	setupTimeout = 30 * time.Second
)

type sendOpts struct {
	paths   []string
	code    string
	name    string
	timeout time.Duration
	keep    bool

	key    []byte
	stdin  io.Reader
	out    io.Writer // messages for the user
	silent bool      // no progress line

	// announce turns the multicast beacon on. Tests switch it off and use
	// onReady to learn the address instead.
	announce bool
	onReady  func(net.Addr)
}

func cmdSend(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("send", flag.ExitOnError)
	code := fs.String("code", "", "use this code instead of a generated one")
	name := fs.String("name", defaultSnippetName, "file name to give a snippet read from stdin")
	timeout := fs.Duration("timeout", 5*time.Minute, "give up if nobody takes the offer")
	keep := fs.Bool("keep", false, "stay up and serve the offer more than once")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), "usage: woosh send [flags] [path...]\n\n"+
			"with no path, woosh sends a snippet read from stdin.\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	// Go stops flag parsing at the first path, so a flag written after a path
	// would become a path. Say so instead of failing later on a strange name.
	for _, a := range fs.Args() {
		if strings.HasPrefix(a, "-") {
			return fmt.Errorf("put %s before the paths: woosh send [flags] [path...]", a)
		}
	}

	key, err := loadKey()
	if err != nil {
		return err
	}

	return runSend(ctx, sendOpts{
		paths:    fs.Args(),
		code:     *code,
		name:     *name,
		timeout:  *timeout,
		keep:     *keep,
		key:      key,
		stdin:    os.Stdin,
		out:      os.Stderr,
		silent:   !isTerminal(os.Stderr),
		announce: true,
	})
}

func runSend(ctx context.Context, opts sendOpts) error {
	if opts.keep && len(opts.paths) == 0 {
		return errors.New("--keep needs a path: a snippet from stdin can only be sent once")
	}

	code := opts.code
	if code == "" {
		var err error
		if code, err = newCode(); err != nil {
			return err
		}
	}
	code, err := normalizeCode(code)
	if err != nil {
		return err
	}
	id := offerID(code)

	src, err := newSource(opts)
	if err != nil {
		return err
	}

	auth, err := newPeerAuth(opts.key, id)
	if err != nil {
		return err
	}

	ln, err := net.Listen("tcp", ":0")
	if err != nil {
		return err
	}
	defer ln.Close()

	if opts.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.timeout)
		defer cancel()
	}
	go func() {
		<-ctx.Done()
		ln.Close() // unblocks Accept
	}()

	if opts.announce {
		host, _ := os.Hostname()
		a := announcement{V: announceVersion, ID: id, Port: ln.Addr().(*net.TCPAddr).Port, From: host}
		if err := announce(ctx, a); err != nil {
			return err
		}
	}
	if opts.onReady != nil {
		opts.onReady(ln.Addr())
	}

	fmt.Fprintf(opts.out, "code: %s\n", code)
	fmt.Fprintf(opts.out, "sending %s, waiting %s for a receiver...\n", src.hdr.describe(), opts.timeout)

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				if errors.Is(ctxErr, context.DeadlineExceeded) {
					return errors.New("nobody took the offer")
				}
				return ctxErr
			}
			return err
		}

		if err := serveOne(conn, auth, src, opts); err != nil {
			if !opts.keep {
				return err
			}
			// With --keep a failed attempt is not fatal, so report it and wait
			// for the next receiver.
			fmt.Fprintf(opts.out, "woosh: %v\n", err)
			continue
		}
		if !opts.keep {
			return nil
		}
	}
}

func serveOne(raw net.Conn, auth peerAuth, src *source, opts sendOpts) error {
	defer raw.Close()

	// A short deadline covers the handshake, so a stalled peer cannot hold the
	// offer open. The payload itself may take as long as it needs.
	raw.SetDeadline(time.Now().Add(setupTimeout))

	conn, err := auth.accept(tls.Server(raw, auth.serverConfig()))
	if err != nil {
		return err
	}

	if err := writeJSONLine(conn, src.hdr); err != nil {
		return err
	}
	var rep reply
	if err := readJSONLine(conn.r, &rep); err != nil {
		return err
	}
	if !rep.Accept {
		return fmt.Errorf("the receiver declined: %s", rep.Reason)
	}
	if err := raw.SetDeadline(time.Time{}); err != nil {
		return err
	}

	body, err := src.open()
	if err != nil {
		return err
	}
	defer body.Close()

	p := newProgress(opts.out, src.hdr.Size, opts.silent)
	n, err := sendPayload(conn, io.TeeReader(body, p))
	p.done()
	if err != nil {
		return err
	}

	fmt.Fprintf(opts.out, "sent %s\n", humanBytes(n))
	return nil
}

// source is what a sender offers: a header, and a way to open the bytes. open
// may be called more than once when --keep is on.
type source struct {
	hdr  header
	open func() (io.ReadCloser, error)
}

func newSource(opts sendOpts) (*source, error) {
	host, _ := os.Hostname()
	hdr := header{V: wireVersion, From: host, Mode: 0o644}

	// No path: a snippet on stdin, of unknown length.
	if len(opts.paths) == 0 {
		if opts.name == "" {
			opts.name = defaultSnippetName
		}
		name, err := safeName(opts.name)
		if err != nil {
			return nil, err
		}
		hdr.Kind, hdr.Name, hdr.Size = kindBlob, name, -1
		return &source{hdr: hdr, open: func() (io.ReadCloser, error) {
			return io.NopCloser(opts.stdin), nil
		}}, nil
	}

	// One regular file: send it as it is.
	if len(opts.paths) == 1 {
		path := opts.paths[0]
		info, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() && !info.IsDir() {
			return nil, fmt.Errorf("cannot send %s: it is neither a file nor a directory", path)
		}
		if info.Mode().IsRegular() {
			name, err := safeName(filepath.Base(path))
			if err != nil {
				return nil, err
			}
			hdr.Kind, hdr.Name = kindBlob, name
			hdr.Size, hdr.Mode = info.Size(), uint32(info.Mode().Perm())
			return &source{hdr: hdr, open: func() (io.ReadCloser, error) {
				return os.Open(path)
			}}, nil
		}
	}

	// A directory, or several paths: a tar stream of unknown length. The name
	// is for the receiver to read, the entries carry their own paths.
	//
	// Packing happens once a receiver arrives, so check every path now. A
	// missing path must not surface minutes later, in the middle of a transfer.
	for _, p := range opts.paths {
		if _, err := os.Stat(p); err != nil {
			return nil, err
		}
	}
	name, err := safeName(filepath.Base(filepath.Clean(opts.paths[0])))
	if err != nil {
		return nil, err
	}
	hdr.Kind, hdr.Name, hdr.Size = kindTar, name, -1
	paths, out := opts.paths, opts.out
	return &source{hdr: hdr, open: func() (io.ReadCloser, error) {
		return tarStream(paths, out), nil
	}}, nil
}

// tarStream packs paths in the background and gives back the read end.
func tarStream(paths []string, skipped io.Writer) io.ReadCloser {
	pr, pw := io.Pipe()
	go func() {
		pw.CloseWithError(packPaths(pw, paths, skipped))
	}()
	return pr
}

func (h header) describe() string {
	if h.Kind == kindTar {
		return fmt.Sprintf("%s (archive)", h.Name)
	}
	if h.Size < 0 {
		return fmt.Sprintf("%s (from stdin)", h.Name)
	}
	return fmt.Sprintf("%s (%s)", h.Name, humanBytes(h.Size))
}

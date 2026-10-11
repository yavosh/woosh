package main

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func requireContent(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("%s: got %q, want %q", path, got, want)
	}
}

func TestFileSinkPreservesExistingPartial(t *testing.T) {
	for _, link := range []bool{false, true} {
		t.Run(fmt.Sprint(link), func(t *testing.T) {
			if link && runtime.GOOS == "windows" {
				t.Skip("symlinks need extra rights on windows")
			}
			dir := t.TempDir()
			partial := filepath.Join(dir, "report.txt.partial")
			victim := partial
			if link {
				victim = filepath.Join(t.TempDir(), "unrelated.txt")
				if err := os.Symlink(victim, partial); err != nil {
					t.Fatal(err)
				}
			}
			writeFile(t, victim, "existing data", 0o600)
			s, err := newFileSink(header{Name: "report.txt"}, recvOpts{out: dir})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := io.WriteString(s.w, "unverified data"); err != nil {
				t.Fatal(err)
			}
			s.abort()
			requireContent(t, victim, "existing data")
			if _, err := os.Lstat(partial); err != nil {
				t.Fatal("abort removed an unrelated partial file:", err)
			}
		})
	}
}

func TestFileSinksHaveIndependentStaging(t *testing.T) {
	dir := t.TempDir()
	hdr := header{Name: "report.txt", Mode: 0o600}
	a, err := newFileSink(hdr, recvOpts{out: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer a.abort()
	b, err := newFileSink(hdr, recvOpts{out: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer b.abort()
	if _, err := io.WriteString(a.w, "verified data"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(b.w, "unverified data"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.commit(); err == nil {
		t.Fatal("second transfer replaced the first without --force")
	}
	requireContent(t, filepath.Join(dir, hdr.Name), "verified data")
	if leftovers := partialFiles(t, dir); len(leftovers) != 0 {
		t.Fatal("failed commit left staging files:", leftovers)
	}
}

func TestFileCommitRefusesLateTargets(t *testing.T) {
	for _, kind := range []string{"file", "directory", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			if kind == "symlink" && runtime.GOOS == "windows" {
				t.Skip("symlinks need extra rights on windows")
			}
			dir := t.TempDir()
			target := filepath.Join(dir, "report.txt")
			s, err := newFileSink(header{Name: "report.txt"}, recvOpts{out: dir})
			if err != nil {
				t.Fatal(err)
			}
			defer s.abort()
			if _, err := io.WriteString(s.w, "received data"); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "file":
				writeFile(t, target, "late arrival", 0o600)
			case "directory":
				if err := os.Mkdir(target, 0o700); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink("missing", target); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.Lstat(target)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.commit(); err == nil {
				t.Fatal("commit replaced a late target without --force")
			}
			after, err := os.Lstat(target)
			if err != nil || !os.SameFile(before, after) {
				t.Fatalf("late target changed: %v", err)
			}
			if kind == "file" {
				requireContent(t, target, "late arrival")
			}
			if leftovers := partialFiles(t, dir); len(leftovers) != 0 {
				t.Fatal("failed commit left staging files:", leftovers)
			}
		})
	}
}

func TestFileSinkAbortPreservesOtherTransfer(t *testing.T) {
	dir := t.TempDir()
	a, err := newFileSink(header{Name: "report.txt"}, recvOpts{out: dir})
	if err != nil {
		t.Fatal(err)
	}
	b, err := newFileSink(header{Name: "report.txt"}, recvOpts{out: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer b.abort()
	a.abort()
	if _, err := io.WriteString(b.w, "verified data"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.commit(); err != nil {
		t.Fatal(err)
	}
	requireContent(t, filepath.Join(dir, "report.txt"), "verified data")
}

func TestArchivePreflightsAllConflicts(t *testing.T) {
	dest, staging := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(staging, "a.txt"), "new first file", 0o600)
	writeFile(t, filepath.Join(staging, "b.txt"), "new second file", 0o600)
	writeFile(t, filepath.Join(dest, "b.txt"), "existing second file", 0o600)
	if err := moveInto(staging, dest, false); err == nil {
		t.Fatal("expected a conflict")
	}
	if _, err := os.Stat(filepath.Join(dest, "a.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed commit published an earlier entry: %v", err)
	}
	requireContent(t, filepath.Join(staging, "a.txt"), "new first file")
	requireContent(t, filepath.Join(staging, "b.txt"), "new second file")
	requireContent(t, filepath.Join(dest, "b.txt"), "existing second file")
}

func TestArchiveSinkRetainsDataAfterCommitConflict(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "b.txt"), "existing file", 0o600)
	s, err := newTarSink(recvOpts{out: dir})
	if err != nil {
		t.Fatal(err)
	}
	var arc bytes.Buffer
	tw := tar.NewWriter(&arc)
	for _, name := range []string{"a.txt", "b.txt"} {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: 8}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte("incoming")); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(s.w, &arc); err != nil {
		s.abort()
		t.Fatal(err)
	}
	if _, err := s.commit(); err == nil || !strings.Contains(err.Error(), "received files remain") {
		t.Fatalf("commit did not report retained staging: %v", err)
	}
	staging, err := filepath.Glob(filepath.Join(dir, ".woosh-partial-*"))
	if err != nil || len(staging) != 1 {
		t.Fatalf("recoverable staging missing: %v, %v", staging, err)
	}
	for _, name := range []string{"a.txt", "b.txt"} {
		requireContent(t, filepath.Join(staging[0], name), "incoming")
	}
	if _, err := os.Stat(filepath.Join(dir, "a.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("conflicting archive published an earlier entry: %v", err)
	}
	requireContent(t, filepath.Join(dir, "b.txt"), "existing file")
}

func TestArchiveForceReplacesFilesAndDirectories(t *testing.T) {
	dest, staging := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(dest, "a", "old.txt"), "old tree", 0o600)
	writeFile(t, filepath.Join(dest, "b"), "old file", 0o600)
	writeFile(t, filepath.Join(staging, "a"), "new file", 0o600)
	writeFile(t, filepath.Join(staging, "b", "new.txt"), "new tree", 0o600)
	if err := moveInto(staging, dest, true); err != nil {
		t.Fatal(err)
	}
	requireContent(t, filepath.Join(dest, "a"), "new file")
	requireContent(t, filepath.Join(dest, "b", "new.txt"), "new tree")
	if _, err := os.Stat(staging); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("successful publication left staging: %v", err)
	}
}

func TestArchivePreflightRefusesDanglingSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need extra rights on windows")
	}
	dest, staging := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(staging, "a"), "incoming", 0o600)
	if err := os.Symlink("missing", filepath.Join(dest, "a")); err != nil {
		t.Fatal(err)
	}
	if err := moveInto(staging, dest, false); err == nil {
		t.Fatal("archive replaced a dangling symlink")
	}
	if link, err := os.Readlink(filepath.Join(dest, "a")); err != nil || link != "missing" {
		t.Fatalf("existing symlink changed: %v", err)
	}
}

func TestArchiveRollbackAfterPublicationFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix socket fixture")
	}
	for _, force := range []bool{false, true} {
		t.Run(fmt.Sprint(force), func(t *testing.T) {
			dest := t.TempDir()
			// Unix socket paths have a small platform-specific length limit.
			staging, err := os.MkdirTemp("", "woosh-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				for _, root := range []string{dest, staging} {
					filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
						if err == nil && entry.IsDir() {
							return os.Chmod(path, 0o700)
						}
						return err
					})
				}
				os.RemoveAll(staging)
			})
			writeFile(t, filepath.Join(staging, "a"), "incoming file", 0o600)
			if force {
				writeFile(t, filepath.Join(dest, "a", "old.txt"), "original file", 0o444)
				if err := os.Symlink("old.txt", filepath.Join(dest, "a", "link")); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(filepath.Join(dest, "a"), 0o500); err != nil {
					t.Fatal(err)
				}
			}
			// This unexpected staging entry simulates a later filesystem failure.
			ln, err := net.Listen("unix", filepath.Join(staging, "z.socket"))
			if err != nil {
				t.Fatal(err)
			}
			defer ln.Close()
			if err := moveInto(staging, dest, force); err == nil {
				t.Fatal("expected publication failure")
			}
			requireContent(t, filepath.Join(staging, "a"), "incoming file")
			if force {
				requireContent(t, filepath.Join(dest, "a", "old.txt"), "original file")
				requireContent(t, filepath.Join(dest, "a", "link"), "original file")
				if got := dirPerm(t, filepath.Join(dest, "a")); got != 0o500 {
					t.Fatalf("rollback changed directory permissions to %o", got)
				}
			} else if _, err := os.Stat(filepath.Join(dest, "a")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("rollback left a published file: %v", err)
			}
		})
	}
}

func TestPublicationRefusesLateDirectory(t *testing.T) {
	staging, dest := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(staging, "a", "new.txt"), "incoming file", 0o600)
	if err := os.Mkdir(filepath.Join(dest, "a"), 0o700); err != nil {
		t.Fatal(err)
	}
	var p publication
	if err := p.add(filepath.Join(staging, "a"), filepath.Join(dest, "a")); !errors.Is(err, os.ErrExist) {
		t.Fatalf("publication did not refuse an existing directory: %v", err)
	}
	if err := p.rollback(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dest, "a")); err != nil {
		t.Fatal("rollback removed an unrelated directory:", err)
	}
}

func TestPublicationRollbackPreservesChangedTarget(t *testing.T) {
	staging, dest := t.TempDir(), t.TempDir()
	source, target := filepath.Join(staging, "a"), filepath.Join(dest, "a")
	writeFile(t, source, "incoming file", 0o600)
	var p publication
	if err := p.add(source, target); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	writeFile(t, target, "external replacement", 0o600)
	if err := p.rollback(); err == nil {
		t.Fatal("rollback did not report the changed target")
	}
	requireContent(t, target, "external replacement")
}

func TestMatchedOfferWinsCancellation(t *testing.T) {
	for range 1000 {
		ctx, cancel := context.WithCancel(context.Background())
		hit := make(chan offer, 1)
		stopped := make(chan error, 1)
		want := offer{announcement: announcement{ID: offerID("1-alpha-beta")}}
		hit <- want
		cancel()
		stopped <- ctx.Err()
		got, err := awaitOffer(ctx, hit, stopped)
		if err != nil || got.ID != want.ID {
			t.Fatalf("lost a matched offer: %v", err)
		}
	}
}

func TestAnnouncementRejectsNonHexID(t *testing.T) {
	for _, id := range []string{"\x1b[2Jxxxxxxxx", "zzzzzzzzzzzz", "aaaaaaaaaaa\n"} {
		if (announcement{V: announceVersion, ID: id, Port: 1}).valid() {
			t.Fatalf("accepted invalid ID %q", id)
		}
	}
}

func TestKeyReplacementRestoresPrivatePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permissions")
	}
	tempHome(t)
	path, err := keyPath()
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, "synthetic placeholder", 0o644)
	if _, err := saveKey(make([]byte, keyLen)); err != nil {
		t.Fatal(err)
	}
	if perm := dirPerm(t, path); perm != 0o600 {
		t.Fatalf("replacement key mode is %o, want 600", perm)
	}
	files, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".woosh-key-*"))
	if err != nil || len(files) != 0 {
		t.Fatalf("key staging files remain: %v, %v", files, err)
	}
}

func TestKeyReplacementDoesNotFollowSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need extra rights on windows")
	}
	tempHome(t)
	path, err := keyPath()
	if err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(t.TempDir(), "unrelated.txt")
	writeFile(t, victim, "unrelated data", 0o644)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, path); err != nil {
		t.Fatal(err)
	}
	if _, err := saveKey(make([]byte, keyLen)); err != nil {
		t.Fatal(err)
	}
	requireContent(t, victim, "unrelated data")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("replacement is not a private regular file: %v", err)
	}
}

func startTestSender(t *testing.T, opts sendOpts) (string, <-chan error, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	t.Cleanup(cancel)
	ready := make(chan string, 1)
	done := make(chan error, 1)
	if opts.out == nil {
		opts.out = io.Discard
	}
	opts.silent = true
	opts.onReady = func(a net.Addr) {
		ready <- net.JoinHostPort("127.0.0.1", portOf(t, a))
	}
	go func() { done <- runSend(ctx, opts) }()
	select {
	case addr := <-ready:
		return addr, done, cancel
	case err := <-done:
		t.Fatalf("sender stopped before readiness: %v", err)
	case <-ctx.Done():
		t.Fatal("sender never became ready")
	}
	return "", nil, cancel
}

func waitSender(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(2 * time.Second):
		t.Fatal("sender stayed blocked")
		return nil
	}
}

func connectTestReceiver(t *testing.T, key []byte, code, addr string) (*authConn, net.Conn) {
	t.Helper()
	auth, err := newPeerAuth(key, offerID(code))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { raw.Close() })
	raw.SetDeadline(time.Now().Add(3 * time.Second))
	conn, err := auth.connect(tls.Client(raw, auth.clientConfig()))
	if err != nil {
		t.Fatal(err)
	}
	var hdr header
	if err := readJSONLine(conn.r, &hdr); err != nil {
		t.Fatal(err)
	}
	if err := hdr.validate(); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONLine(conn, reply{Accept: true}); err != nil {
		t.Fatal(err)
	}
	return conn, raw
}

func TestSendCancellationClosesActiveConnection(t *testing.T) {
	for _, phase := range []string{"payload", "receipt"} {
		t.Run(phase, func(t *testing.T) {
			key, code := testKey(t), "1-alpha-beta"
			body := []byte("small payload")
			if phase == "payload" {
				body = make([]byte, 16<<20)
			}
			addr, done, cancel := startTestSender(t, sendOpts{
				key: key, code: code, stdin: bytes.NewReader(body),
			})
			conn, _ := connectTestReceiver(t, key, code, addr)
			if phase == "payload" {
				if _, err := conn.r.ReadByte(); err != nil {
					t.Fatal(err)
				}
			} else if _, err := recvPayload(io.Discard, conn.r, 0); err != nil {
				t.Fatal(err)
			}
			cancel()
			if err := waitSender(t, done); !errors.Is(err, context.Canceled) {
				t.Fatalf("sender cancellation returned %v", err)
			}
		})
	}
}

func TestSenderRequiresDeliveryReceipt(t *testing.T) {
	key, code := testKey(t), "1-alpha-beta"
	var messages bytes.Buffer
	addr, done, _ := startTestSender(t, sendOpts{
		key: key, code: code, stdin: strings.NewReader("payload"), out: &messages,
	})
	conn, raw := connectTestReceiver(t, key, code, addr)
	if _, err := recvPayload(io.Discard, conn.r, 0); err != nil {
		t.Fatal(err)
	}
	raw.Close() // No receipt, even though all payload bytes arrived.
	if err := waitSender(t, done); err == nil {
		t.Fatal("sender reported success without a receipt")
	}
	if strings.Contains(messages.String(), "sent ") {
		t.Fatal("sender printed success without a receipt")
	}
}

func TestReceiverSendsFailureReceiptForCorruption(t *testing.T) {
	key, code := testKey(t), "1-alpha-beta"
	auth, err := newPeerAuth(key, offerID(code))
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	done := make(chan error, 1)
	go func() {
		raw, err := ln.Accept()
		if err != nil {
			done <- err
			return
		}
		defer raw.Close()
		raw.SetDeadline(time.Now().Add(3 * time.Second))
		conn, err := auth.accept(tls.Server(raw, auth.serverConfig()))
		if err == nil {
			err = writeJSONLine(conn, header{V: wireVersion, Kind: kindBlob, Name: "report.txt"})
		}
		var rep reply
		if err == nil {
			err = readJSONLine(conn.r, &rep)
		}
		if err == nil && !rep.Accept {
			err = errors.New("receiver declined before payload")
		}
		var framed bytes.Buffer
		if err == nil {
			_, err = sendPayload(&framed, strings.NewReader("payload"))
		}
		if err == nil {
			frame := framed.Bytes()
			frame[len(frame)-1] ^= 1 // Corrupt the checksum, not the TLS stream.
			_, err = conn.Write(frame)
		}
		var got receipt
		if err == nil {
			err = readJSONLine(conn.r, &got)
		}
		if err == nil && (got.Committed || !strings.Contains(got.Reason, "checksum mismatch")) {
			err = errors.New("receiver did not send a checksum failure receipt")
		}
		done <- err
	}()
	dir := t.TempDir()
	err = runRecv(t.Context(), recvOpts{
		key: key, code: code, addr: ln.Addr().String(), out: dir, msg: io.Discard, silent: true,
	})
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("receiver did not reject corruption: %v", err)
	}
	if err := waitSender(t, done); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "report.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("corrupted payload was published: %v", err)
	}
	if leftovers := partialFiles(t, dir); len(leftovers) != 0 {
		t.Fatal("corrupted payload left staging:", leftovers)
	}
}

type writeFunc func([]byte) (int, error)

func (f writeFunc) Write(b []byte) (int, error) { return f(b) }

func TestSenderReportsReceiverCommitFailure(t *testing.T) {
	key, code := testKey(t), "1-alpha-beta"
	var messages bytes.Buffer
	addr, done, _ := startTestSender(t, sendOpts{
		key: key, code: code, stdin: strings.NewReader("payload"), name: "report.txt", out: &messages,
	})
	dir := t.TempDir()
	target := filepath.Join(dir, "report.txt")
	msg := writeFunc(func(b []byte) (int, error) {
		if strings.HasPrefix(string(b), "receiving ") {
			// Sink creation precedes this message; the target arrives during transfer.
			if err := os.WriteFile(target, []byte("late arrival"), 0o600); err != nil {
				return 0, err
			}
		}
		return len(b), nil
	})
	err := runRecv(t.Context(), recvOpts{
		key: key, code: code, addr: addr, out: dir, msg: msg, silent: true,
	})
	if err == nil {
		t.Fatal("receiver replaced a late arrival")
	}
	if err := waitSender(t, done); err == nil || !strings.Contains(err.Error(), "could not commit") {
		t.Fatalf("sender did not report receiver commit failure: %v", err)
	}
	requireContent(t, target, "late arrival")
	if strings.Contains(messages.String(), "sent ") {
		t.Fatal("sender printed success after commit failure")
	}
}

type delayedReader struct {
	r     io.Reader
	first bool
}

func (r *delayedReader) Read(b []byte) (int, error) {
	if !r.first {
		r.first = true
		time.Sleep(250 * time.Millisecond)
	}
	return r.r.Read(b)
}

func TestOfferTimeoutAllowsActiveTransfer(t *testing.T) {
	key, code := testKey(t), "1-alpha-beta"
	addr, done, _ := startTestSender(t, sendOpts{
		key: key, code: code, timeout: 100 * time.Millisecond,
		stdin: &delayedReader{r: strings.NewReader("payload")},
	})
	var received bytes.Buffer
	if err := runRecv(t.Context(), recvOpts{
		key: key, code: code, addr: addr, stdout: &received, toStdout: true, msg: io.Discard, silent: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := waitSender(t, done); err != nil {
		t.Fatal(err)
	}
	if received.String() != "payload" {
		t.Fatal("active transfer was truncated")
	}
}

func TestReceiverRejectsOldWireVersion(t *testing.T) {
	if err := (header{V: 1, Kind: kindBlob, Name: "report.txt"}).validate(); err == nil {
		t.Fatal("accepted a peer without delivery receipts")
	}
}

func TestOfferChoiceSanitizesHostnames(t *testing.T) {
	_, err := chooseOffer([]offer{
		{announcement: announcement{ID: offerID("1-alpha-beta"), From: "bad\x1b[2J\npeer"}},
		{announcement: announcement{ID: offerID("2-alpha-beta"), From: "second"}},
	})
	if err == nil || !strings.Contains(err.Error(), "bad?[2J?peer") || strings.Contains(err.Error(), "\x1b") {
		t.Fatalf("unsafe ambiguous-offer diagnostic: %v", err)
	}
}

func TestMessagesRemoveTerminalControls(t *testing.T) {
	if got := cleanMessage("first\n\x1b[2Jsecond\u202e"); got != "first\n?[2Jsecond?" {
		t.Fatalf("unsafe diagnostic: %q", got)
	}
}

func TestFileSinkQuotesReceivedPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("windows refuses control characters in file names")
	}
	s, err := newFileSink(header{Name: "bad\x1b[2J\nname"}, recvOpts{out: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer s.abort()
	where, err := s.commit()
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(where, "\x1b\n") {
		t.Fatalf("unsafe destination message: %q", where)
	}
}

func TestReceiverCancellationStopsPayloadRead(t *testing.T) {
	key := make([]byte, keyLen) // Synthetic test material.
	id := offerID("review-cancel")
	auth, err := newPeerAuth(key, id)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	ready := make(chan struct{})
	release := make(chan struct{})
	serverDone := make(chan error, 1)
	go func() {
		raw, err := ln.Accept()
		if err != nil {
			serverDone <- err
			return
		}
		defer raw.Close()
		raw.SetDeadline(time.Now().Add(3 * time.Second))
		conn, err := auth.accept(tls.Server(raw, auth.serverConfig()))
		if err != nil {
			serverDone <- err
			return
		}
		if err := writeJSONLine(conn, header{V: wireVersion, Kind: kindBlob, Name: "report.txt", From: "bad\x1b[2J\npeer"}); err != nil {
			serverDone <- err
			return
		}
		var rep reply
		if err := readJSONLine(conn.r, &rep); err != nil {
			serverDone <- err
			return
		}
		close(ready)
		<-release
		serverDone <- nil
	}()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var messages bytes.Buffer
	receiving := make(chan struct{})
	recvDone := make(chan error, 1)
	go func() {
		msg := writeFunc(func(b []byte) (int, error) {
			n, err := messages.Write(b)
			if strings.HasPrefix(string(b), "receiving ") {
				close(receiving)
			}
			return n, err
		})
		recvDone <- runRecv(ctx, recvOpts{code: "review-cancel", addr: ln.Addr().String(), key: key, stdout: io.Discard, toStdout: true, msg: msg, silent: true})
	}()
	select {
	case <-ready:
	case err := <-serverDone:
		t.Fatalf("server setup: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("setup timeout")
	}
	select {
	case <-receiving:
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("receiver did not start the payload")
	}
	cancel()
	blocked := false
	select {
	case <-recvDone:
	case <-time.After(2 * time.Second):
		blocked = true
	}
	close(release)
	if blocked {
		<-recvDone
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(messages.String(), "\x1b") || !strings.Contains(messages.String(), "bad?[2J?peer") {
		t.Fatal("receiver printed a terminal escape")
	}
	if blocked {
		t.Fatal("receiver stayed blocked on payload after context cancellation")
	}
}

func TestSenderTimeoutStopsUnauthenticatedPeer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		done <- runSend(ctx, sendOpts{key: make([]byte, keyLen), code: "review-timeout", stdin: bytes.NewReader(nil), out: io.Discard, silent: true, timeout: 100 * time.Millisecond, onReady: func(a net.Addr) {
			_, port, _ := net.SplitHostPort(a.String())
			ready <- net.JoinHostPort("127.0.0.1", port)
		}})
	}()
	raw, err := net.Dial("tcp", <-ready)
	if err != nil {
		t.Fatal(err)
	}
	blocked := false
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		blocked = true
	}
	raw.Close()
	if blocked {
		<-done
	}
	if blocked {
		t.Fatal("an unauthenticated connection kept send alive past --timeout")
	}
}

func TestSenderDoesNotReportRejectedPayloadAsSuccess(t *testing.T) {
	key := make([]byte, keyLen)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ready := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		done <- runSend(ctx, sendOpts{key: key, code: "review-rejected", stdin: bytes.NewReader([]byte("too large")), out: io.Discard, silent: true, timeout: time.Second, onReady: func(a net.Addr) {
			_, port, _ := net.SplitHostPort(a.String())
			ready <- net.JoinHostPort("127.0.0.1", port)
		}})
	}()
	err := runRecv(ctx, recvOpts{code: "review-rejected", addr: <-ready, key: key, stdout: io.Discard, toStdout: true, limit: 1, msg: io.Discard, silent: true})
	if err == nil {
		t.Fatal("receiver accepted an oversized payload")
	}
	if err := <-done; err == nil {
		t.Fatal("sender reported success even though the receiver rejected the payload")
	}
}

func TestSparseArchiveIsRejected(t *testing.T) {
	paxRecord := func(k, v string) string {
		for n := len(k) + len(v) + 3; ; n++ {
			s := fmt.Sprintf("%d %s=%s\n", n, k, v)
			if len(s) == n {
				return s
			}
		}
	}
	var pax string
	for _, kv := range [][2]string{{"GNU.sparse.major", "0"}, {"GNU.sparse.minor", "1"}, {"GNU.sparse.numblocks", "1"}, {"GNU.sparse.map", "0,0"}, {"GNU.sparse.size", "1048576"}} {
		pax += paxRecord(kv[0], kv[1])
	}
	x := tarWith(t, tar.Header{Name: "PaxHeaders/expanded.txt", Mode: 0o600, Typeflag: tar.TypeReg, Size: int64(len(pax))}, pax)
	x = x[:len(x)-1024]
	x[156] = tar.TypeXHeader
	for i := 148; i < 156; i++ {
		x[i] = ' '
	}
	sum := 0
	for _, b := range x[:512] {
		sum += int(b)
	}
	copy(x[148:156], fmt.Sprintf("%06o\x00 ", sum))
	arc := append(x, tarWith(t, tar.Header{Name: "expanded.txt", Mode: 0o600, Typeflag: tar.TypeReg}, "")...)
	if err := extractTar(bytes.NewReader(arc), t.TempDir()); err == nil || !strings.Contains(err.Error(), "sparse") {
		t.Fatalf("sparse archive was not rejected: %v", err)
	}
	var framed bytes.Buffer
	if _, err := sendPayload(&framed, bytes.NewReader(arc)); err != nil {
		t.Fatal(err)
	}
	dest := t.TempDir()
	s, err := newTarSink(recvOpts{out: dest})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := recvPayload(s.w, bufio.NewReader(&framed), 4096); err == nil {
		if _, err := s.commit(); err == nil {
			t.Fatal("receiver published a sparse archive")
		}
	} else {
		s.abort()
	}
	if _, err := os.Stat(filepath.Join(dest, "expanded.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("sparse file reached its destination: %v", err)
	}
	if leftovers := partialFiles(t, dest); len(leftovers) != 0 {
		t.Fatal("rejected sparse archive left staging:", leftovers)
	}
}

package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
)

const (
	wireVersion = 2

	// kindBlob is one file, or a snippet from stdin.
	kindBlob = "blob"
	// kindTar is a tar stream of several paths, or of a directory.
	kindTar = "tar"

	// maxLineLen caps a JSON control line, so a hostile peer cannot make the
	// other side hold an unbounded buffer.
	maxLineLen = 8 << 10

	// chunkLen is the payload chunk the sender writes at a time.
	chunkLen = 64 << 10
	// maxChunkLen caps the chunk the receiver accepts.
	maxChunkLen = 1 << 20
)

// header describes an offer. The sender writes it once, after both sides prove
// they hold the network key.
type header struct {
	V    int    `json:"v"`
	Kind string `json:"kind"`
	Name string `json:"name"`
	Size int64  `json:"size"` // advisory, for the progress line. -1 when unknown.
	Mode uint32 `json:"mode"`
	From string `json:"from"`
}

// reply is the receiver's answer to a header.
type reply struct {
	Accept bool   `json:"accept"`
	Reason string `json:"reason,omitempty"`
}

// receipt confirms that the receiver checked and committed the payload.
type receipt struct {
	Committed bool   `json:"committed"`
	Reason    string `json:"reason,omitempty"`
}

func (h header) validate() error {
	if h.V != wireVersion {
		return fmt.Errorf("peer speaks wire version %d, this build speaks %d", h.V, wireVersion)
	}
	if h.Kind != kindBlob && h.Kind != kindTar {
		return fmt.Errorf("unknown payload kind %q", h.Kind)
	}
	if _, err := safeName(h.Name); err != nil {
		return err
	}
	return nil
}

// safeName checks a name that came from a peer. It accepts a plain file name
// and nothing else, so a name can never reach outside the target directory.
// It does not repair a bad name, because a name that needs repair is a sign
// that something is wrong at the other end.
func safeName(name string) (string, error) {
	if name == "" || name == "." || name != filepath.Base(name) || !filepath.IsLocal(name) {
		return "", fmt.Errorf("unsafe file name %q", name)
	}
	return name, nil
}

func writeJSONLine(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if len(b) > maxLineLen {
		return errors.New("control line too long")
	}
	_, err = w.Write(append(b, '\n'))
	return err
}

func readJSONLine(r *bufio.Reader, v any) error {
	var line []byte
	for {
		b, err := r.ReadByte()
		if err != nil {
			return err
		}
		if b == '\n' {
			break
		}
		if len(line) >= maxLineLen {
			return errors.New("control line too long")
		}
		line = append(line, b)
	}
	return json.Unmarshal(line, v)
}

// sendPayload copies src to w as length-prefixed chunks, ends them with a
// zero-length chunk, then writes the SHA-256 of the payload. It returns the
// number of payload bytes.
func sendPayload(w io.Writer, src io.Reader) (int64, error) {
	sum := sha256.New()
	buf := make([]byte, chunkLen)
	hdr := make([]byte, binary.MaxVarintLen64)
	var total int64

	for {
		n, readErr := src.Read(buf)
		if n > 0 {
			if _, err := w.Write(hdr[:binary.PutUvarint(hdr, uint64(n))]); err != nil {
				return total, err
			}
			if _, err := w.Write(buf[:n]); err != nil {
				return total, err
			}
			sum.Write(buf[:n])
			total += int64(n)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return total, readErr
		}
	}

	if _, err := w.Write(hdr[:binary.PutUvarint(hdr, 0)]); err != nil {
		return total, err
	}
	_, err := w.Write(sum.Sum(nil))
	return total, err
}

// recvPayload reads a framed payload into dst and checks the SHA-256 trailer.
// A limit above zero caps the payload size. It returns the number of bytes
// written to dst, which stay unverified until recvPayload returns nil.
func recvPayload(dst io.Writer, r *bufio.Reader, limit int64) (int64, error) {
	sum := sha256.New()
	out := io.MultiWriter(dst, sum)
	var total int64

	for {
		n, err := binary.ReadUvarint(r)
		if err != nil {
			return total, fmt.Errorf("reading chunk length: %w", err)
		}
		if n == 0 {
			break
		}
		if n > maxChunkLen {
			return total, fmt.Errorf("peer sent a %d byte chunk, the cap is %d", n, maxChunkLen)
		}
		if limit > 0 && total+int64(n) > limit {
			return total, fmt.Errorf("payload is larger than the %d byte limit", limit)
		}
		if _, err := io.CopyN(out, r, int64(n)); err != nil {
			return total, fmt.Errorf("reading chunk: %w", err)
		}
		total += int64(n)
	}

	want := make([]byte, sha256.Size)
	if _, err := io.ReadFull(r, want); err != nil {
		return total, fmt.Errorf("reading checksum: %w", err)
	}
	if !bytes.Equal(want, sum.Sum(nil)) {
		return total, errors.New("checksum mismatch: the transfer was corrupted")
	}
	return total, nil
}

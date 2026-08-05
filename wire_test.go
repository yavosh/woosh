package main

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"io"
	"strings"
	"testing"
)

func TestJSONLineRoundTrip(t *testing.T) {
	want := header{V: wireVersion, Kind: kindBlob, Name: "report.pdf", Size: 10482, Mode: 0o644, From: "mbp"}

	var buf bytes.Buffer
	if err := writeJSONLine(&buf, want); err != nil {
		t.Fatal(err)
	}
	// A second line must survive, the reader must not swallow it.
	if err := writeJSONLine(&buf, reply{Accept: true}); err != nil {
		t.Fatal(err)
	}

	r := bufio.NewReader(&buf)
	var got header
	if err := readJSONLine(r, &got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("header round trip:\n got %+v\nwant %+v", got, want)
	}

	var rep reply
	if err := readJSONLine(r, &rep); err != nil {
		t.Fatal(err)
	}
	if !rep.Accept {
		t.Error("reply round trip lost Accept")
	}
}

func TestReadJSONLineRejectsLongLine(t *testing.T) {
	long := strings.Repeat("x", maxLineLen+10)
	r := bufio.NewReader(strings.NewReader(long))
	var h header
	if err := readJSONLine(r, &h); err == nil {
		t.Error("readJSONLine accepted an oversize line")
	}
}

func TestHeaderValidate(t *testing.T) {
	ok := header{V: wireVersion, Kind: kindBlob, Name: "a.txt"}
	if err := ok.validate(); err != nil {
		t.Fatalf("valid header rejected: %v", err)
	}

	bad := map[string]header{
		"wrong version": {V: 99, Kind: kindBlob, Name: "a.txt"},
		"unknown kind":  {V: wireVersion, Kind: "zip", Name: "a.txt"},
		"path in name":  {V: wireVersion, Kind: kindBlob, Name: "../../etc/passwd"},
		"absolute name": {V: wireVersion, Kind: kindBlob, Name: "/etc/passwd"},
		"empty name":    {V: wireVersion, Kind: kindBlob, Name: ""},
		"dotdot name":   {V: wireVersion, Kind: kindBlob, Name: ".."},
	}
	for name, h := range bad {
		if err := h.validate(); err == nil {
			t.Errorf("%s: header accepted", name)
		}
	}
}

func TestSafeName(t *testing.T) {
	if got, err := safeName("report.pdf"); err != nil || got != "report.pdf" {
		t.Errorf("safeName(report.pdf) = %q, %v", got, err)
	}
	for _, bad := range []string{"", ".", "..", "/", "../..", "/etc/passwd", "dir/report.pdf"} {
		if got, err := safeName(bad); err == nil {
			t.Errorf("safeName(%q) accepted, gave %q", bad, got)
		}
	}
}

func TestPayloadRoundTrip(t *testing.T) {
	sizes := []int{0, 1, chunkLen - 1, chunkLen, chunkLen + 1, 3 * chunkLen}
	for _, size := range sizes {
		src := make([]byte, size)
		if _, err := rand.Read(src); err != nil {
			t.Fatal(err)
		}

		var wire bytes.Buffer
		n, err := sendPayload(&wire, bytes.NewReader(src))
		if err != nil {
			t.Fatalf("size %d: %v", size, err)
		}
		if n != int64(size) {
			t.Errorf("size %d: sendPayload reported %d bytes", size, n)
		}

		var got bytes.Buffer
		n, err = recvPayload(&got, bufio.NewReader(&wire), 0)
		if err != nil {
			t.Fatalf("size %d: %v", size, err)
		}
		if n != int64(size) {
			t.Errorf("size %d: recvPayload reported %d bytes", size, n)
		}
		if !bytes.Equal(got.Bytes(), src) {
			t.Errorf("size %d: payload changed", size)
		}
	}
}

func TestRecvPayloadDetectsCorruption(t *testing.T) {
	var wire bytes.Buffer
	if _, err := sendPayload(&wire, strings.NewReader("hello world")); err != nil {
		t.Fatal(err)
	}
	b := wire.Bytes()
	b[3] ^= 0xff // flip a bit inside the payload, the checksum must catch it

	if _, err := recvPayload(io.Discard, bufio.NewReader(bytes.NewReader(b)), 0); err == nil {
		t.Error("recvPayload accepted a corrupted payload")
	}
}

func TestRecvPayloadDetectsTruncation(t *testing.T) {
	var wire bytes.Buffer
	if _, err := sendPayload(&wire, strings.NewReader("hello world")); err != nil {
		t.Fatal(err)
	}
	short := wire.Bytes()[:wire.Len()-4] // lose part of the checksum

	if _, err := recvPayload(io.Discard, bufio.NewReader(bytes.NewReader(short)), 0); err == nil {
		t.Error("recvPayload accepted a truncated stream")
	}
}

func TestRecvPayloadHonoursLimit(t *testing.T) {
	var wire bytes.Buffer
	if _, err := sendPayload(&wire, bytes.NewReader(make([]byte, 4096))); err != nil {
		t.Fatal(err)
	}
	if _, err := recvPayload(io.Discard, bufio.NewReader(&wire), 1024); err == nil {
		t.Error("recvPayload ignored the size limit")
	}
}

func TestRecvPayloadRejectsHugeChunk(t *testing.T) {
	// Hand-build a frame that claims a chunk larger than the cap.
	var wire bytes.Buffer
	wire.Write([]byte{0xff, 0xff, 0xff, 0xff, 0x0f}) // uvarint, far above maxChunkLen

	if _, err := recvPayload(io.Discard, bufio.NewReader(&wire), 0); err == nil {
		t.Error("recvPayload accepted an oversize chunk length")
	}
}

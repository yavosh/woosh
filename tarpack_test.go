package main

import (
	"archive/tar"
	"bytes"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestPackAndExtractTree(t *testing.T) {
	src := t.TempDir()
	writeFile(t, filepath.Join(src, "notes", "a.txt"), "alpha", 0o644)
	writeFile(t, filepath.Join(src, "notes", "deep", "b.txt"), "bravo", 0o600)
	writeFile(t, filepath.Join(src, "todo.txt"), "charlie", 0o644)

	var arc bytes.Buffer
	err := packPaths(&arc, []string{filepath.Join(src, "notes"), filepath.Join(src, "todo.txt")}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}

	dest := t.TempDir()
	if err := extractTar(&arc, dest); err != nil {
		t.Fatal(err)
	}

	want := map[string]string{
		"notes/a.txt":      "alpha",
		"notes/deep/b.txt": "bravo",
		"todo.txt":         "charlie",
	}
	for name, content := range want {
		got, err := os.ReadFile(filepath.Join(dest, filepath.FromSlash(name)))
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if string(got) != content {
			t.Errorf("%s: got %q, want %q", name, got, content)
		}
	}

	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(dest, "notes", "deep", "b.txt"))
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("b.txt mode is %o, want 600", perm)
		}
	}
}

func TestPackSkipsIrregularFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need extra rights on windows")
	}
	src := t.TempDir()
	writeFile(t, filepath.Join(src, "real.txt"), "data", 0o644)
	if err := os.Symlink("real.txt", filepath.Join(src, "link.txt")); err != nil {
		t.Fatal(err)
	}

	var arc, skipped bytes.Buffer
	if err := packPaths(&arc, []string{src}, &skipped); err != nil {
		t.Fatal(err)
	}
	if skipped.Len() == 0 {
		t.Error("the symlink was not reported as skipped")
	}
	for _, name := range tarNames(t, arc.Bytes()) {
		if filepath.Base(name) == "link.txt" {
			t.Error("the symlink went into the archive")
		}
	}
}

func TestPackRejectsClashingNames(t *testing.T) {
	src := t.TempDir()
	writeFile(t, filepath.Join(src, "one", "x.txt"), "1", 0o644)
	writeFile(t, filepath.Join(src, "two", "x.txt"), "2", 0o644)

	err := packPaths(io.Discard, []string{
		filepath.Join(src, "one", "x.txt"),
		filepath.Join(src, "two", "x.txt"),
	}, io.Discard)
	if err == nil {
		t.Error("packPaths accepted two paths with the same base name")
	}
}

func TestExtractRefusesEscapingEntries(t *testing.T) {
	bad := []string{"../escape.txt", "notes/../../escape.txt", "/etc/passwd"}
	for _, name := range bad {
		dest := t.TempDir()
		if err := extractTar(bytes.NewReader(tarWith(t, tar.Header{
			Typeflag: tar.TypeReg, Name: name, Mode: 0o644, Size: 3,
		}, "bad")), dest); err == nil {
			t.Errorf("entry %q was accepted", name)
		}
	}
}

func TestExtractRefusesLinks(t *testing.T) {
	dest := t.TempDir()
	err := extractTar(bytes.NewReader(tarWith(t, tar.Header{
		Typeflag: tar.TypeSymlink, Name: "link", Linkname: "/etc/passwd", Mode: 0o777,
	}, "")), dest)
	if err == nil {
		t.Error("a symlink entry was accepted")
	}
}

func TestEntryPath(t *testing.T) {
	dest := filepath.Join("tmp", "out")
	if got, err := entryPath(dest, "notes/a.txt"); err != nil || got != filepath.Join(dest, "notes", "a.txt") {
		t.Errorf("entryPath(notes/a.txt) = %q, %v", got, err)
	}
	if got, err := entryPath(dest, "notes/"); err != nil || got != filepath.Join(dest, "notes") {
		t.Errorf("entryPath(notes/) = %q, %v", got, err)
	}
	for _, bad := range []string{"..", "../x", "/x", ""} {
		if got, err := entryPath(dest, bad); err == nil {
			t.Errorf("entryPath(%q) accepted, gave %q", bad, got)
		}
	}
}

func writeFile(t *testing.T, path, content string, perm fs.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), perm); err != nil {
		t.Fatal(err)
	}
}

func tarWith(t *testing.T, hdr tar.Header, body string) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&hdr); err != nil {
		t.Fatal(err)
	}
	if body != "" {
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func tarNames(t *testing.T, arc []byte) []string {
	t.Helper()
	var names []string
	tr := tar.NewReader(bytes.NewReader(arc))
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return names
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, hdr.Name)
	}
}

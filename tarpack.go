package main

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// packPaths writes the given files and directories to w as a tar stream. Each
// root keeps its base name, so "send ./notes ./todo.txt" gives entries under
// "notes/" plus "todo.txt".
//
// Only regular files and directories go in. packPaths reports anything else,
// such as a symlink or a socket, to skipped.
func packPaths(w io.Writer, paths []string, skipped io.Writer) error {
	tw := tar.NewWriter(w)
	roots := make(map[string]string, len(paths))

	for _, root := range paths {
		root = filepath.Clean(root)
		base := filepath.Base(root)
		if base == "." || base == string(filepath.Separator) {
			return fmt.Errorf("cannot send %q: it has no name to give the receiver", root)
		}
		if other, dup := roots[base]; dup {
			return fmt.Errorf("%q and %q would both arrive as %q", other, root, base)
		}
		roots[base] = root

		// WalkDir lstats the root, so a root that is a symlink would be
		// reported as skipped and the receiver would get an empty tree. Walk
		// what the link points at instead. The receiver still sees the name
		// that was typed, and a symlink inside the tree is still skipped.
		walk, err := filepath.EvalSymlinks(root)
		if err != nil {
			return err
		}

		err = filepath.WalkDir(walk, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(walk, p)
			if err != nil {
				return err
			}
			name := base
			if rel != "." {
				name = path.Join(base, filepath.ToSlash(rel))
			}
			return writeTarEntry(tw, p, name, d, skipped)
		})
		if err != nil {
			return err
		}
	}
	return tw.Close()
}

func writeTarEntry(tw *tar.Writer, p, name string, d fs.DirEntry, skipped io.Writer) error {
	info, err := d.Info()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() && !info.IsDir() {
		fmt.Fprintf(skipped, "woosh: skipped %s (%s)\n", p, info.Mode().Type())
		return nil
	}

	hdr, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return err
	}
	hdr.Name = name
	if info.IsDir() {
		hdr.Name += "/"
	}
	// Ownership belongs to the sending machine, so leave it out.
	hdr.Uid, hdr.Gid, hdr.Uname, hdr.Gname = 0, 0, "", ""

	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	if info.IsDir() {
		return nil
	}

	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	n, err := io.Copy(tw, f)
	if err != nil {
		return err
	}
	if n != info.Size() {
		return fmt.Errorf("%s changed size while it was read", p)
	}
	return nil
}

// extractTar unpacks a tar stream below dest. It accepts regular files and
// directories only, and it refuses any entry that points outside dest.
func extractTar(r io.Reader, dest string) error {
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		for key := range hdr.PAXRecords {
			if strings.HasPrefix(key, "GNU.sparse.") || key == "SCHILY.realsize" {
				return fmt.Errorf("refused %q: sparse archives are not supported", hdr.Name)
			}
		}

		target, err := entryPath(dest, hdr.Name)
		if err != nil {
			return err
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, fs.FileMode(hdr.Mode).Perm()|0o700); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			if err := writeTarFile(target, tr, fs.FileMode(hdr.Mode).Perm()); err != nil {
				return err
			}
		default:
			return fmt.Errorf("refused %q: woosh sends regular files and directories only", hdr.Name)
		}
	}
}

func writeTarFile(target string, r io.Reader, perm fs.FileMode) error {
	if perm == 0 {
		perm = 0o644
	}
	f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// entryPath turns a tar entry name into a path below dest. It rejects the
// entry, rather than repairing the name, when the name is absolute or when it
// steps above the entry root.
func entryPath(dest, name string) (string, error) {
	rel := filepath.FromSlash(strings.TrimSuffix(name, "/"))
	if !filepath.IsLocal(rel) {
		return "", fmt.Errorf("refused entry %q: it points outside the target directory", name)
	}
	return filepath.Join(dest, rel), nil
}

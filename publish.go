package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// moveInto preflights every entry, then publishes without replacing late arrivals.
// Sources and replaced entries stay in staging until publication succeeds.
func moveInto(staging, dest string, force bool) error {
	entries, err := os.ReadDir(staging)
	if err != nil {
		return err
	}
	for _, e := range entries {
		target := filepath.Join(dest, e.Name())
		if _, err := os.Lstat(target); err == nil && !force {
			return fmt.Errorf("%s is already there, use --force to replace it", target)
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}

	backup, err := os.MkdirTemp(staging, ".woosh-backup-")
	if err != nil {
		return err
	}
	var replaced []string
	var published publication
	rollback := func(cause error) error {
		err := published.rollback()
		for _, name := range replaced {
			var restored publication
			restoreErr := restored.add(filepath.Join(backup, name), filepath.Join(dest, name))
			if restoreErr == nil {
				restoreErr = restored.finish()
			}
			if restoreErr != nil {
				restoreErr = errors.Join(restoreErr, restored.rollback())
			}
			err = errors.Join(err, restoreErr)
		}
		if err != nil {
			return errors.Join(cause, fmt.Errorf("rollback incomplete; original files remain in %s: %w", backup, err))
		}
		return cause
	}

	for _, e := range entries {
		target := filepath.Join(dest, e.Name())
		if force {
			if err := os.Rename(target, filepath.Join(backup, e.Name())); err == nil {
				replaced = append(replaced, e.Name())
			} else if !errors.Is(err, os.ErrNotExist) {
				return rollback(err)
			}
		}
		if err := published.add(filepath.Join(staging, e.Name()), target); err != nil {
			return rollback(err)
		}
	}
	if err := published.finish(); err != nil {
		return rollback(err)
	}
	if err := os.RemoveAll(staging); err != nil {
		return &cleanupError{staging, err}
	}
	return nil
}

// cleanupError means publication succeeded, but staging cleanup needs attention.
type cleanupError struct {
	staging string
	err     error
}

func (e *cleanupError) Error() string {
	return fmt.Sprintf("files committed; could not remove staging %q: %v", e.staging, e.err)
}

func (e *cleanupError) Unwrap() error { return e.err }

type publishedPath struct {
	path string
	info fs.FileInfo
	mode fs.FileMode
}

// publication records only entries this transfer created. Files are hard-linked
// and directories are exclusively created; neither operation replaces a target.
type publication struct {
	paths []publishedPath
}

func (p *publication) add(source, target string) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	switch {
	case info.IsDir():
		if err := os.Mkdir(target, info.Mode().Perm()|0o700); err != nil {
			return err
		}
		created, err := os.Lstat(target)
		if err != nil {
			return err
		}
		p.paths = append(p.paths, publishedPath{target, created, info.Mode().Perm()})
		entries, err := os.ReadDir(source)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if err := p.add(filepath.Join(source, e.Name()), filepath.Join(target, e.Name())); err != nil {
				return err
			}
		}
	case info.Mode().IsRegular():
		if err := os.Link(source, target); err != nil {
			return err
		}
		p.paths = append(p.paths, publishedPath{target, info, info.Mode().Perm()})
	case info.Mode()&os.ModeSymlink != 0:
		// Only backups can contain symlinks; extractTar rejects incoming links.
		link, err := os.Readlink(source)
		if err != nil {
			return err
		}
		if err := os.Symlink(link, target); err != nil {
			return err
		}
		created, err := os.Lstat(target)
		if err != nil {
			return err
		}
		p.paths = append(p.paths, publishedPath{target, created, info.Mode().Perm()})
	default:
		return fmt.Errorf("cannot publish %s: unsupported file type", source)
	}
	return nil
}

func (p *publication) finish() error {
	for i := len(p.paths) - 1; i >= 0; i-- {
		entry := p.paths[i]
		if entry.info.IsDir() {
			if err := os.Chmod(entry.path, entry.mode); err != nil {
				return err
			}
		}
	}
	return nil
}

func (p *publication) rollback() error {
	var result error
	for _, entry := range p.paths {
		if entry.info.IsDir() {
			if info, err := os.Lstat(entry.path); err == nil && os.SameFile(info, entry.info) {
				result = errors.Join(result, os.Chmod(entry.path, entry.mode|0o700))
			}
		}
	}
	for i := len(p.paths) - 1; i >= 0; i-- {
		entry := p.paths[i]
		info, err := os.Lstat(entry.path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err == nil && !os.SameFile(info, entry.info) {
			err = fmt.Errorf("%s changed during publication; left untouched", entry.path)
		}
		if err == nil {
			err = os.Remove(entry.path)
		}
		result = errors.Join(result, err)
	}
	return result
}

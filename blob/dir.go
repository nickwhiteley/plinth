package blob

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

// Dir is a Bucket over a directory.
//
// It exists so local development runs the same code as production: a feature that needs an
// object store can be built and tested without a deployment. It is the object-store contract,
// on disk.
type Dir struct {
	root string
	// mu makes each operation atomic, matching what a real object store
	// promises — including the compare-and-swap in PutIfMatch. It does not
	// make sequences atomic, so callers race here exactly as they would
	// against Vercel.
	mu sync.Mutex
}

func NewDir(root string) (*Dir, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("blob: creating %s: %w", root, err)
	}
	return &Dir{root: root}, nil
}

var _ Bucket = (*Dir)(nil)

// path maps a key to a file. Keys are slash-separated and must not escape the
// root: a key of "../../etc/passwd" is a caller error, not a path.
func (d *Dir) path(key string) (string, error) {
	clean := filepath.Clean("/" + strings.ReplaceAll(key, "\\", "/"))
	if clean == "/" {
		return "", fmt.Errorf("blob: empty key")
	}
	return filepath.Join(d.root, filepath.FromSlash(clean)), nil
}

func (d *Dir) Get(_ context.Context, key string) (Object, error) {
	name, err := d.path(key)
	if err != nil {
		return Object{}, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()

	data, err := os.ReadFile(name)
	if errors.Is(err, os.ErrNotExist) {
		return Object{}, ErrNoSuchKey
	} else if err != nil {
		return Object{}, fmt.Errorf("blob: reading %s: %w", key, err)
	}
	return Object{Data: data, ETag: etagOf(data)}, nil
}

func (d *Dir) Put(_ context.Context, key string, data []byte) error {
	name, err := d.path(key)
	if err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.write(name, data)
}

func (d *Dir) PutIfMatch(_ context.Context, key string, data []byte, etag string) error {
	name, err := d.path(key)
	if err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()

	existing, err := os.ReadFile(name)
	missing := errors.Is(err, os.ErrNotExist)
	if err != nil && !missing {
		return fmt.Errorf("blob: reading %s: %w", key, err)
	}

	switch {
	case etag == IfAbsent:
		if !missing {
			return ErrPreconditionFailed
		}
	case missing:
		return ErrPreconditionFailed
	case etagOf(existing) != etag:
		return ErrPreconditionFailed
	}
	return d.write(name, data)
}

// write replaces a file atomically, so a reader never sees a half-written
// object. The caller holds the lock.
func (d *Dir) write(name string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		return fmt.Errorf("blob: creating directory: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(name), ".tmp-*")
	if err != nil {
		return fmt.Errorf("blob: creating temp file: %w", err)
	}
	// A no-op once the rename succeeds; on a failure path the write error is
	// what the caller needs to hear, not this one.
	defer func() { _ = os.Remove(tmp.Name()) }()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("blob: writing: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("blob: syncing: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("blob: closing: %w", err)
	}
	return os.Rename(tmp.Name(), name)
}

func (d *Dir) Delete(_ context.Context, key string) error {
	name, err := d.path(key)
	if err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()

	if err := os.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("blob: deleting %s: %w", key, err)
	}
	return nil
}

func (d *Dir) List(_ context.Context, prefix string) ([]string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	var out []string
	err := filepath.WalkDir(d.root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || strings.HasPrefix(entry.Name(), ".tmp-") {
			return nil
		}
		rel, err := filepath.Rel(d.root, path)
		if err != nil {
			return err
		}
		key := filepath.ToSlash(rel)
		if strings.HasPrefix(key, prefix) {
			out = append(out, key)
		}
		return nil
	})
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, fmt.Errorf("blob: listing %s: %w", prefix, err)
	}
	slices.Sort(out)
	return out, nil
}

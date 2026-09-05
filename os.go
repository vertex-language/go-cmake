package cmake

import (
	"io/fs"
	"os"
	"path/filepath"
)

type realFS struct {
	root string
}

func RealFS(root string) FS {
	return &realFS{root: root}
}

func (r *realFS) path(name string) string {
	if filepath.IsAbs(name) {
		return name
	}
	return filepath.Join(r.root, name)
}

func (r *realFS) ReadFile(name string) ([]byte, error) {
	return os.ReadFile(r.path(name))
}

func (r *realFS) WriteFile(name string, data []byte, perm fs.FileMode) error {
	return os.WriteFile(r.path(name), data, perm)
}

func (r *realFS) MkdirAll(name string, perm fs.FileMode) error {
	return os.MkdirAll(r.path(name), perm)
}

func (r *realFS) Glob(pattern string) ([]string, error) {
	return filepath.Glob(r.path(pattern))
}

func (r *realFS) Remove(name string) error {
	return os.Remove(r.path(name))
}

func (r *realFS) Stat(name string) (fs.FileInfo, error) {
	return os.Stat(r.path(name))
}

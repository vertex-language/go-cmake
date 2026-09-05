package eval_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// diskFS is the test filesystem: a real one rooted at a temporary directory.
// The differential tests compare against a cmake binary that can only see real
// files, so an in-memory filesystem would give the two implementations
// different worlds to reason about.
type diskFS struct{}

func (diskFS) ReadFile(name string) ([]byte, error) { return os.ReadFile(name) }

func (diskFS) WriteFile(name string, data []byte) error {
	return os.WriteFile(name, data, 0644)
}

func (diskFS) MkdirAll(name string) error { return os.MkdirAll(name, 0755) }

func (diskFS) Glob(pattern string) ([]string, error) { return filepath.Glob(pattern) }

func (diskFS) Stat(name string) (fs.FileInfo, error) { return os.Stat(name) }

func (diskFS) Remove(name string) error    { return os.Remove(name) }
func (diskFS) RemoveAll(name string) error { return os.RemoveAll(name) }

// memFS is an in-memory filesystem for tests that do not touch the disk.
type memFS struct {
	files map[string]string
}

func newMemFS() *memFS { return &memFS{files: map[string]string{}} }

func (m *memFS) ReadFile(name string) ([]byte, error) {
	if v, ok := m.files[filepath.ToSlash(name)]; ok {
		return []byte(v), nil
	}
	return nil, fs.ErrNotExist
}

func (m *memFS) WriteFile(name string, data []byte) error {
	m.files[filepath.ToSlash(name)] = string(data)
	return nil
}

func (m *memFS) MkdirAll(string) error { return nil }

func (m *memFS) Glob(pattern string) ([]string, error) {
	var out []string
	for name := range m.files {
		if ok, _ := filepath.Match(filepath.ToSlash(pattern), name); ok {
			out = append(out, name)
		}
	}
	return out, nil
}

func (m *memFS) Stat(name string) (fs.FileInfo, error) {
	if _, ok := m.files[filepath.ToSlash(name)]; ok {
		return memInfo{name: filepath.Base(name)}, nil
	}
	return nil, fs.ErrNotExist
}

func (m *memFS) Remove(name string) error {
	delete(m.files, filepath.ToSlash(name))
	return nil
}

func (m *memFS) RemoveAll(name string) error {
	prefix := filepath.ToSlash(name) + "/"
	for path := range m.files {
		if path == filepath.ToSlash(name) || strings.HasPrefix(path, prefix) {
			delete(m.files, path)
		}
	}
	return nil
}

type memInfo struct{ name string }

func (i memInfo) Name() string       { return i.name }
func (i memInfo) Size() int64        { return 0 }
func (i memInfo) Mode() fs.FileMode  { return 0644 }
func (i memInfo) ModTime() time.Time { return time.Time{} }
func (i memInfo) IsDir() bool        { return false }
func (i memInfo) Sys() any           { return nil }

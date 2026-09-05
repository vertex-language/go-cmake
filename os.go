package cmake

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"time"
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

func (r *realFS) ModTime(name string) (time.Time, error) {
	info, err := os.Stat(r.path(name))
	if err != nil {
		return time.Time{}, err
	}
	return info.ModTime(), nil
}

func (r *realFS) Glob(pattern string) ([]string, error) {
	return filepath.Glob(r.path(pattern))
}

func (r *realFS) Remove(name string) error {
	return os.Remove(r.path(name))
}

func (r *realFS) Symlink(old, new string) error {
	return os.Symlink(r.path(old), r.path(new))
}

func (r *realFS) Stat(name string) (fs.FileInfo, error) {
	return os.Stat(r.path(name))
}

type realRunner struct{}

func RealRunner() Runner {
	return &realRunner{}
}

func (r *realRunner) Run(ctx context.Context, cmd Command) error {
	var c *exec.Cmd
	if cmd.Line != "" {
		c = shellCommand(ctx, cmd.Line)
	} else {
		if len(cmd.Argv) == 0 {
			return errors.New("cmake: empty command")
		}
		c = exec.CommandContext(ctx, cmd.Argv[0], cmd.Argv[1:]...)
	}
	c.Dir = cmd.Dir
	c.Env = cmd.Env
	c.Stdin = cmd.Stdin
	c.Stdout = cmd.Stdout
	c.Stderr = cmd.Stderr
	return c.Run()
}

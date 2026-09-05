package cli

import (
	"context"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/vertex-language/go-cmake/run"
)

// `cmake -E` is CMake's portable shell. It exists because a generated build
// file has to copy a file, make a directory, or touch a timestamp on every
// platform, and neither `cp` nor `copy` is available on all of them. Every
// generated rule that does a file operation goes through it, so these commands
// are not a convenience — a build produced by this package will invoke them.

// scriptFS is the filesystem script mode and tool mode use: the real one.
type scriptFS struct{}

func (scriptFS) ReadFile(name string) ([]byte, error) { return os.ReadFile(name) }
func (scriptFS) WriteFile(name string, data []byte) error {
	return os.WriteFile(name, data, 0644)
}
func (scriptFS) MkdirAll(name string) error            { return os.MkdirAll(name, 0755) }
func (scriptFS) Glob(pattern string) ([]string, error) { return filepath.Glob(pattern) }
func (scriptFS) Stat(name string) (fs.FileInfo, error) { return os.Stat(name) }
func (scriptFS) Remove(name string) error              { return os.RemoveAll(name) }

// runToolMode dispatches `cmake -E <command>`.
func runToolMode(ctx context.Context, e Env, args []string) int {
	if len(args) == 0 {
		toolUsage(e.Out)
		return 1
	}
	cmd, rest := args[0], args[1:]

	fail := func(format string, a ...any) int {
		fmt.Fprintf(e.Err, "CMake Error: "+format+"\n", a...)
		return 1
	}

	switch cmd {
	case "echo":
		fmt.Fprintln(e.Out, strings.Join(rest, " "))
		return 0

	case "echo_append":
		fmt.Fprint(e.Out, strings.Join(rest, " "))
		return 0

	case "true":
		return 0
	case "false":
		return 1

	case "capabilities":
		fmt.Fprintf(e.Out, `{"version":{"string":"%s"},"generators":[{"name":"Ninja"}]}`+"\n", Version)
		return 0

	case "make_directory":
		for _, d := range rest {
			if err := os.MkdirAll(d, 0755); err != nil {
				return fail("failed to create directory %q: %v", d, err)
			}
		}
		return 0

	case "remove_directory", "rm":
		return toolRemove(e, cmd, rest, fail)

	case "remove":
		force := false
		for _, f := range rest {
			if f == "-f" {
				force = true
				continue
			}
			if err := os.Remove(f); err != nil && !os.IsNotExist(err) {
				if !force {
					return fail("failed to remove %q: %v", f, err)
				}
			}
		}
		return 0

	case "copy":
		return toolCopy(rest, copyAlways, fail)
	case "copy_if_different":
		return toolCopy(rest, copyIfDifferent, fail)
	case "copy_if_newer":
		return toolCopy(rest, copyIfNewer, fail)

	case "copy_directory":
		return toolCopyDirectory(rest, copyAlways, fail)
	case "copy_directory_if_different":
		return toolCopyDirectory(rest, copyIfDifferent, fail)
	case "copy_directory_if_newer":
		return toolCopyDirectory(rest, copyIfNewer, fail)

	case "create_hardlink":
		if len(rest) != 2 {
			return fail("create_hardlink expects two arguments")
		}
		if err := os.Link(rest[0], rest[1]); err != nil {
			return fail("failed to create hard link: %v", err)
		}
		return 0

	// Commands cmake has that this one does not. Naming them is what keeps a
	// generated build rule that uses one from failing as a typo.
	case "tar":
		return fail("tar is not implemented by this cmake")
	case "bin2c":
		return fail("bin2c is not implemented by this cmake")
	case "delete_regv", "write_regv", "env_vs8_wince", "env_vs9_wince":
		return fail("%s is not implemented by this cmake", cmd)

	case "rename":
		if len(rest) != 2 {
			return fail("rename expects two arguments")
		}
		if err := os.Rename(rest[0], rest[1]); err != nil {
			return fail("failed to rename %q: %v", rest[0], err)
		}
		return 0

	case "touch":
		for _, f := range rest {
			if err := touch(f, true); err != nil {
				return fail("%v", err)
			}
		}
		return 0

	case "touch_nocreate":
		for _, f := range rest {
			if err := touch(f, false); err != nil {
				return fail("%v", err)
			}
		}
		return 0

	case "cat":
		if len(rest) > 0 && rest[0] == "--" {
			rest = rest[1:]
		}
		for _, f := range rest {
			data, err := os.ReadFile(f)
			if err != nil {
				return fail("failed to read %q: %v", f, err)
			}
			e.Out.Write(data)
		}
		return 0

	case "compare_files":
		return toolCompare(rest, fail)

	case "md5sum", "sha1sum", "sha224sum", "sha256sum", "sha384sum", "sha512sum":
		return toolHash(e, cmd, rest, fail)

	case "environment":
		env := append([]string{}, e.Env...)
		sort.Strings(env)
		for _, v := range env {
			fmt.Fprintln(e.Out, v)
		}
		return 0

	case "chdir":
		if len(rest) < 2 {
			return fail("chdir expects a directory and a command")
		}
		return runIn(ctx, e, rest[0], rest[1:], fail)

	case "env":
		return toolEnv(ctx, e, rest, fail)

	case "sleep":
		for _, s := range rest {
			d, err := time.ParseDuration(s + "s")
			if err != nil {
				return fail("sleep expects a number of seconds, got %q", s)
			}
			select {
			case <-time.After(d):
			case <-ctx.Done():
				return 1
			}
		}
		return 0

	case "time":
		if len(rest) == 0 {
			return fail("time expects a command")
		}
		start := time.Now()
		code := runIn(ctx, e, "", rest, fail)
		fmt.Fprintf(e.Out, "Elapsed time: %.2f s\n", time.Since(start).Seconds())
		return code

	case "create_symlink":
		if len(rest) != 2 {
			return fail("create_symlink expects two arguments")
		}
		if err := os.Symlink(rest[0], rest[1]); err != nil {
			return fail("failed to create symlink: %v", err)
		}
		return 0
	}

	fmt.Fprintf(e.Err, "CMake Error: cmake -E does not implement %q\n", cmd)
	toolUsage(e.Err)
	return 1
}

func toolUsage(w io.Writer) {
	fmt.Fprint(w, `Usage: cmake -E <command> [arguments]

Available commands:
  cat <files>...              concatenate files to stdout
  capabilities                print capabilities in JSON
  chdir <dir> <cmd> [args]    run a command in a directory
  compare_files <a> <b>       exit 0 if the files are identical
  copy <files>... <dest> | -t <dest> <files>...
  copy_if_different <files>... <dest>
  copy_if_newer <files>... <dest>
  copy_directory <dirs>... <dest>
  copy_directory_if_different <dirs>... <dest>
  copy_directory_if_newer <dirs>... <dest>
  create_symlink <old> <new>
  create_hardlink <old> <new>
  echo [args]...              print arguments
  echo_append [args]...       print without a trailing newline
  env [--unset=NAME]... [NAME=VALUE]... <cmd>
                              run a command with a modified environment
  environment                 print the environment
  false                       exit non-zero
  make_directory <dirs>...    create directories
  md5sum|sha1sum|sha224sum|sha256sum|sha384sum|sha512sum <files>...
  remove [-f] <files>...      remove files
  remove_directory <dirs>...  remove directories
  rename <old> <new>
  rm [-rRf] <paths>...        remove files and directories
  sleep <seconds>...
  time <cmd> [args]...        run a command and report its duration
  touch <files>...            create or update a timestamp
  touch_nocreate <files>...   update a timestamp only if the file exists
  true                        exit zero
`)
}

func toolRemove(e Env, cmd string, args []string, fail func(string, ...any) int) int {
	force := cmd == "remove_directory"
	recursive := cmd == "remove_directory"
	var paths []string
	for _, a := range args {
		if strings.HasPrefix(a, "-") && cmd == "rm" {
			for _, c := range a[1:] {
				switch c {
				case 'f':
					force = true
				case 'r', 'R':
					recursive = true
				}
			}
			continue
		}
		paths = append(paths, a)
	}
	for _, p := range paths {
		var err error
		if recursive {
			err = os.RemoveAll(p)
		} else {
			err = os.Remove(p)
		}
		if err != nil && !os.IsNotExist(err) && !force {
			return fail("failed to remove %q: %v", p, err)
		}
	}
	return 0
}

// toolCopy copies files to a destination, which is a directory when more than
// one source is named.
// copyMode says when a copy actually writes.
type copyMode int

const (
	copyAlways copyMode = iota
	copyIfDifferent
	copyIfNewer
)

// splitCopyArgs separates the sources from the destination. cmake accepts two
// spellings -- trailing destination, or `-t <destination>` first -- and the
// second exists so that a single source can go to a directory without the
// command being ambiguous about which argument is which.
func splitCopyArgs(args []string) (sources []string, dest string, ok bool) {
	if len(args) >= 2 && args[0] == "-t" {
		return args[2:], args[1], len(args) > 2
	}
	if len(args) < 2 {
		return nil, "", false
	}
	return args[:len(args)-1], args[len(args)-1], true
}

func toolCopy(args []string, mode copyMode, fail func(string, ...any) int) int {
	sources, dest, ok := splitCopyArgs(args)
	if !ok {
		return fail("copy expects at least a source and a destination")
	}
	destIsDir := len(sources) > 1
	if fi, err := os.Stat(dest); err == nil && fi.IsDir() {
		destIsDir = true
	}
	for _, src := range sources {
		target := dest
		if destIsDir {
			target = filepath.Join(dest, filepath.Base(src))
		}
		if err := copyFile(src, target, mode); err != nil {
			return fail("%v", err)
		}
	}
	return 0
}

func toolCopyDirectory(args []string, mode copyMode, fail func(string, ...any) int) int {
	sources, dest, ok := splitCopyArgs(args)
	if !ok {
		return fail("copy_directory expects at least a source and a destination")
	}
	for _, src := range sources {
		if err := copyTree(src, dest, mode); err != nil {
			return fail("%v", err)
		}
	}
	return 0
}

// copyFile copies one file, optionally skipping the write when the destination
// already holds the same bytes. Skipping matters: rewriting an unchanged
// generated header would advance its timestamp and rebuild everything that
// includes it.
func copyFile(src, dst string, mode copyMode) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("failed to copy %q: %v", src, err)
	}
	switch mode {
	case copyIfDifferent:
		if existing, err := os.ReadFile(dst); err == nil && string(existing) == string(data) {
			return nil
		}
	case copyIfNewer:
		// Skipping an equal timestamp as well as an older one is what makes a
		// second run of a generated rule do nothing.
		srcInfo, err1 := os.Stat(src)
		dstInfo, err2 := os.Stat(dst)
		if err1 == nil && err2 == nil && !srcInfo.ModTime().After(dstInfo.ModTime()) {
			return nil
		}
	}
	if dir := filepath.Dir(dst); dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("failed to create %q: %v", dir, err)
		}
	}
	info, err := os.Stat(src)
	perm := fs.FileMode(0644)
	if err == nil {
		perm = info.Mode().Perm()
	}
	if err := os.WriteFile(dst, data, perm); err != nil {
		return fmt.Errorf("failed to write %q: %v", dst, err)
	}
	return nil
}

func copyTree(src, dst string, mode copyMode) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0755)
		}
		return copyFile(path, target, mode)
	})
}

func touch(path string, create bool) error {
	if _, err := os.Stat(path); err != nil {
		if !create {
			return nil
		}
		if dir := filepath.Dir(path); dir != "" {
			if err := os.MkdirAll(dir, 0755); err != nil {
				return fmt.Errorf("failed to create %q: %v", dir, err)
			}
		}
		f, err := os.Create(path)
		if err != nil {
			return fmt.Errorf("failed to create %q: %v", path, err)
		}
		return f.Close()
	}
	now := time.Now()
	if err := os.Chtimes(path, now, now); err != nil {
		return fmt.Errorf("failed to touch %q: %v", path, err)
	}
	return nil
}

func toolCompare(args []string, fail func(string, ...any) int) int {
	ignoreEOL := false
	var files []string
	for _, a := range args {
		if a == "--ignore-eol" {
			ignoreEOL = true
			continue
		}
		files = append(files, a)
	}
	if len(files) != 2 {
		return fail("compare_files expects two files")
	}
	a, errA := os.ReadFile(files[0])
	b, errB := os.ReadFile(files[1])
	if errA != nil || errB != nil {
		return 1
	}
	sa, sb := string(a), string(b)
	if ignoreEOL {
		sa = strings.ReplaceAll(strings.ReplaceAll(sa, "\r\n", "\n"), "\r", "\n")
		sb = strings.ReplaceAll(strings.ReplaceAll(sb, "\r\n", "\n"), "\r", "\n")
	}
	if sa != sb {
		return 1
	}
	return 0
}

func toolHash(e Env, cmd string, files []string, fail func(string, ...any) int) int {
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return fail("failed to read %q: %v", f, err)
		}
		var sum string
		switch cmd {
		case "md5sum":
			h := md5.Sum(data)
			sum = hex.EncodeToString(h[:])
		case "sha1sum":
			h := sha1.Sum(data)
			sum = hex.EncodeToString(h[:])
		case "sha224sum":
			h := sha256.Sum224(data)
			sum = hex.EncodeToString(h[:])
		case "sha256sum":
			h := sha256.Sum256(data)
			sum = hex.EncodeToString(h[:])
		case "sha384sum":
			h := sha512.Sum384(data)
			sum = hex.EncodeToString(h[:])
		default:
			h := sha512.Sum512(data)
			sum = hex.EncodeToString(h[:])
		}
		fmt.Fprintf(e.Out, "%s  %s\n", sum, f)
	}
	return 0
}

// toolEnv runs a command with environment variables set or removed.
func toolEnv(ctx context.Context, e Env, args []string, fail func(string, ...any) int) int {
	env := append([]string{}, e.Env...)
	i := 0
	for ; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--unset" && i+1 < len(args):
			i++
			env = removeEnv(env, args[i])
		case strings.HasPrefix(a, "--unset="):
			env = removeEnv(env, a[len("--unset="):])
		case strings.Contains(a, "=") && !strings.HasPrefix(a, "-"):
			env = append(removeEnv(env, a[:strings.IndexByte(a, '=')]), a)
		default:
			goto run
		}
	}
run:
	if i >= len(args) {
		// With no command, print the environment it would have used.
		sort.Strings(env)
		for _, v := range env {
			fmt.Fprintln(e.Out, v)
		}
		return 0
	}
	child := e
	child.Env = env
	return runIn(ctx, child, "", args[i:], fail)
}

func removeEnv(env []string, name string) []string {
	prefix := name + "="
	out := env[:0]
	for _, v := range env {
		if !strings.HasPrefix(v, prefix) {
			out = append(out, v)
		}
	}
	return out
}

// runIn executes a command, returning its exit status. `cmake -E chdir dir
// prog` reports what prog reported, so a non-zero exit is passed through rather
// than collapsed into a generic failure.
func runIn(ctx context.Context, e Env, dir string, argv []string, fail func(string, ...any) int) int {
	code, err := run.OS().Run(ctx, command(argv, dir, e))
	if err != nil {
		return fail("%v", err)
	}
	return code
}

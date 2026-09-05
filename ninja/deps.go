package ninja

import (
	"bufio"
	"fmt"
	"io"
	"sort"
	"strings"
)

// DepsLog records the header dependencies a compiler reported.
//
// Upstream ninja stores these in a packed binary format built for incremental
// append. This implementation uses a text one: the file is rewritten whole at
// the end of a build, which costs a rewrite proportional to the project size
// and buys a format a person can read when a build rebuilds something it
// should not have.
//
// The record shape is one output line followed by tab-indented dependency
// lines:
//
//	C:/build/main.c.obj
//		C:/Program Files (x86)/Windows Kits/10/Include/ucrt/stdio.h
//		C:/project/value.h
//
// One dependency per line, because paths contain spaces. Putting several on a
// line separated by spaces looks tidier and is wrong: every path under
// "C:/Program Files" comes back as three paths that do not exist, so every
// object looks stale and the build recompiles everything on every run — while
// appearing to work perfectly.
type DepsLog struct {
	Deps map[string][]string // output -> dependencies
}

// NewDepsLog returns an empty log.
func NewDepsLog() *DepsLog {
	return &DepsLog{Deps: make(map[string][]string)}
}

// Add records the dependencies of one output.
func (d *DepsLog) Add(output string, deps []string) {
	d.Deps[output] = deps
}

// Get returns the recorded dependencies of an output.
func (d *DepsLog) Get(output string) []string {
	return d.Deps[output]
}

// All returns every recorded output and its dependencies.
func (d *DepsLog) All() map[string][]string { return d.Deps }

// ReadDepsLog parses a dependency log.
func ReadDepsLog(r io.Reader) (*DepsLog, error) {
	log := NewDepsLog()
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	current := ""
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if line[0] == '\t' {
			if current == "" {
				// A dependency with no output above it: the file is damaged.
				// Skipping the line costs one unnecessary rebuild, which is the
				// safe direction to be wrong in.
				continue
			}
			log.Deps[current] = append(log.Deps[current], line[1:])
			continue
		}
		current = line
		if _, ok := log.Deps[current]; !ok {
			log.Deps[current] = nil
		}
	}
	return log, scanner.Err()
}

// Write serialises the log. Outputs are written in sorted order so that two
// builds of the same project produce the same file.
func (d *DepsLog) Write(w io.Writer) error {
	if _, err := fmt.Fprintln(w, "# go-cmake deps v1"); err != nil {
		return err
	}
	outputs := make([]string, 0, len(d.Deps))
	for out := range d.Deps {
		outputs = append(outputs, out)
	}
	sort.Strings(outputs)
	for _, out := range outputs {
		if _, err := fmt.Fprintln(w, out); err != nil {
			return err
		}
		for _, dep := range d.Deps[out] {
			if _, err := fmt.Fprintf(w, "\t%s\n", dep); err != nil {
				return err
			}
		}
	}
	return nil
}

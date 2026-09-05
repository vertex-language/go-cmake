package ninja

import (
	"bufio"
	"fmt"
	"hash/fnv"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The build log is what makes a build correct across changes that leave no
// trace in the filesystem. Two of them matter:
//
//   - The command changed. Editing a compile flag alters no timestamp, so
//     without a record of the command that produced each output the build would
//     keep objects compiled with the old flags.
//   - A header changed. The build file does not know which headers a source
//     includes; the compiler discovers that while compiling. The dependencies
//     it reports are recorded here and consulted on the next run.
//
// The file format is ninja's log v5, with one deliberate difference: the
// command field holds this implementation's hash, not upstream ninja's. Sharing
// a build directory between the two therefore costs one extra rebuild each time
// the tool changes, which is a better failure than a silently stale object.

// LogEntry records one output's last successful build.
type LogEntry struct {
	StartTime   int64
	EndTime     int64
	MTime       int64
	Output      string
	CommandHash uint64
}

// Log is the build log: what was built, with which command, and what it turned
// out to depend on.
type Log struct {
	// The build runs edges in parallel, and each one records its command and
	// its discovered dependencies as it finishes. The mutex is not incidental:
	// without it a build with -j2 or more corrupts the map and takes the
	// process down, and it does so intermittently, which is the worst way for
	// a build tool to fail.
	mu      sync.Mutex
	Entries map[string]*LogEntry
	Deps    map[string][]string
}

// NewLog returns an empty log.
func NewLog() *Log {
	return &Log{Entries: map[string]*LogEntry{}, Deps: map[string][]string{}}
}

// ReadLog parses a .ninja_log.
func ReadLog(r io.Reader) (*Log, error) {
	log := NewLog()
	scanner := bufio.NewScanner(r)
	// Compile lines can be long; the default 64K token limit is not enough for
	// a log whose paths are deep.
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "#") {
			continue
		}
		processLogLine(log, line)
	}
	return log, scanner.Err()
}

func processLogLine(log *Log, line string) {
	parts := strings.Split(line, "\t")
	if len(parts) < 4 {
		return
	}
	start, _ := strconv.ParseInt(parts[0], 10, 64)
	end, _ := strconv.ParseInt(parts[1], 10, 64)
	mtime, _ := strconv.ParseInt(parts[2], 10, 64)
	output := parts[3]
	var hash uint64
	if len(parts) >= 5 {
		hash, _ = strconv.ParseUint(parts[4], 16, 64)
	}
	log.Entries[output] = &LogEntry{
		StartTime:   start,
		EndTime:     end,
		MTime:       mtime,
		Output:      output,
		CommandHash: hash,
	}
}

// Write serialises the log.
func (l *Log) Write(w io.Writer) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, err := fmt.Fprintf(w, "# ninja log v5\n"); err != nil {
		return err
	}
	for _, entry := range l.Entries {
		if _, err := fmt.Fprintf(w, "%d\t%d\t%d\t%s\t%x\n",
			entry.StartTime, entry.EndTime, entry.MTime, entry.Output, entry.CommandHash); err != nil {
			return err
		}
	}
	return nil
}

// RecordCommand notes that output was produced by command.
func (l *Log) RecordCommand(output, command string, at time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.Entries == nil {
		l.Entries = map[string]*LogEntry{}
	}
	ms := at.UnixMilli()
	l.Entries[output] = &LogEntry{
		StartTime:   ms,
		EndTime:     ms,
		MTime:       at.UnixNano(),
		Output:      output,
		CommandHash: hashCommand(command),
	}
}

// Command reports whether output's recorded command matches the given one.
// It returns the recorded command's identity rather than its text, because the
// log stores a hash: keeping every command line would make the log as large as
// the build file for no gain.
func (l *Log) Command(output string) (uint64, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.Entries[output]
	if !ok {
		return 0, false
	}
	return e.CommandHash, true
}

// CommandMatches reports whether output was last built by this command.
func (l *Log) CommandMatches(output, command string) bool {
	h, ok := l.Command(output)
	return ok && h == hashCommand(command)
}

// EachDep calls f for every recorded output and its dependencies.
func (l *Log) EachDep(f func(output string, deps []string)) {
	l.mu.Lock()
	snapshot := make(map[string][]string, len(l.Deps))
	for k, v := range l.Deps {
		snapshot[k] = v
	}
	l.mu.Unlock()
	for k, v := range snapshot {
		f(k, v)
	}
}

// RecordDeps stores the dependencies a compiler discovered for an output.
func (l *Log) RecordDeps(output string, deps []string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.Deps == nil {
		l.Deps = map[string][]string{}
	}
	l.Deps[output] = deps
}

// DepsFor returns the recorded dependencies of an output.
func (l *Log) DepsFor(output string) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.Deps[output]
}

func hashCommand(command string) uint64 {
	h := fnv.New64a()
	_, _ = io.WriteString(h, command)
	return h.Sum64()
}

// HasDeps reports whether any dependencies were recorded.
func (l *Log) HasDeps() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.Deps) > 0
}

// Package ctest runs the tests a configured project declared.
//
// It reads them rather than being told them: generate wrote a
// CTestTestfile.cmake into each directory of the build tree, and those files
// are CMake scripts whose only commands are add_test and set_tests_properties.
// Evaluating one is how the test list is recovered, which means the test runner
// needs no format of its own and cannot drift from what configure decided.
//
// Running a test is running a process and looking at its exit status. Almost
// everything else here is about the properties that change what that status
// means: WILL_FAIL inverts it, SKIP_RETURN_CODE removes the test from the
// verdict, and DISABLED means it does not run at all.
package ctest

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vertex-language/go-cmake/eval"
	"github.com/vertex-language/go-cmake/run"
)

// Config is a test run.
type Config struct {
	// BinaryDir is the build tree to test. Its CTestTestfile.cmake is the root
	// of the test list.
	BinaryDir string

	// Include and Exclude select tests by name; IncludeLabel and ExcludeLabel
	// select them by label. All four are regular expressions, as ctest's -R,
	// -E, -L and -LE are.
	Include      string
	Exclude      string
	IncludeLabel string
	ExcludeLabel string

	// Jobs is how many tests run at once. Zero means one at a time, which is
	// ctest's default: tests routinely share a working directory or a port, and
	// running them in parallel is opt-in for that reason.
	Jobs int

	// OutputOnFailure prints a failing test's output, which is otherwise kept.
	OutputOnFailure bool

	// StopOnFailure abandons the run at the first failure.
	StopOnFailure bool

	// ShowOnly lists the tests instead of running them.
	ShowOnly bool

	// Repeat runs each test up to this many times while it fails.
	Repeat int

	Runner run.Runner
	Env    []string
	Out    io.Writer
	Err    io.Writer
}

// Status is what became of one test.
//
// Skipped and NotRun are separate because they mean different things to the
// tally: a disabled test never ran and is left out of the totals entirely,
// while a skipped one ran, decided it had nothing to do, and counts as not
// having failed.
type Status int

const (
	Passed Status = iota
	Failed
	Skipped
	NotRun // disabled
)

// Result is one test's outcome.
type Result struct {
	Name     string
	Status   Status
	Duration time.Duration
	ExitCode int
	Output   string
	Reason   string
}

// Ran reports whether the test was executed at all.
func (r Result) Ran() bool { return r.Status != NotRun }

// Summary is the whole run.
type Summary struct {
	Results  []Result
	Passed   int
	Failed   int
	Skipped  int
	NotRun   int
	Duration time.Duration
}

// Test is one test with the properties that decide how it is judged.
type Test struct {
	Name       string
	Command    []string
	WorkDir    string
	Env        []string
	Labels     []string
	WillFail   bool
	Disabled   bool
	Timeout    time.Duration
	SkipCode   int
	HasSkip    bool
	PassRegex  string
	FailRegex  string
	SkipRegex  string
	Attachment string
}

// Run reads a build tree's tests and runs the ones the filters select.
func Run(ctx context.Context, cfg Config) (*Summary, error) {
	tests, err := Load(ctx, cfg.BinaryDir, cfg.Env)
	if err != nil {
		return nil, err
	}
	selected, err := filter(tests, cfg)
	if err != nil {
		return nil, err
	}

	out := orStdout(cfg.Out)
	if cfg.ShowOnly {
		for i, t := range selected {
			fmt.Fprintf(out, "  Test #%d: %s\n", i+1, t.Name)
		}
		fmt.Fprintf(out, "\nTotal Tests: %d\n", len(selected))
		return &Summary{}, nil
	}
	if len(selected) == 0 {
		fmt.Fprintln(out, "No tests were found!!!")
		return &Summary{}, nil
	}

	fmt.Fprintf(out, "Test project %s\n", filepath.ToSlash(cfg.BinaryDir))
	return runAll(ctx, cfg, selected, out), nil
}

// Load reads the test list out of a build tree.
func Load(ctx context.Context, binaryDir string, env []string) ([]Test, error) {
	abs, err := filepath.Abs(binaryDir)
	if err != nil {
		return nil, err
	}
	abs = filepath.ToSlash(abs)
	root := abs + "/" + eval.TestFileName
	if _, err := os.Stat(root); err != nil {
		return nil, fmt.Errorf("%s does not contain %s;\n"+
			"  either the project declared no tests or it has not been configured",
			binaryDir, eval.TestFileName)
	}

	state := eval.NewState(abs, abs, env)
	state.LogSink = func(string, string) {} // a test file should print nothing
	if err := eval.EvalCacheFile(ctx, state, realFS{}, root); err != nil {
		return nil, err
	}

	tests := make([]Test, 0, len(state.Tests))
	for _, entry := range state.Tests {
		tests = append(tests, testFromState(state, entry))
	}
	return tests, nil
}

// testFromState folds a declared test together with its properties.
func testFromState(state *eval.State, entry eval.TestEntry) Test {
	prop := func(name string) string {
		return state.Properties["TEST:"+entry.Name+":"+name]
	}
	t := Test{
		Name:      entry.Name,
		Command:   entry.Command,
		WorkDir:   entry.WorkDir,
		Labels:    eval.SplitList(prop("LABELS")),
		WillFail:  eval.IsOn(prop("WILL_FAIL")),
		Disabled:  eval.IsOn(prop("DISABLED")),
		PassRegex: prop("PASS_REGULAR_EXPRESSION"),
		FailRegex: prop("FAIL_REGULAR_EXPRESSION"),
		SkipRegex: prop("SKIP_REGULAR_EXPRESSION"),
	}
	if v := prop("WORKING_DIRECTORY"); v != "" {
		t.WorkDir = v
	}
	if v := prop("TIMEOUT"); v != "" {
		if seconds, err := strconv.ParseFloat(v, 64); err == nil {
			t.Timeout = time.Duration(seconds * float64(time.Second))
		}
	}
	if v := prop("SKIP_RETURN_CODE"); v != "" {
		if code, err := strconv.Atoi(v); err == nil {
			t.SkipCode, t.HasSkip = code, true
		}
	}
	// ENVIRONMENT is a list of KEY=VALUE entries layered over the caller's.
	t.Env = eval.SplitList(prop("ENVIRONMENT"))
	return t
}

// filter applies the four selection expressions.
func filter(tests []Test, cfg Config) ([]Test, error) {
	compile := func(expr, flag string) (*regexp.Regexp, error) {
		if expr == "" {
			return nil, nil
		}
		re, err := regexp.Compile(expr)
		if err != nil {
			return nil, fmt.Errorf("%s: %q is not a valid regular expression: %v", flag, expr, err)
		}
		return re, nil
	}
	include, err := compile(cfg.Include, "-R")
	if err != nil {
		return nil, err
	}
	exclude, err := compile(cfg.Exclude, "-E")
	if err != nil {
		return nil, err
	}
	includeLabel, err := compile(cfg.IncludeLabel, "-L")
	if err != nil {
		return nil, err
	}
	excludeLabel, err := compile(cfg.ExcludeLabel, "-LE")
	if err != nil {
		return nil, err
	}

	var out []Test
	for _, t := range tests {
		if include != nil && !include.MatchString(t.Name) {
			continue
		}
		if exclude != nil && exclude.MatchString(t.Name) {
			continue
		}
		if includeLabel != nil && !anyMatch(includeLabel, t.Labels) {
			continue
		}
		if excludeLabel != nil && anyMatch(excludeLabel, t.Labels) {
			continue
		}
		out = append(out, t)
	}
	return out, nil
}

func anyMatch(re *regexp.Regexp, values []string) bool {
	for _, v := range values {
		if re.MatchString(v) {
			return true
		}
	}
	return false
}

// runAll executes the selected tests and prints ctest's progress lines.
func runAll(ctx context.Context, cfg Config, tests []Test, out io.Writer) *Summary {
	start := time.Now()
	summary := &Summary{Results: make([]Result, len(tests))}

	jobs := cfg.Jobs
	if jobs < 1 {
		jobs = 1
	}
	width := len(strconv.Itoa(len(tests)))
	nameWidth := 0
	for _, t := range tests {
		if len(t.Name) > nameWidth {
			nameWidth = len(t.Name)
		}
	}

	var mu sync.Mutex
	var wg sync.WaitGroup
	slots := make(chan struct{}, jobs)
	stopped := false

	for i := range tests {
		mu.Lock()
		halt := stopped
		mu.Unlock()
		if halt {
			break
		}

		wg.Add(1)
		slots <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-slots }()

			res := runOne(ctx, cfg, tests[i])

			mu.Lock()
			defer mu.Unlock()
			summary.Results[i] = res
			fmt.Fprintf(out, "%*d/%d Test #%d: %s %s%s%6.2f sec\n",
				width, i+1, len(tests), i+1, res.Name,
				strings.Repeat(".", nameWidth-len(res.Name)+3),
				statusLabel(res), res.Duration.Seconds())
			if res.Status == Failed && cfg.OutputOnFailure && res.Output != "" {
				fmt.Fprintf(out, "%s\n", strings.TrimRight(res.Output, "\n"))
			}
			if res.Status == Failed && cfg.StopOnFailure {
				stopped = true
			}
		}(i)
	}
	wg.Wait()

	for _, r := range summary.Results {
		if r.Name == "" {
			continue // never started, because the run stopped early
		}
		switch r.Status {
		case Passed:
			summary.Passed++
		case Failed:
			summary.Failed++
		case Skipped:
			summary.Skipped++
		case NotRun:
			summary.NotRun++
		}
	}
	summary.Duration = time.Since(start)
	report(out, summary)
	return summary
}

// statusLabel is the verdict ctest prints in the progress line.
func statusLabel(r Result) string {
	switch r.Status {
	case NotRun:
		return "***Not Run (Disabled) "
	case Skipped:
		return "***Skipped "
	case Failed:
		return "***Failed  "
	default:
		return "   Passed  "
	}
}

// runOne executes a single test and decides what its exit status meant.
func runOne(ctx context.Context, cfg Config, t Test) Result {
	res := Result{Name: t.Name}
	if t.Disabled {
		res.Status = NotRun
		res.Reason = "Disabled"
		return res
	}
	if len(t.Command) == 0 {
		res.Status = Failed
		res.Reason = "no command"
		return res
	}

	if t.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, t.Timeout)
		defer cancel()
	}

	var buf bytes.Buffer
	start := time.Now()
	code, err := cfg.Runner.Run(ctx, run.Command{
		Argv:   t.Command,
		Dir:    t.WorkDir,
		Env:    append(append([]string{}, cfg.Env...), t.Env...),
		Stdout: &buf,
		Stderr: &buf,
	})
	res.Duration = time.Since(start)
	res.Output = buf.String()
	res.ExitCode = code

	if err != nil {
		res.Status = Failed
		res.Reason = err.Error()
		return res
	}
	if ctx.Err() == context.DeadlineExceeded {
		res.Status = Failed
		res.Reason = "Timeout"
		return res
	}

	// The properties decide what the exit status meant, in the order ctest
	// applies them: a skip beats everything, then the output expressions, then
	// the status itself, and WILL_FAIL inverts whatever that concluded.
	if t.HasSkip && code == t.SkipCode {
		res.Status = Skipped
		res.Reason = "Skipped"
		return res
	}
	if t.SkipRegex != "" && matches(t.SkipRegex, res.Output) {
		res.Status = Skipped
		res.Reason = "Skipped"
		return res
	}

	passed := code == 0
	if t.PassRegex != "" {
		passed = matches(t.PassRegex, res.Output)
	}
	if t.FailRegex != "" && matches(t.FailRegex, res.Output) {
		passed = false
	}
	if t.WillFail {
		passed = !passed
	}
	if passed {
		res.Status = Passed
		return res
	}
	res.Status = Failed
	res.Reason = fmt.Sprintf("Failed (exit status %d)", code)
	return res
}

func matches(expr, text string) bool {
	re, err := regexp.Compile(expr)
	if err != nil {
		return false
	}
	return re.MatchString(text)
}

// report prints the closing summary, which is the part people read.
//
// A disabled test is left out of the totals entirely: it did not run, so
// counting it as either a pass or a failure would misstate what happened. A
// skipped test did run and is counted as not having failed, which is why the
// percentage can be 100% with tests in the "did not run" list below it.
func report(out io.Writer, s *Summary) {
	ran := s.Passed + s.Failed + s.Skipped
	fmt.Fprintln(out)
	if ran > 0 {
		fmt.Fprintf(out, "%d%% tests passed, %d tests failed out of %d\n",
			100*(ran-s.Failed)/ran, s.Failed, ran)
	}
	fmt.Fprintf(out, "\nTotal Test time (real) = %6.2f sec\n", s.Duration.Seconds())

	var notRun, failed []string
	for i, r := range s.Results {
		switch {
		case r.Name == "":
		case r.Status == NotRun || r.Status == Skipped:
			notRun = append(notRun, fmt.Sprintf("%6d - %s (%s)", i+1, r.Name, r.Reason))
		case r.Status == Failed:
			failed = append(failed, fmt.Sprintf("%6d - %s (%s)", i+1, r.Name, r.Reason))
		}
	}
	if len(notRun) > 0 {
		fmt.Fprintln(out, "\nThe following tests did not run:")
		sort.Strings(notRun)
		for _, line := range notRun {
			fmt.Fprintln(out, line)
		}
	}
	if len(failed) > 0 {
		fmt.Fprintln(out, "\nThe following tests FAILED:")
		sort.Strings(failed)
		for _, line := range failed {
			fmt.Fprintln(out, line)
		}
	}
}

func orStdout(w io.Writer) io.Writer {
	if w == nil {
		return os.Stdout
	}
	return w
}

// realFS is the filesystem the test files are read through.
type realFS struct{}

func (realFS) ReadFile(name string) ([]byte, error) { return os.ReadFile(name) }
func (realFS) WriteFile(name string, data []byte) error {
	return os.WriteFile(name, data, 0644)
}
func (realFS) MkdirAll(name string) error            { return os.MkdirAll(name, 0755) }
func (realFS) Glob(pattern string) ([]string, error) { return filepath.Glob(pattern) }
func (realFS) Stat(name string) (fs.FileInfo, error) { return os.Stat(name) }
func (realFS) Remove(name string) error              { return os.Remove(name) }
func (realFS) RemoveAll(name string) error           { return os.RemoveAll(name) }

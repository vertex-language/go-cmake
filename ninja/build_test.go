package ninja

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// recordingRunner executes nothing; it records the command lines it was given
// and creates the outputs, so the scheduler can be tested without a compiler.
type recordingRunner struct {
	mu       sync.Mutex
	commands []string
	// touch names the file each command creates, keyed by a substring of the
	// command, so a test can make an edge actually produce its output.
	produce map[string]string
	delay   time.Duration
}

func (r *recordingRunner) Run(ctx context.Context, cmd, dir string, stdout, stderr io.Writer) error {
	if r.delay > 0 {
		time.Sleep(r.delay)
	}
	r.mu.Lock()
	r.commands = append(r.commands, cmd)
	r.mu.Unlock()
	for key, out := range r.produce {
		if strings.Contains(cmd, key) {
			if err := os.MkdirAll(filepath.Dir(out), 0755); err != nil {
				return err
			}
			// A distinct timestamp per write; a filesystem with one-second
			// resolution would otherwise make a fresh output look stale.
			if err := os.WriteFile(out, []byte(cmd), 0644); err != nil {
				return err
			}
			now := time.Now().Add(time.Second)
			_ = os.Chtimes(out, now, now)
		}
	}
	return nil
}

func (r *recordingRunner) ran() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string{}, r.commands...)
}

// buildFile writes a build.ninja and returns the driver for it.
func buildFile(t *testing.T, dir, src string, runner CommandRunner) *Driver {
	t.Helper()
	path := filepath.Join(dir, "build.ninja")
	if err := os.WriteFile(path, []byte(src), 0644); err != nil {
		t.Fatal(err)
	}
	f, err := Parse(OSFS(), path)
	if err != nil {
		t.Fatal(err)
	}
	return &Driver{
		File:   f,
		Runner: runner,
		Log:    NewLog(),
		Out:    io.Discard,
		Err:    io.Discard,
	}
}

func TestBuildRunsMissingOutputs(t *testing.T) {
	dir := t.TempDir()
	out := filepath.ToSlash(filepath.Join(dir, "out.txt"))
	src := filepath.ToSlash(filepath.Join(dir, "in.txt"))
	os.WriteFile(src, []byte("x"), 0644)

	runner := &recordingRunner{produce: map[string]string{"make": out}}
	d := buildFile(t, dir, "rule r\n  command = make $in $out\n\nbuild "+out+": r "+src+"\n", runner)

	res, err := d.Build(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Built != 1 {
		t.Errorf("built %d edges, want 1", res.Built)
	}
	if len(runner.ran()) != 1 {
		t.Errorf("ran %v", runner.ran())
	}
}

func TestBuildSkipsUpToDateOutputs(t *testing.T) {
	dir := t.TempDir()
	out := filepath.ToSlash(filepath.Join(dir, "out.txt"))
	src := filepath.ToSlash(filepath.Join(dir, "in.txt"))
	os.WriteFile(src, []byte("x"), 0644)
	// The output already exists and is newer than the input.
	os.WriteFile(out, []byte("done"), 0644)
	future := time.Now().Add(time.Minute)
	os.Chtimes(out, future, future)

	runner := &recordingRunner{}
	d := buildFile(t, dir, "rule r\n  command = make $in $out\n\nbuild "+out+": r "+src+"\n", runner)
	// The log records the same command, so nothing looks changed.
	d.Log.RecordCommand(out, "make "+src+" "+out, time.Now())

	res, err := d.Build(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Built != 0 {
		t.Errorf("built %d edges, want 0", res.Built)
	}
	if len(runner.ran()) != 0 {
		t.Errorf("ran %v when nothing needed doing", runner.ran())
	}
}

// TestBuildRerunsOnCommandChange is the property the build log exists for:
// changing a compile flag alters no timestamp, and without the log the build
// would keep an object compiled with the old flags.
func TestBuildRerunsOnCommandChange(t *testing.T) {
	dir := t.TempDir()
	out := filepath.ToSlash(filepath.Join(dir, "out.txt"))
	src := filepath.ToSlash(filepath.Join(dir, "in.txt"))
	os.WriteFile(src, []byte("x"), 0644)
	os.WriteFile(out, []byte("done"), 0644)
	future := time.Now().Add(time.Minute)
	os.Chtimes(out, future, future)

	runner := &recordingRunner{produce: map[string]string{"make": out}}
	d := buildFile(t, dir, "rule r\n  command = make $in $out\n\nbuild "+out+": r "+src+"\n", runner)
	d.Log.RecordCommand(out, "make with DIFFERENT flags", time.Now())

	res, err := d.Build(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Built != 1 {
		t.Errorf("built %d edges; a changed command must force a rebuild", res.Built)
	}
}

func TestBuildRespectsDependencyOrder(t *testing.T) {
	dir := t.TempDir()
	a := filepath.ToSlash(filepath.Join(dir, "a"))
	b := filepath.ToSlash(filepath.Join(dir, "b"))
	c := filepath.ToSlash(filepath.Join(dir, "c"))
	os.WriteFile(a, []byte("a"), 0644)

	runner := &recordingRunner{produce: map[string]string{"first": b, "second": c}}
	src := "rule r1\n  command = first $in $out\n" +
		"rule r2\n  command = second $in $out\n\n" +
		"build " + b + ": r1 " + a + "\n" +
		"build " + c + ": r2 " + b + "\n"
	d := buildFile(t, dir, src, runner)

	if _, err := d.Build(context.Background()); err != nil {
		t.Fatal(err)
	}
	ran := runner.ran()
	if len(ran) != 2 {
		t.Fatalf("ran %v", ran)
	}
	if !strings.HasPrefix(ran[0], "first") || !strings.HasPrefix(ran[1], "second") {
		t.Errorf("commands ran out of order: %v", ran)
	}
}

func TestBuildPhonyEdgesAreNotCounted(t *testing.T) {
	dir := t.TempDir()
	out := filepath.ToSlash(filepath.Join(dir, "out"))
	src := filepath.ToSlash(filepath.Join(dir, "in"))
	os.WriteFile(src, []byte("x"), 0644)

	runner := &recordingRunner{produce: map[string]string{"make": out}}
	d := buildFile(t, dir,
		"rule r\n  command = make $in $out\n\n"+
			"build "+out+": r "+src+"\n"+
			"build all: phony "+out+"\n"+
			"default all\n", runner)

	res, err := d.Build(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// One real command ran; the two phony edges are bookkeeping, and counting
	// them would make an incremental build look busier than it was.
	if res.Built != 1 {
		t.Errorf("built = %d, want 1", res.Built)
	}
}

func TestBuildMissingSourceIsAnError(t *testing.T) {
	dir := t.TempDir()
	out := filepath.ToSlash(filepath.Join(dir, "out"))
	missing := filepath.ToSlash(filepath.Join(dir, "nope"))

	d := buildFile(t, dir, "rule r\n  command = make\n\nbuild "+out+": r "+missing+"\n", &recordingRunner{})
	if _, err := d.Build(context.Background()); err == nil {
		t.Error("a missing source with no rule to make it must be an error")
	}
}

func TestBuildUnknownRuleIsAnError(t *testing.T) {
	dir := t.TempDir()
	d := buildFile(t, dir, "build out: nosuchrule\n", &recordingRunner{})
	if _, err := d.Build(context.Background()); err == nil {
		t.Error("an unknown rule must be an error")
	}
}

func TestBuildDuplicateOutputIsAnError(t *testing.T) {
	dir := t.TempDir()
	d := buildFile(t, dir,
		"rule r\n  command = make\n\nbuild out: r\nbuild out: r\n", &recordingRunner{})
	if _, err := d.Build(context.Background()); err == nil {
		t.Error("two rules generating the same output must be an error")
	}
}

func TestBuildExpandsInAndOut(t *testing.T) {
	dir := t.TempDir()
	out := filepath.ToSlash(filepath.Join(dir, "out"))
	in1 := filepath.ToSlash(filepath.Join(dir, "in1"))
	in2 := filepath.ToSlash(filepath.Join(dir, "in2"))
	os.WriteFile(in1, []byte("1"), 0644)
	os.WriteFile(in2, []byte("2"), 0644)

	runner := &recordingRunner{produce: map[string]string{"cc": out}}
	d := buildFile(t, dir,
		"rule cc\n  command = cc $FLAGS $in -o $out\n\n"+
			"build "+out+": cc "+in1+" "+in2+"\n  FLAGS = -O2\n", runner)

	if _, err := d.Build(context.Background()); err != nil {
		t.Fatal(err)
	}
	ran := runner.ran()
	if len(ran) != 1 {
		t.Fatalf("ran %v", ran)
	}
	want := "cc -O2 " + in1 + " " + in2 + " -o " + out
	if ran[0] != want {
		t.Errorf("command = %q\nwant       %q", ran[0], want)
	}
}

func TestBuildTargetSelection(t *testing.T) {
	dir := t.TempDir()
	a := filepath.ToSlash(filepath.Join(dir, "a"))
	b := filepath.ToSlash(filepath.Join(dir, "b"))

	runner := &recordingRunner{produce: map[string]string{"makea": a, "makeb": b}}
	d := buildFile(t, dir,
		"rule ra\n  command = makea $out\nrule rb\n  command = makeb $out\n\n"+
			"build "+a+": ra\nbuild "+b+": rb\n", runner)
	d.Targets = []string{a}

	if _, err := d.Build(context.Background()); err != nil {
		t.Fatal(err)
	}
	ran := runner.ran()
	if len(ran) != 1 || !strings.HasPrefix(ran[0], "makea") {
		t.Errorf("asking for one target ran %v", ran)
	}
}

// TestBuildParallelLogWrites is the regression test for a crash that only
// appeared under load: every finished edge records its command in the build
// log, and an unsynchronised map made a parallel build take the process down
// intermittently. The race detector needs cgo, which is not always available,
// so this reproduces the pressure directly.
func TestBuildParallelLogWrites(t *testing.T) {
	dir := t.TempDir()
	const n = 64

	var src strings.Builder
	src.WriteString("rule r\n  command = make $out\n\n")
	produce := map[string]string{}
	for i := 0; i < n; i++ {
		out := filepath.ToSlash(filepath.Join(dir, "out"+itoa(i)))
		src.WriteString("build " + out + ": r\n")
		produce["make "+out] = out
	}

	runner := &recordingRunner{produce: produce}
	d := buildFile(t, dir, src.String(), runner)
	d.Jobs = 16

	res, err := d.Build(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Built != n {
		t.Errorf("built %d of %d edges", res.Built, n)
	}
	// Every output must have been recorded, which is only true if no write was
	// lost to a data race.
	for i := 0; i < n; i++ {
		out := filepath.ToSlash(filepath.Join(dir, "out"+itoa(i)))
		if _, ok := d.Log.Command(out); !ok {
			t.Fatalf("no log entry for %s", out)
		}
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}
